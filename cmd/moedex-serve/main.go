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
	"log/slog"
	"net"
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
	// Load a KEY=VALUE config file (systemd EnvironmentFile format) BEFORE the
	// flag defaults below read the environment, so the file feeds those defaults.
	// Layering: an explicit flag overrides the file, the file overrides the
	// process env, the env overrides the built-in default. Parsed straight from
	// os.Args because it must run before flag.Parse.
	if cp := extractConfigPath(os.Args[1:]); cp != "" {
		if err := loadEnvFile(cp); err != nil {
			fmt.Fprintf(os.Stderr, "moedex-serve: -config %s: %v\n", cp, err)
			os.Exit(2)
		}
	}
	flag.String("config", "", "load a KEY=VALUE settings file (systemd EnvironmentFile format) before flags; flag > file > env > default")

	shardDir := flag.String("shard-dir", os.Getenv("MOEDEX_SHARD_DIR"), "directory of prebuilt *.idx shards")
	httpAddr := flag.String("http", os.Getenv("MOEDEX_HTTP_ADDR"), "if set (or MOEDEX_HTTP_ADDR), serve the retrieval HTTP API on this address (e.g. 127.0.0.1:8080)")
	mcpMode := flag.Bool("mcp", false, "serve ranked agent context over MCP (stdio)")
	q := flag.String("q", "", "one-shot retrieval query")
	isRegex := flag.Bool("regex", false, "treat -q as a regular expression (default: literal)")
	limit := flag.Int("limit", 0, "cap matches printed/returned (0 = no cap)")
	topK := flag.Int("top-k", 20, "default ranked results per MCP query")
	embedKind := flag.String("embed", envOr("MOEDEX_EMBED", "auto"), "dense embedder for -mcp: auto|onnx|http|none (auto = onnx if -onnx-runtime/ONNXRUNTIME_LIB_PATH set, else http if MOEDEX_EMBED_URL set, else none)")
	onnxRuntime := flag.String("onnx-runtime", os.Getenv("ONNXRUNTIME_LIB_PATH"), "path to the ONNX Runtime shared library (in-process embedder; requires -tags onnx build)")
	authToken := flag.String("auth-token", "", "if set (or MOEDEX_AUTH_TOKEN), require `Authorization: Bearer <token>` on -http (except /healthz, /metrics)")
	tlsCert := flag.String("tls-cert", os.Getenv("MOEDEX_TLS_CERT"), "TLS certificate file; serve -http over HTTPS (requires -tls-key)")
	tlsKey := flag.String("tls-key", os.Getenv("MOEDEX_TLS_KEY"), "TLS private key file; serve -http over HTTPS (requires -tls-cert)")
	requestTimeout := flag.Duration("request-timeout", 30*time.Second, "per-request HTTP timeout on -http (503 on expiry; the underlying scan observes cancellation and aborts promptly)")
	flag.Parse()

	// Auth precedence: MOEDEX_AUTH_TOKEN is the base, -auth-token overrides it.
	authTok := os.Getenv("MOEDEX_AUTH_TOKEN")
	if *authToken != "" {
		authTok = *authToken
	}

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
	cfg := httpConfig{
		addr:           *httpAddr,
		shardDir:       *shardDir,
		token:          authTok,
		tlsCert:        *tlsCert,
		tlsKey:         *tlsKey,
		requestTimeout: *requestTimeout,
		bootElapsed:    time.Since(start),
		shards:         corpus.NumShards(),
		blobs:          corpus.NumBlobs(),
	}
	if err := runHTTP(corpus, cfg); err != nil {
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

	// Serve through a hot-swappable holder so a SIGHUP can rebuild the ranked
	// corpus (reusing the embedder; the persisted embedding sidecar makes warm
	// reloads cheap, and a refreshed shard set re-embeds) without dropping a
	// request. A failed reload keeps the current ranker.
	holder := newRankHolder(rc)
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			t0 := time.Now()
			fmt.Fprintln(os.Stderr, "moedex-serve: SIGHUP — rebuilding ranked corpus")
			nrc, err := server.OpenRank(ctx, shardDir, cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "moedex-serve: reload failed (%v); keeping current ranker\n", err)
				continue
			}
			old := holder.swap(nrc)
			go old.retire()
			src := "none"
			if nrc.DenseChunks() > 0 {
				if nrc.DenseFromCache() {
					src = "cached"
				} else {
					src = "built"
				}
			}
			fmt.Fprintf(os.Stderr, "moedex-serve: reloaded ranker — %d blobs, %d symbol blobs, %d dense chunks (%s) in %s\n",
				nrc.NumBlobs(), nrc.NumSymbolBlobs(), nrc.DenseChunks(), src, time.Since(t0).Round(time.Millisecond))
		}
	}()

	srv := mcp.NewServer(holder)
	fmt.Fprintln(os.Stderr, "moedex-serve: MCP ready on stdio (SIGHUP to reload)")
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
		m, _, err := c.Regex(context.Background(), pattern)
		if err != nil {
			fmt.Fprintf(os.Stderr, "moedex-serve: %v\n", err)
			os.Exit(1)
		}
		matches = m
	} else {
		m, _, err := c.Literal(context.Background(), pattern)
		if err != nil {
			fmt.Fprintf(os.Stderr, "moedex-serve: %v\n", err)
			os.Exit(1)
		}
		matches = m
	}
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	for _, m := range matches {
		fmt.Printf("%s/%s:%d\n", m.Repo, m.RelPath, m.Line)
	}
	fmt.Fprintf(os.Stderr, "moedex-serve: %d match(es)\n", len(matches))
}

// httpConfig carries the resolved -http settings into runHTTP so the boot-line
// reporting and server wiring stay in one place.
type httpConfig struct {
	addr           string
	shardDir       string
	token          string
	tlsCert        string
	tlsKey         string
	requestTimeout time.Duration
	bootElapsed    time.Duration
	shards         int
	blobs          int
}

// runHTTP serves the corpus over a minimal JSON API until SIGINT/SIGTERM. A
// SIGHUP re-opens shardDir and hot-swaps the served corpus without dropping any
// in-flight request (see reload.go); a failed reload keeps the current corpus.
//
// The mux is wrapped with the hardening chain (recover/log/timeout/auth — see
// middleware.go); /metrics and /healthz stay open. Server timeouts bound slow
// clients; the per-request timeout cancels r.Context(), which /search threads
// into the corpus scan, so an expired request aborts the scan promptly (within
// a stride; see withTimeout) instead of running to completion.
func runHTTP(c *server.Corpus, cfg httpConfig) error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	// TLS is all-or-nothing: a half-configured pair is a usage error, mirroring
	// the flag-validation style in main().
	tls := cfg.tlsCert != "" && cfg.tlsKey != ""
	if (cfg.tlsCert != "") != (cfg.tlsKey != "") {
		fmt.Fprintln(os.Stderr, "moedex-serve: -tls-cert and -tls-key must be set together")
		os.Exit(2)
	}

	effAddr := resolveAddr(cfg.addr, cfg.token)
	slog.Info("boot", "shards", cfg.shards, "blobs", cfg.blobs,
		"elapsed_ms", cfg.bootElapsed.Milliseconds(),
		"requested_addr", cfg.addr, "effective_addr", effAddr, "tls", tls)
	if cfg.token == "" {
		if isLoopback(effAddr) {
			slog.Warn("no auth token configured; /search and /stats are open (loopback bind)")
		} else {
			slog.Warn("no auth token AND non-loopback bind; /search and /stats are open to the network",
				"effective_addr", effAddr)
		}
	}

	holder := newCorpusHolder(c)
	m := newMetrics()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/metrics", metricsHandler(holder, m))
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		snap := holder.acquire()
		defer snap.release()
		writeJSON(w, http.StatusOK, map[string]any{
			"shards": snap.c.NumShards(),
			"blobs":  snap.c.NumBlobs(),
		})
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		snap := holder.acquire()
		defer snap.release()
		handleSearch(snap.c, w, r)
	})

	srv := &http.Server{
		Addr:              effAddr,
		Handler:           chain(mux, cfg.token, cfg.requestTimeout, m),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      cfg.requestTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		if tls {
			errCh <- srv.ListenAndServeTLS(cfg.tlsCert, cfg.tlsKey)
		} else {
			errCh <- srv.ListenAndServe()
		}
	}()
	slog.Info("listening", "addr", effAddr, "tls", tls, "auth", cfg.token != "")

	// SIGHUP -> reload. Processed one at a time on its own goroutine so reloads
	// never overlap and never block request serving.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			start := time.Now()
			slog.Info("reload requested (SIGHUP)")
			nc, err := server.Open(cfg.shardDir)
			if err != nil {
				m.incReload("fail")
				slog.Error("reload failed; keeping current corpus", "err", err.Error())
				continue
			}
			old := holder.swap(nc)
			go old.retire() // unmap the previous corpus once its readers drain
			m.incReload("ok")
			slog.Info("reloaded", "shards", nc.NumShards(), "blobs", nc.NumBlobs(),
				"elapsed_ms", time.Since(start).Milliseconds())
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case <-stop:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		slog.Info("shutting down")
		return srv.Shutdown(ctx)
	}
}

// isLoopback reports whether addr binds a loopback host (for the no-token
// warning severity).
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return host == "localhost"
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
		m, s, err := c.Regex(r.Context(), q)
		if err != nil {
			// A regex error is either a malformed pattern (client's fault, 400) or a
			// cancelled/expired request (no useful body — the TimeoutHandler already
			// owns the 503 response, so just stop).
			if r.Context().Err() != nil {
				return
			}
			writeJSON(w, http.StatusBadRequest, searchResponse{Query: q, Regex: true, Error: err.Error()})
			return
		}
		matches, stats = m, s
	} else {
		m, s, err := c.Literal(r.Context(), q)
		if err != nil {
			// Literal only errors on cancellation today; the TimeoutHandler owns the
			// 503, so just stop. Other errors become a 500.
			if r.Context().Err() != nil {
				return
			}
			writeJSON(w, http.StatusInternalServerError, searchResponse{Query: q, Error: err.Error()})
			return
		}
		matches, stats = m, s
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
