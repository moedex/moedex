// Command moedex-mcp serves moedex as an agent tool over the Model Context
// Protocol (stdio transport). It ingests a repo, builds the trigram index and
// the BM25 token index, optionally lights up the dense arm against a local
// embedding server, then serves the search_context tool on stdin/stdout.
//
// Usage:
//
//	moedex-mcp -repo DIR
//
// Dense arm (optional): set MOEDEX_EMBED_URL (e.g. http://localhost:11434/v1)
// and MOEDEX_EMBED_MODEL to enable embedding-based retrieval. Without them the
// server runs pure-lexical with zero external dependencies.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"moedex/internal/embed"
	"moedex/internal/index"
	"moedex/internal/ingest"
	"moedex/internal/mcp"
	"moedex/internal/rank"
	"moedex/internal/symbol"
	"moedex/internal/tokenindex"
	"moedex/internal/version"
)

func main() {
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

	srv := mcp.NewServer(searcher)

	fmt.Fprintln(os.Stderr, "moedex-mcp ready on stdio")
	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}
