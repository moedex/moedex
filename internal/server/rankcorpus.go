package server

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"moedex/internal/contextwin"
	"moedex/internal/diskstore"
	"moedex/internal/embed"
	"moedex/internal/index"
	"moedex/internal/mcp"
	"moedex/internal/rank"
	"moedex/internal/symbol"
	"moedex/internal/tokenindex"
)

// RankConfig tunes the corpus ranker. The zero value is valid (pure-lexical +
// symbol arm, topK 20).
type RankConfig struct {
	TopK  int            // ranked results drawn into a context window (default 20)
	Rank  rank.Config    // BM25/RRF/symbol-gate tuning (zero = sensible defaults)
	Store *embed.Store   // optional dense arm; nil = pure-lexical
	Emb   embed.Embedder // optional dense arm; nil = pure-lexical
}

// RankCorpus is the ranked, agent-facing surface over a set of prebuilt shards.
// It loads only blob CONTENT from every shard (diskstore.LoadBlobs — no
// positional postings, so no RAM wall), concatenates it into one index with
// global blob IDs, and builds the corpus-wide ranking layer: a BM25 token index
// and a syntactic symbol index, fused by rank.Ranker. Candidate generation runs
// off the token index (Ranker.UseTokenCandidates), which is why the content-only
// index needs no trigram postings.
//
// It implements mcp.ContextSearcher, so the MCP search_context tool serves the
// whole corpus, not a single repo.
type RankCorpus struct {
	ix       *index.Index
	ti       *tokenindex.TokenIndex
	syms     *symbol.Index
	searcher *mcp.IndexSearcher
}

// OpenRank builds the corpus ranker from every "*.idx" shard under dir.
func OpenRank(dir string, cfg RankConfig) (*RankCorpus, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.idx"))
	if err != nil {
		return nil, fmt.Errorf("server: glob shards: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("server: no *.idx shards under %s", dir)
	}
	sort.Strings(paths)

	// Concatenate blob content across shards. index.Restore assigns each blob the
	// ID equal to its position in this slice, so IDs are global and dense.
	var blobs []index.BlobData
	for _, p := range paths {
		bs, err := diskstore.LoadBlobs(p)
		if err != nil {
			return nil, fmt.Errorf("server: load blobs %s: %w", p, err)
		}
		blobs = append(blobs, bs...)
	}
	// nil postings: the corpus ranker never does trigram search (candidates come
	// from the token index), so the positional postings are intentionally absent.
	ix := index.Restore(blobs, nil)

	ti := tokenindex.Build(ix)
	ranker := rank.New(ix, ti, cfg.Store, cfg.Emb, cfg.Rank)
	ranker.UseTokenCandidates(true)

	syms := symbol.BuildMulti(ix)
	ranker.SetSymbols(syms)

	searcher := mcp.NewIndexSearcher(ix, ranker, cfg.TopK)
	searcher.SetEnclosingBytes(syms.EnclosingBytesFunc())

	return &RankCorpus{ix: ix, ti: ti, syms: syms, searcher: searcher}, nil
}

// SearchContext implements mcp.ContextSearcher by delegating to the underlying
// ranked-context searcher.
func (rc *RankCorpus) SearchContext(ctx context.Context, query string, tokenBudget, topK int) (contextwin.ContextWindow, error) {
	return rc.searcher.SearchContext(ctx, query, tokenBudget, topK)
}

// NumBlobs reports total blobs in the corpus.
func (rc *RankCorpus) NumBlobs() int { return rc.ix.NumBlobs() }

// NumDocs reports how many documents the BM25 index covers.
func (rc *RankCorpus) NumDocs() int { return rc.ti.NumDocs() }

// NumSymbolBlobs reports how many blobs carry extracted symbols.
func (rc *RankCorpus) NumSymbolBlobs() int { return rc.syms.NumBlobs() }
