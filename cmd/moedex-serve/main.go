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
//	moedex-serve -shard-dir DIR -http :8080          # retrieval daemon (HTTP)
//	moedex-serve -shard-dir DIR -q PATTERN [-regex]  # one-shot retrieval query
//	moedex-serve -shard-dir DIR -mcp                 # ranked agent context (MCP/stdio)
//	moedex-serve -shard-dir DIR -mcp-http :8081      # ranked agent context (MCP/HTTP, warm shared daemon)
//	moedex-serve -shard-dir DIR -build-embeddings    # build/refresh dense sidecar, then exit (out-of-band refresh)
//
// -mcp spawns per agent session (each pays the cold load); -mcp-http loads once
// and serves many sessions over the network — the warm shared surface for coding
// agents (register with `claude mcp add --transport http ... /mcp`).
//
// Dense arm (optional, -mcp/-mcp-http only): set MOEDEX_EMBED_URL and
// MOEDEX_EMBED_MODEL to light up embedding-based retrieval. Without them the ranker
// is pure-lexical + symbol arm with zero external dependencies. -build-embeddings
// builds the dense sidecar out of band so a corpus refresh never makes the warm
// daemon re-embed inline on reload (see scripts/refresh-corpus.sh).
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
	"moedex/internal/version"
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
	mcpMode := flag.Bool("mcp", false, "serve ranked agent context over MCP (stdio); per-session, loads on each launch")
	mcpHTTPAddr := flag.String("mcp-http", os.Getenv("MOEDEX_MCP_HTTP_ADDR"), "if set (or MOEDEX_MCP_HTTP_ADDR), serve the ranked agent-context MCP tool over HTTP (Streamable HTTP) at /mcp on this address (e.g. 127.0.0.1:8081) — the warm shared daemon for coding agents")
	q := flag.String("q", "", "one-shot retrieval query")
	isRegex := flag.Bool("regex", false, "treat -q as a regular expression (default: literal)")
	limit := flag.Int("limit", 0, "cap matches printed/returned (0 = no cap)")
	topK := flag.Int("top-k", 20, "default ranked results per MCP query")
	embedKind := flag.String("embed", envOr("MOEDEX_EMBED", "auto"), "dense embedder for -mcp: auto|onnx|http|none (auto = onnx when a configured or standard runtime is found, else http if MOEDEX_EMBED_URL set, else none)")
	onnxRuntime := flag.String("onnx-runtime", embed.ResolveONNXRuntimePath(""), "path to the ONNX Runtime shared library (in-process embedder; auto-discovers standard Homebrew/system paths; requires -tags onnx build)")
	buildEmbeddings := flag.Bool("build-embeddings", false, "build/refresh the corpus embedding sidecar for -shard-dir, then exit (out-of-band dense refresh; requires -embed onnx|http). Run this before reloading the warm daemon so it never re-embeds the corpus inline.")
	authToken := flag.String("auth-token", "", "if set (or MOEDEX_AUTH_TOKEN), require `Authorization: Bearer <token>` on -http (except /healthz, /metrics)")
	tlsCert := flag.String("tls-cert", os.Getenv("MOEDEX_TLS_CERT"), "TLS certificate file; serve -http over HTTPS (requires -tls-key)")
	tlsKey := flag.String("tls-key", os.Getenv("MOEDEX_TLS_KEY"), "TLS private key file; serve -http over HTTPS (requires -tls-cert)")
	requestTimeout := flag.Duration("request-timeout", 30*time.Second, "per-request HTTP timeout on -http (503 on expiry; the underlying scan observes cancellation and aborts promptly)")
	searchMaxConcurrency := flag.Int("search-max-concurrency", envOrInt("MOEDEX_SEARCH_MAX_CONCURRENCY", defaultSearchMaxConcurrency), "cap concurrent in-flight /search requests on -http; each one scans the full corpus and can pin a core for up to -request-timeout. 0 disables the cap. Mirrors the MCP server's request-concurrency guard.")
	mcpMaxConcurrency := flag.Int("mcp-max-concurrency", envOrInt("MOEDEX_MCP_MAX_CONCURRENCY", defaultMCPMaxConcurrency), "cap concurrent in-flight /mcp requests on -mcp-http; each one runs a ranked search + context assembly and can pin a core for up to -request-timeout. 0 disables the cap. Mirrors -search-max-concurrency.")
	showVersion := flag.Bool("version", false, "print build identity (name, commit, dense capability) and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Line("moedex-serve", embed.ONNXCompiled))
		return
	}

	// Auth precedence: MOEDEX_AUTH_TOKEN is the base, -auth-token overrides it.
	authTok := os.Getenv("MOEDEX_AUTH_TOKEN")
	if *authToken != "" {
		authTok = *authToken
	}

	if *shardDir == "" {
		fmt.Fprintln(os.Stderr, "moedex-serve: -shard-dir is required (or set MOEDEX_SHARD_DIR)")
		os.Exit(2)
	}
	if !*mcpMode && *mcpHTTPAddr == "" && *httpAddr == "" && *q == "" && !*buildEmbeddings {
		fmt.Fprintln(os.Stderr, "moedex-serve: provide -mcp / -mcp-http ADDR (agent context), -http ADDR (retrieval daemon), -q PATTERN (one-shot), or -build-embeddings (dense sidecar refresh)")
		os.Exit(2)
	}

	if *buildEmbeddings {
		if err := runBuildEmbeddings(*shardDir, *topK, *embedKind, *onnxRuntime); err != nil {
			fmt.Fprintf(os.Stderr, "moedex-serve: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *mcpMode {
		if err := runMCP(*shardDir, *topK, *embedKind, *onnxRuntime); err != nil {
			fmt.Fprintf(os.Stderr, "moedex-serve: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *mcpHTTPAddr != "" {
		cfg := mcpHTTPConfig{
			addr:           *mcpHTTPAddr,
			shardDir:       *shardDir,
			token:          authTok,
			tlsCert:        *tlsCert,
			tlsKey:         *tlsKey,
			requestTimeout: *requestTimeout,
			topK:           *topK,
			embedKind:      *embedKind,
			onnxRuntime:    *onnxRuntime,
			maxConcurrency: *mcpMaxConcurrency,
		}
		if err := runMCPHTTP(cfg); err != nil {
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
		addr:                 *httpAddr,
		shardDir:             *shardDir,
		token:                authTok,
		tlsCert:              *tlsCert,
		tlsKey:               *tlsKey,
		requestTimeout:       *requestTimeout,
		bootElapsed:          time.Since(start),
		shards:               corpus.NumShards(),
		blobs:                corpus.NumBlobs(),
		searchMaxConcurrency: *searchMaxConcurrency,
	}
	if err := runHTTP(corpus, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "moedex-serve: %v\n", err)
		os.Exit(1)
	}
}

// openRankCorpus builds the ranked corpus shared by the agent-facing surfaces
// (-mcp stdio and -mcp-http): it configures the dense arm, opens the ranker
// (falling back cleanly to lexical+symbol if the dense build fails), logs the
// ready line, and returns the corpus plus the RESOLVED config so the SIGHUP
// reload path can rebuild with the same settings. The dense arm lights up only
// when an embedder is configured; embeddings are persisted next to the shards so
// subsequent boots load instead of re-embedding the whole corpus.
func openRankCorpus(ctx context.Context, shardDir string, topK int, embedKind, onnxRuntime string) (*server.RankCorpus, server.RankConfig, error) {
	start := time.Now()
	cfg := server.RankConfig{TopK: topK}

	dense, err := configureDenseArm(&cfg, shardDir, embedKind, onnxRuntime)
	if err != nil {
		fmt.Fprintf(os.Stderr, "moedex-serve: dense arm setup failed (%v); ranking lexical+symbol\n", err)
		cfg.Emb, dense = nil, false
	}
	if !dense {
		fmt.Fprintln(os.Stderr, "moedex-serve: dense arm disabled; ranking lexical+symbol")
	}

	rc, cfg, err := openRankOrDegrade(ctx, shardDir, cfg)
	if err != nil {
		return nil, cfg, err
	}
	fmt.Fprintf(os.Stderr, "moedex-serve: corpus ranker ready — %d blobs, %d docs, %d symbol blobs, %d dense chunks (%s) in %s\n",
		rc.NumBlobs(), rc.NumDocs(), rc.NumSymbolBlobs(), rc.DenseChunks(), denseSource(rc), time.Since(start).Round(time.Millisecond))
	return rc, cfg, nil
}

// openRankOrDegrade calls server.OpenRank(ctx, shardDir, cfg) and, when it
// fails while cfg configures a dense arm, retries once with the dense arm
// stripped -- lexical+symbol ranking has no external dependency, so a dense
// arm failure (embed service down, bad model, transient network hiccup)
// should never by itself keep the whole corpus from opening.
//
// It returns the RankConfig actually used to produce the returned corpus
// (identical to cfg on a clean open; a copy with Emb cleared after a
// fallback) so the caller decides whether to keep serving degraded. cfg is
// passed by value and is never mutated, so a caller that discards this
// return value and reuses its own cfg on a later call (as the SIGHUP reload
// paths below do -- see F-22) gets a fresh attempt at the dense arm every
// time, rather than staying degraded forever after one transient failure.
func openRankOrDegrade(ctx context.Context, shardDir string, cfg server.RankConfig) (*server.RankCorpus, server.RankConfig, error) {
	rc, err := server.OpenRank(ctx, shardDir, cfg)
	if err != nil && cfg.Emb != nil {
		// Dense build failed (service down, bad model, etc.): degrade to lexical.
		fmt.Fprintf(os.Stderr, "moedex-serve: dense arm failed (%v); falling back to lexical+symbol\n", err)
		cfg.Emb = nil
		rc, err = server.OpenRank(ctx, shardDir, cfg)
	}
	return rc, cfg, err
}

// denseSource reports where the dense vectors came from, for the boot/reload log.
func denseSource(rc *server.RankCorpus) string {
	if rc.DenseChunks() == 0 {
		return "none"
	}
	if rc.DenseFromCache() {
		return "cached"
	}
	return "built"
}

// runMCP serves ranked, token-budgeted context over MCP stdio — the per-session
// agent surface. Each invocation loads the corpus; for the warm SHARED surface
// that loads once and answers many sessions, see runMCPHTTP / -mcp-http.
// runBuildEmbeddings builds/refreshes the corpus embedding sidecar for shardDir
// out of band, then exits. This is the missing step in the refresh pipeline: the
// token/symbol sidecars rebuild cheaply (moedex-index refresh), but the dense store
// is expensive, and if the warm daemon rebuilt it inline on reload it would stall
// (no serving until the whole corpus re-embeds). Building it here first means the
// daemon's next SIGHUP just LOADS the fresh, fingerprint-matching sidecar — a fast,
// zero-downtime hot-swap.
//
// It reuses the exact dense-arm wiring as serving (configureDenseArm + openRankCorpus
// + the persisted-store fingerprint), so it is idempotent: an up-to-date corpus loads
// the cached store (a quick no-op); a changed shard set invalidates the fingerprint
// and triggers the re-embed. Requires a configured embedder — -embed none has nothing
// to build and is reported as an error.
func runBuildEmbeddings(shardDir string, topK int, embedKind, onnxRuntime string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := server.RankConfig{TopK: topK}
	dense, err := configureDenseArm(&cfg, shardDir, embedKind, onnxRuntime)
	if err != nil {
		return fmt.Errorf("dense arm setup failed: %w", err)
	}
	if !dense || cfg.Emb == nil {
		return fmt.Errorf("no embeddings built: dense arm is off — set -embed onnx with -onnx-runtime <lib> (requires -tags onnx build), or -embed http with MOEDEX_EMBED_URL")
	}

	t0 := time.Now()
	stats, err := server.RefreshEmbeddings(ctx, shardDir, cfg)
	if err != nil {
		return err
	}
	switch {
	case stats.UpToDate:
		fmt.Fprintf(os.Stderr, "moedex-serve: embedding sidecar already current — %d dense chunks, nothing to embed (%s)\n",
			stats.TotalChunks, time.Since(t0).Round(time.Millisecond))
	case stats.Migrated:
		fmt.Fprintf(os.Stderr, "moedex-serve: embedding sidecar re-keyed for incremental refresh — %d dense chunks, no re-embed (%s)\n",
			stats.TotalChunks, time.Since(t0).Round(time.Millisecond))
	default:
		fmt.Fprintf(os.Stderr, "moedex-serve: embedding sidecar rebuilt — %d dense chunks: %d reused, %d new (%d distinct texts embedded) in %s; SIGHUP the daemon to hot-swap\n",
			stats.TotalChunks, stats.Reused, stats.TotalChunks-stats.Reused, stats.Embedded, time.Since(t0).Round(time.Millisecond))
	}
	return nil
}

func runMCP(shardDir string, topK int, embedKind, onnxRuntime string) error {
	ctx := context.Background()
	rc, cfg, err := openRankCorpus(ctx, shardDir, topK, embedKind, onnxRuntime)
	if err != nil {
		return err
	}

	// Serve through a hot-swappable holder so a SIGHUP can rebuild the ranked
	// corpus (reusing the embedder; the persisted embedding sidecar makes warm
	// reloads cheap, and a refreshed shard set re-embeds) without dropping a
	// request. A failed reload keeps the current ranker.
	holder := newRankHolder(rc)
	graphTools, err := server.OpenGraphTools(shardDir)
	if err != nil {
		_ = rc.Close()
		return err
	}
	defer graphTools.Close()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			t0 := time.Now()
			fmt.Fprintln(os.Stderr, "moedex-serve: SIGHUP — rebuilding ranked corpus")
			nrc, _, rankErr := openRankOrDegrade(ctx, shardDir, cfg)
			if rankErr != nil {
				fmt.Fprintf(os.Stderr, "moedex-serve: reload failed (%v); keeping current ranker\n", rankErr)
			}

			// Attempted regardless of the rank-corpus outcome above: rankHolder
			// and GraphToolset are independently refcounted and hot-swappable, so
			// a rank-corpus rebuild failure must not skip an unrelated,
			// otherwise-successful graph-sidecar refresh (F-21).
			if openErr, closeErr := graphTools.Reload(shardDir); openErr != nil {
				fmt.Fprintf(os.Stderr, "moedex-serve: graph reload failed (%v); keeping current graph\n", openErr)
			} else if closeErr != nil {
				fmt.Fprintf(os.Stderr, "moedex-serve: graph reload succeeded but releasing the previous generation failed (%v)\n", closeErr)
			}

			if rankErr != nil {
				continue
			}
			old := holder.swap(nrc)
			go old.retire()
			fmt.Fprintf(os.Stderr, "moedex-serve: reloaded ranker — %d blobs, %d symbol blobs, %d dense chunks (%s) in %s\n",
				nrc.NumBlobs(), nrc.NumSymbolBlobs(), nrc.DenseChunks(), denseSource(nrc), time.Since(t0).Round(time.Millisecond))
		}
	}()

	navtools, navClose := navTools()
	defer navClose()
	tools := append(navtools, graphTools.Tools()...)
	srv := mcp.NewServer(holder,
		mcp.WithTools(tools...),
		mcp.WithCorpusRoot(rc.CorpusRoot()),
		// Graph-fused search: every search_context block carries its graph
		// neighborhood, so an agent never needs a second tool call to learn what
		// calls a hit or what it depends on. graph_depth=0 opts out per call.
		mcp.WithGraphAnnotator(graphTools),
	)
	fmt.Fprintln(os.Stderr, "moedex-serve: MCP ready on stdio (SIGHUP to reload)")
	return srv.Serve(ctx, os.Stdin, os.Stdout)
}

// mcpHTTPConfig carries the resolved -mcp-http settings into runMCPHTTP.
type mcpHTTPConfig struct {
	addr           string
	shardDir       string
	token          string
	tlsCert        string
	tlsKey         string
	requestTimeout time.Duration
	topK           int
	embedKind      string
	onnxRuntime    string
	maxConcurrency int
}

// runMCPHTTP is the warm SHARED agent daemon: it builds the ranked corpus once and
// serves the MCP search_context tool over the Streamable HTTP transport at /mcp,
// so every agent session connects in milliseconds instead of spawning its own
// stdio process and re-paying the ~40s cold load (the daemon pays it once per
// process lifetime). The same refcounted hot-swap as the retrieval daemon keeps
// it serving the old generation while a SIGHUP rebuilds the new one, and the same
// hardening chain (recover/log/timeout/auth — see middleware.go) wraps the mux,
// and /mcp is additionally wrapped in withConcurrencyLimit (cfg.maxConcurrency)
// the same way /search is on the retrieval daemon, since a burst of agent
// sessions is exactly the CPU-bound-scan concern that guard exists for.
// /mcp requires the bearer token when one is configured; /healthz and /metrics
// stay open.
func runMCPHTTP(cfg mcpHTTPConfig) error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	// TLS is all-or-nothing, mirroring runHTTP and the flag-validation in main().
	tls := cfg.tlsCert != "" && cfg.tlsKey != ""
	if (cfg.tlsCert != "") != (cfg.tlsKey != "") {
		fmt.Fprintln(os.Stderr, "moedex-serve: -tls-cert and -tls-key must be set together")
		os.Exit(2)
	}

	ctx := context.Background()
	rc, rankCfg, err := openRankCorpus(ctx, cfg.shardDir, cfg.topK, cfg.embedKind, cfg.onnxRuntime)
	if err != nil {
		return err
	}
	holder := newRankHolder(rc)
	graphTools, err := server.OpenGraphTools(cfg.shardDir)
	if err != nil {
		_ = rc.Close()
		return err
	}
	defer graphTools.Close()
	m := newMetrics()

	effAddr := resolveAddr(cfg.addr, cfg.token)
	slog.Info("boot", "mode", "mcp-http", "blobs", rc.NumBlobs(), "symbol_blobs", rc.NumSymbolBlobs(),
		"dense_chunks", rc.DenseChunks(), "requested_addr", cfg.addr, "effective_addr", effAddr, "tls", tls)
	if cfg.token == "" {
		if isLoopback(effAddr) {
			slog.Warn("no auth token configured; /mcp is open (loopback bind)")
		} else {
			slog.Warn("no auth token AND non-loopback bind; /mcp is open to the network", "effective_addr", effAddr)
		}
	}

	navtools, navClose := navTools()
	defer navClose()
	if len(navtools) > 0 {
		slog.Info("lsp navigation tools enabled", "count", len(navtools))
	}
	tools := append(navtools, graphTools.Tools()...)
	mcpSrv := mcp.NewServer(holder,
		mcp.WithTools(tools...),
		mcp.WithCorpusRoot(rc.CorpusRoot()),
		mcp.WithGraphAnnotator(graphTools),
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/metrics", rankMetricsHandler(holder, m))
	// /mcp is wrapped in withConcurrencyLimit exactly as /search is on the
	// retrieval daemon: mcp.Server.HTTPHandler does not bound concurrency
	// itself (see its doc comment), and each search_context call is a
	// CPU-bound ranking + context-assembly pass, so unbounded fan-in from many
	// agent sessions (or one large JSON-RPC batch) is the same
	// resource-exhaustion vector /search already guards against.
	mux.Handle("/mcp", withConcurrencyLimit(mcpSrv.HTTPHandler(), cfg.maxConcurrency, m.mcpRejected, "too many concurrent mcp requests"))

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
	slog.Info("listening", "addr", effAddr, "endpoint", "/mcp", "tls", tls, "auth", cfg.token != "")

	// SIGHUP -> rebuild the ranked corpus and hot-swap it under live traffic; the
	// daemon keeps answering on the old generation during the ~40s rebuild, then
	// swaps atomically (see reload.go). A failed reload keeps the current ranker.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			start := time.Now()
			slog.Info("reload requested (SIGHUP)")
			nrc, _, rankErr := openRankOrDegrade(ctx, cfg.shardDir, rankCfg)
			if rankErr != nil {
				m.incReload("fail")
				slog.Error("rank corpus reload failed; keeping current ranker", "err", rankErr.Error())
			}

			// Attempted regardless of the rank-corpus outcome above: rankHolder
			// and GraphToolset are independently refcounted and hot-swappable, so
			// a rank-corpus rebuild failure must not skip an unrelated,
			// otherwise-successful graph-sidecar refresh (F-21).
			if openErr, closeErr := graphTools.Reload(cfg.shardDir); openErr != nil {
				slog.Error("graph reload failed; keeping current graph", "err", openErr.Error())
			} else if closeErr != nil {
				slog.Error("graph reload succeeded but releasing the previous generation failed", "err", closeErr.Error())
			}

			if rankErr != nil {
				continue
			}
			old := holder.swap(nrc)
			go old.retire()
			m.incReload("ok")
			slog.Info("reloaded", "blobs", nrc.NumBlobs(), "symbol_blobs", nrc.NumSymbolBlobs(),
				"dense_chunks", nrc.DenseChunks(), "elapsed_ms", time.Since(start).Milliseconds())
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case <-stop:
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		slog.Info("shutting down")
		return srv.Shutdown(shutCtx)
	}
}

// configureDenseArm picks the dense embedder per `kind` and wires it (plus the
// persisted embedding cache path) into cfg. Returns dense=false with no error
// when the dense arm is intentionally off; returns an error only when a
// requested embedder could not be constructed.
func configureDenseArm(cfg *server.RankConfig, shardDir, kind, onnxRuntime string) (bool, error) {
	url := os.Getenv("MOEDEX_EMBED_URL")
	onnxRuntime = embed.ResolveONNXRuntimePath(onnxRuntime)
	if kind == "auto" {
		switch {
		case embed.ONNXCompiled && onnxRuntime != "":
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
	addr                 string
	shardDir             string
	token                string
	tlsCert              string
	tlsKey               string
	requestTimeout       time.Duration
	bootElapsed          time.Duration
	shards               int
	blobs                int
	searchMaxConcurrency int
}

// newHTTPMux builds the retrieval daemon's route table: /healthz and /metrics
// stay open (see openPaths), /stats and /search read the live corpus through
// holder. /search is wrapped in withConcurrencyLimit since it alone runs a
// full cross-shard scan that can pin a core for up to the request timeout —
// the other routes are cheap and unbounded concurrency there is not a risk.
// Shared by runHTTP and the test helper newTestChain so both exercise the
// identical composition.
func newHTTPMux(holder *corpusHolder, m *metrics, searchMaxConcurrency int) *http.ServeMux {
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
	mux.Handle("/search", withConcurrencyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := holder.acquire()
		defer snap.release()
		handleSearch(snap.c, w, r)
	}), searchMaxConcurrency, m.searchRejected, "too many concurrent searches"))
	return mux
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
	mux := newHTTPMux(holder, m, cfg.searchMaxConcurrency)

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
