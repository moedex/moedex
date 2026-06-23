// Command moedex-serve is the warm retrieval daemon: it mmaps a directory of
// prebuilt shards once and answers queries with zero cold-start, either over a
// small HTTP API (-http) or as a one-shot query (-q) for validation.
//
// This is the v0 serving surface — line-granular retrieval, parity-proven
// against ripgrep. The ranked, token-budgeted MCP context path (search_context)
// composes over the same server.Corpus and lands in a later slice.
//
// Usage:
//
//	moedex-serve -shard-dir DIR -http :8080         # retrieval daemon (HTTP)
//	moedex-serve -shard-dir DIR -q PATTERN [-regex]  # one-shot retrieval query
//	moedex-serve -shard-dir DIR -mcp                 # ranked agent context (MCP/stdio)
//
// Dense arm (optional, -mcp only): set MOEDEX_EMBED_URL and MOEDEX_EMBED_MODEL to
// light up embedding-based retrieval. Without them the ranker is pure-lexical +
// symbol arm with zero external dependencies.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"moedex/internal/embed"
	"moedex/internal/mcp"
	"moedex/internal/search"
	"moedex/internal/server"
)

func main() {
	shardDir := flag.String("shard-dir", os.Getenv("MOEDEX_SHARD_DIR"), "directory of prebuilt *.idx shards")
	httpAddr := flag.String("http", "", "if set, serve the retrieval HTTP API on this address (e.g. :8080)")
	mcpMode := flag.Bool("mcp", false, "serve ranked agent context over MCP (stdio)")
	q := flag.String("q", "", "one-shot retrieval query")
	isRegex := flag.Bool("regex", false, "treat -q as a regular expression (default: literal)")
	limit := flag.Int("limit", 0, "cap matches printed/returned (0 = no cap)")
	topK := flag.Int("top-k", 20, "default ranked results per MCP query")
	embedKind := flag.String("embed", "auto", "dense embedder for -mcp: auto|onnx|http|none (auto = onnx if -onnx-runtime/ONNXRUNTIME_LIB_PATH set, else http if MOEDEX_EMBED_URL set, else none)")
	onnxRuntime := flag.String("onnx-runtime", os.Getenv("ONNXRUNTIME_LIB_PATH"), "path to the ONNX Runtime shared library (in-process embedder; requires -tags onnx build)")
	flag.Parse()

	if *shardDir == "" {
		fmt.Fprintln(os.Stderr, "moedex-serve: -shard-dir is required (or set MOEDEX_SHARD_DIR)")
		os.Exit(2)
	}
	if !*mcpMode && *httpAddr == "" && *q == "" {
		fmt.Fprintln(os.Stderr, "moedex-serve: provide -mcp (agent context), -http ADDR (retrieval daemon), or -q PATTERN (one-shot)")
		os.Exit(2)
	}

	if *mcpMode {
		if err := runMCP(*shardDir, *topK, *embedKind, *onnxRuntime); err != nil {
			fmt.Fprintf(os.Stderr, "moedex-serve: %v\n", err)
			os.Exit(1)
		}
		return
	}

	start := time.Now()
	corpus, err := server.Open(*shardDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "moedex-serve: %v\n", err)
		os.Exit(1)
	}
	defer corpus.Close()
	fmt.Fprintf(os.Stderr, "moedex-serve: mmap'd %d shards, %d blobs in %s\n",
		corpus.NumShards(), corpus.NumBlobs(), time.Since(start).Round(time.Millisecond))

	if *q != "" {
		runOneShot(corpus, *q, *isRegex, *limit)
		return
	}
	if err := runHTTP(corpus, *httpAddr); err != nil {
		fmt.Fprintf(os.Stderr, "moedex-serve: %v\n", err)
		os.Exit(1)
	}
}

// runMCP builds the corpus ranker and serves ranked, token-budgeted context over
// MCP stdio — the agent-facing surface. The dense arm lights up only when
// MOEDEX_EMBED_URL is configured; building it embeds the whole corpus at boot.
func runMCP(shardDir string, topK int, embedKind, onnxRuntime string) error {
	ctx := context.Background()
	start := time.Now()
	cfg := server.RankConfig{TopK: topK}

	// Choose the dense embedder. On any build/connect failure we fall back cleanly
	// to lexical+symbol. Embeddings are persisted next to the shards so subsequent
	// boots load instead of re-embedding the whole corpus.
	dense, err := configureDenseArm(&cfg, shardDir, embedKind, onnxRuntime)
	if err != nil {
		fmt.Fprintf(os.Stderr, "moedex-serve: dense arm setup failed (%v); ranking lexical+symbol\n", err)
		cfg.Emb, dense = nil, false
	}
	if !dense {
		fmt.Fprintln(os.Stderr, "moedex-serve: dense arm disabled; ranking lexical+symbol")
	}

	rc, err := server.OpenRank(ctx, shardDir, cfg)
	if err != nil && dense {
		// Dense build failed (service down, bad model, etc.): degrade to lexical.
		fmt.Fprintf(os.Stderr, "moedex-serve: dense arm failed (%v); falling back to lexical+symbol\n", err)
		cfg.Emb = nil
		rc, err = server.OpenRank(ctx, shardDir, cfg)
	}
	if err != nil {
		return err
	}
	denseSrc := "none"
	if rc.DenseChunks() > 0 {
		if rc.DenseFromCache() {
			denseSrc = "cached"
		} else {
			denseSrc = "built"
		}
	}
	fmt.Fprintf(os.Stderr, "moedex-serve: corpus ranker ready — %d blobs, %d docs, %d symbol blobs, %d dense chunks (%s) in %s\n",
		rc.NumBlobs(), rc.NumDocs(), rc.NumSymbolBlobs(), rc.DenseChunks(), denseSrc, time.Since(start).Round(time.Millisecond))

	srv := mcp.NewServer(rc)
	fmt.Fprintln(os.Stderr, "moedex-serve: MCP ready on stdio")
	return srv.Serve(ctx, os.Stdin, os.Stdout)
}

// configureDenseArm picks the dense embedder per `kind` and wires it (plus the
// persisted embedding cache path) into cfg. Returns dense=false with no error
// when the dense arm is intentionally off; returns an error only when a
// requested embedder could not be constructed.
func configureDenseArm(cfg *server.RankConfig, shardDir, kind, onnxRuntime string) (bool, error) {
	url := os.Getenv("MOEDEX_EMBED_URL")
	if kind == "auto" {
		switch {
		case onnxRuntime != "":
			kind = "onnx"
		case url != "":
			kind = "http"
		default:
			kind = "none"
		}
	}

	cfg.StorePath = filepath.Join(shardDir, "corpus-embeddings.store")
	switch kind {
	case "none":
		cfg.StorePath = ""
		return false, nil
	case "onnx":
		emb, err := embed.NewONNXEmbedder(onnxRuntime)
		if err != nil {
			return false, err
		}
		cfg.Emb = emb
		cfg.EmbedModel = "st-codesearch-distilroberta-onnx"
		fmt.Fprintf(os.Stderr, "moedex-serve: dense arm = in-process st-codesearch-distilroberta (onnx, code-trained); embedding cache %s\n", cfg.StorePath)
		return true, nil
	case "http":
		if url == "" {
			return false, fmt.Errorf("embed=http but MOEDEX_EMBED_URL is unset")
		}
		model := os.Getenv("MOEDEX_EMBED_MODEL")
		cfg.Emb = embed.NewHTTPEmbedder(url, model)
		cfg.EmbedModel = model
		fmt.Fprintf(os.Stderr, "moedex-serve: dense arm = http %s (model %q); embedding cache %s\n", url, model, cfg.StorePath)
		return true, nil
	default:
		return false, fmt.Errorf("unknown -embed %q (want auto|onnx|http|none)", kind)
	}
}

// runOneShot executes a single query and prints repo/relpath:line, one per line.
func runOneShot(c *server.Corpus, pattern string, isRegex bool, limit int) {
	var matches []search.Match
	if isRegex {
		m, _, err := c.Regex(pattern)
		if err != nil {
			fmt.Fprintf(os.Stderr, "moedex-serve: %v\n", err)
			os.Exit(1)
		}
		matches = m
	} else {
		matches, _ = c.Literal(pattern)
	}
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	for _, m := range matches {
		fmt.Printf("%s/%s:%d\n", m.Repo, m.RelPath, m.Line)
	}
	fmt.Fprintf(os.Stderr, "moedex-serve: %d match(es)\n", len(matches))
}

// runHTTP serves the corpus over a minimal JSON API until SIGINT/SIGTERM.
func runHTTP(c *server.Corpus, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"shards": c.NumShards(),
			"blobs":  c.NumBlobs(),
		})
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		handleSearch(c, w, r)
	})

	srv := &http.Server{Addr: addr, Handler: mux}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	fmt.Fprintf(os.Stderr, "moedex-serve: HTTP listening on %s\n", addr)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case <-stop:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		fmt.Fprintln(os.Stderr, "\nmoedex-serve: shutting down")
		return srv.Shutdown(ctx)
	}
}

// searchResponse is the JSON shape returned by GET /search.
type searchResponse struct {
	Query   string      `json:"query"`
	Regex   bool        `json:"regex"`
	Count   int         `json:"count"`
	Matches []matchJSON `json:"matches"`
	Stats   *statsJSON  `json:"stats,omitempty"`
	Error   string      `json:"error,omitempty"`
}

type matchJSON struct {
	Repo    string `json:"repo"`
	RelPath string `json:"rel_path"`
	AbsPath string `json:"abs_path"`
	Line    int    `json:"line"`
}

type statsJSON struct {
	CandidateBlobs int   `json:"candidate_blobs"`
	CandidateBytes int64 `json:"candidate_bytes"`
	QueryAll       bool  `json:"query_all"`
}

// handleSearch answers GET /search?q=PATTERN&regex=1&limit=N with JSON matches.
func handleSearch(c *server.Corpus, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeJSON(w, http.StatusBadRequest, searchResponse{Error: "missing required query param: q"})
		return
	}
	isRegex := r.URL.Query().Get("regex") == "1" || r.URL.Query().Get("regex") == "true"
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	var matches []search.Match
	var stats search.Stats
	if isRegex {
		m, s, err := c.Regex(q)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, searchResponse{Query: q, Regex: true, Error: err.Error()})
			return
		}
		matches, stats = m, s
	} else {
		matches, stats = c.Literal(q)
	}

	total := len(matches)
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]matchJSON, len(matches))
	for i, m := range matches {
		out[i] = matchJSON{Repo: m.Repo, RelPath: m.RelPath, AbsPath: m.AbsPath, Line: m.Line}
	}
	writeJSON(w, http.StatusOK, searchResponse{
		Query:   q,
		Regex:   isRegex,
		Count:   total,
		Matches: out,
		Stats:   &statsJSON{CandidateBlobs: stats.CandidateBlobs, CandidateBytes: stats.CandidateBytes, QueryAll: stats.QueryAll},
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
