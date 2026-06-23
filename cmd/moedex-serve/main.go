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
//	moedex-serve -shard-dir DIR -http :8080         # long-running daemon
//	moedex-serve -shard-dir DIR -q PATTERN [-regex]  # one-shot query
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"moedex/internal/search"
	"moedex/internal/server"
)

func main() {
	shardDir := flag.String("shard-dir", os.Getenv("MOEDEX_SHARD_DIR"), "directory of prebuilt *.idx shards")
	httpAddr := flag.String("http", "", "if set, serve the HTTP API on this address (e.g. :8080)")
	q := flag.String("q", "", "one-shot query (mutually exclusive with -http)")
	isRegex := flag.Bool("regex", false, "treat -q as a regular expression (default: literal)")
	limit := flag.Int("limit", 0, "cap matches printed/returned (0 = no cap)")
	flag.Parse()

	if *shardDir == "" {
		fmt.Fprintln(os.Stderr, "moedex-serve: -shard-dir is required (or set MOEDEX_SHARD_DIR)")
		os.Exit(2)
	}
	if *httpAddr == "" && *q == "" {
		fmt.Fprintln(os.Stderr, "moedex-serve: provide -http ADDR (daemon) or -q PATTERN (one-shot)")
		os.Exit(2)
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
