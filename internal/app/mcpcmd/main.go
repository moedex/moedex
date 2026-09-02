// Command moedex-mcp serves moedex as an agent tool over the Model Context
// Protocol (stdio transport). It ingests a repo, builds the trigram index and
// the BM25 token index, optionally lights up the dense arm against a local
// embedding server, stages a cached graph/symbol sidecar build (see
// graphcache.go) to wire in the same graph tools the warm daemon exposes, then
// serves stdin/stdout.
//
// Usage:
//
//	moedex-mcp -repo DIR
//
// Dense arm (optional): set MOEDEX_EMBED_URL (e.g. http://localhost:11434/v1)
// and MOEDEX_EMBED_MODEL to enable embedding-based retrieval. Without them the
// server runs pure-lexical with zero external dependencies.
//
// Set MOEDEX_MCP_SKIP_GRAPH=1 to skip the graph build and serve search_context
// only — useful for a very large ad-hoc repo where the first-time graph build
// cost isn't worth it.
package mcpcmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"moedex/internal/app/navtools"
	"moedex/internal/embed"
	"moedex/internal/index"
	"moedex/internal/ingest"
	"moedex/internal/mcp"
	"moedex/internal/rank"
	"moedex/internal/symbol"
	"moedex/internal/tokenindex"
	"moedex/internal/version"
)

func Main() {
	repo := flag.String("repo", ".", "path to a git repo to index")
	linesPerChunk := flag.Int("chunk-lines", 40, "lines per embedding chunk (dense arm)")
	overlap := flag.Int("chunk-overlap", 10, "overlapping lines between chunks (dense arm)")
	showVersion := flag.Bool("version", false, "print build identity and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.Line("moedex-mcp", embed.ONNXCompiled))
		return
	}

	files, err := ingest.Repo(*repo, *repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ingest:", err)
		os.Exit(1)
	}
	ix := index.New()
	for _, f := range files {
		ix.AddFile(f.Repo, f.RelPath, f.AbsPath, f.SHA, f.Content)
	}
	ti := tokenindex.Build(ix)
	fmt.Fprintf(os.Stderr, "indexed %d files into %d blobs, %d docs in token index\n",
		len(files), ix.NumBlobs(), ti.NumDocs())

	ctx := context.Background()

	// Optional dense arm: only if a local embedding server is configured.
	var store *embed.Store
	var emb embed.Embedder
	if url := os.Getenv("MOEDEX_EMBED_URL"); url != "" {
		model := os.Getenv("MOEDEX_EMBED_MODEL")
		emb = embed.NewHTTPEmbedder(url, model)
		store, err = embed.BuildStore(ctx, ix, emb, *linesPerChunk, *overlap)
		if err != nil {
			fmt.Fprintln(os.Stderr, "embed (dense arm disabled):", err)
			store, emb = nil, nil
		} else {
			fmt.Fprintf(os.Stderr, "dense arm: %d chunks embedded (dim %d) via %s\n",
				store.Len(), store.Dim(), url)
		}
	} else {
		fmt.Fprintln(os.Stderr, "dense arm disabled (set MOEDEX_EMBED_URL to enable); running pure-lexical")
	}

	ranker := rank.New(ix, ti, store, emb, rank.Config{})
	searcher := mcp.NewIndexSearcher(ix, ranker, 20)

	// Syntactic symbol layer: scope context blocks to real function/type
	// boundaries and power the symbol-name ranking arm. BuildMulti dispatches a
	// per-language extractor (Go/C#/TypeScript) by file extension; blobs with no
	// recognized extractor are skipped, so contextwin falls back to its
	// brace/indent heuristic for them.
	symIdx := symbol.BuildMulti(ix)
	searcher.SetEnclosingBytes(symIdx.EnclosingBytesFunc())
	ranker.SetSymbols(symIdx) // symbol-name match as a third RRF ranking arm
	fmt.Fprintf(os.Stderr, "symbol layer: %d blobs carry symbols\n", symIdx.NumBlobs())

	// Kind: Boolean in config.DefaultRegistry, so cli.go's PersistentPreRunE has
	// already rejected an unparseable value before Main ever runs; ParseBool
	// here just recovers the validated value (default false: unset -> "").
	skipGraph, _ := strconv.ParseBool(os.Getenv("MOEDEX_MCP_SKIP_GRAPH"))

	opts := []mcp.Option{}
	if skipGraph {
		fmt.Fprintln(os.Stderr, "graph tools disabled (MOEDEX_MCP_SKIP_GRAPH set); search_context only")
	} else {
		start := time.Now()
		dir, ready := adhocGraphDir(files)
		graphTools, err := buildAdhocGraphTools(dir, ix, ready)
		if err != nil {
			fmt.Fprintln(os.Stderr, "graph tools (disabled):", err)
		} else {
			defer graphTools.Close()
			opts = append(opts, mcp.WithTools(graphTools.Tools()...), mcp.WithGraphAnnotator(graphTools))
			cached := "built"
			if ready {
				cached = "cached"
			}
			fmt.Fprintf(os.Stderr, "graph tools: %s at %s in %s\n", cached, dir, time.Since(start).Round(time.Millisecond))
		}
	}

	// Inert without -tags lsp (nav_stub.go returns no tools, no-op close).
	navHandlers, navClose := navtools.NavTools()
	defer navClose()
	if len(navHandlers) > 0 {
		opts = append(opts, mcp.WithTools(navHandlers...))
		fmt.Fprintf(os.Stderr, "lsp navigation tools enabled: %d\n", len(navHandlers))
	}
	if cta := lspRecommendation(files, navtools.LSPBuild); cta != "" {
		opts = append(opts, mcp.WithExtraInstructions(cta))
	}

	srv := mcp.NewServer(searcher, opts...)

	fmt.Fprintln(os.Stderr, "moedex-mcp ready on stdio")
	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}
