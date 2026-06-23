package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
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
	TopK int         // ranked results drawn into a context window (default 20)
	Rank rank.Config // BM25/RRF/symbol-gate tuning (zero = sensible defaults)

	// Dense arm (optional). When Emb is set, OpenRank lights up the dense arm;
	// Emb is always needed for query embedding at search time. When Emb is nil,
	// ranking is pure-lexical + symbol with no external dependency.
	//
	// The corpus chunk embeddings can come from three places, in order:
	//   - Store: a prebuilt store, used as-is (skips all building/loading).
	//   - StorePath: a persisted sidecar. If it exists and its .meta matches the
	//     current corpus (shard fingerprint + model + blob count), it is loaded
	//     instantly; otherwise OpenRank builds the store and saves it there for
	//     next boot. This is what makes the dense arm practical at full-corpus
	//     scale — embed once, reuse across boots.
	//   - Otherwise OpenRank embeds the whole corpus in memory (the boot cost).
	Emb           embed.Embedder
	Store         *embed.Store
	StorePath     string // persisted corpus-embedding sidecar (load-or-build-and-save)
	EmbedModel    string // recorded in the sidecar meta; a model change invalidates it
	LinesPerChunk int    // dense chunk window (default 40)
	Overlap       int    // dense chunk overlap (default 10)
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
	ix          *index.Index
	ti          *tokenindex.TokenIndex
	syms        *symbol.Index
	store       *embed.Store // nil when the dense arm is disabled
	denseCached bool         // true when the store was loaded from a persisted sidecar
	searcher    *mcp.IndexSearcher
}

// OpenRank builds the corpus ranker from every "*.idx" shard under dir. When
// cfg.Emb is set (and no prebuilt cfg.Store is supplied), it embeds the whole
// corpus under ctx — the boot-time dense-arm cost — so callers should pass a
// cancellable context.
func OpenRank(ctx context.Context, dir string, cfg RankConfig) (*RankCorpus, error) {
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

	// Dense arm: a prebuilt store wins; else load a matching persisted sidecar;
	// else embed the corpus now (and persist if StorePath is set).
	store := cfg.Store
	cached := false
	if store == nil && cfg.Emb != nil {
		if cfg.StorePath != "" {
			if st, ok := loadPersistedStore(cfg.StorePath, paths, ix.NumBlobs(), cfg.EmbedModel); ok {
				store, cached = st, true
			}
		}
		if store == nil {
			store, err = embed.BuildStore(ctx, ix, cfg.Emb, chunkLines(cfg), chunkOverlap(cfg))
			if err != nil {
				return nil, fmt.Errorf("server: build embeddings: %w", err)
			}
			if cfg.StorePath != "" {
				if err := savePersistedStore(store, cfg.StorePath, paths, ix.NumBlobs(), cfg.EmbedModel); err != nil {
					// Persistence is best-effort: a failed cache write must not
					// take down a working ranker.
					fmt.Fprintf(os.Stderr, "server: persist embeddings (continuing): %v\n", err)
				}
			}
		}
	}

	ranker := rank.New(ix, ti, store, cfg.Emb, cfg.Rank)
	ranker.UseTokenCandidates(true)

	syms := symbol.BuildMulti(ix)
	ranker.SetSymbols(syms)

	searcher := mcp.NewIndexSearcher(ix, ranker, cfg.TopK)
	searcher.SetEnclosingBytes(syms.EnclosingBytesFunc())

	return &RankCorpus{ix: ix, ti: ti, syms: syms, store: store, denseCached: cached, searcher: searcher}, nil
}

// storeMeta validates a persisted corpus-embedding sidecar against the current
// corpus. A mismatch on any field means the store was built from a different
// shard set, model, or corpus size and must not be reused.
type storeMeta struct {
	Fingerprint string `json:"fingerprint"` // shard set (sorted name+size)
	Model       string `json:"model"`       // embedding model
	NumBlobs    int    `json:"num_blobs"`   // unified-index blob count (global IDs)
}

// corpusFingerprint hashes the shard set (sorted basename + byte size) so any
// add/remove/regrow of shards changes it — invalidating a stale embedding cache.
func corpusFingerprint(shardPaths []string) string {
	h := sha256.New()
	for _, p := range shardPaths {
		size := int64(-1)
		if fi, err := os.Stat(p); err == nil {
			size = fi.Size()
		}
		fmt.Fprintf(h, "%s:%d\n", filepath.Base(p), size)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// loadPersistedStore loads storePath iff its sibling .meta matches the current
// corpus. Any mismatch or read error returns ok=false so OpenRank rebuilds.
func loadPersistedStore(storePath string, shardPaths []string, numBlobs int, model string) (*embed.Store, bool) {
	raw, err := os.ReadFile(storePath + ".meta")
	if err != nil {
		return nil, false
	}
	var m storeMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false
	}
	if m.Fingerprint != corpusFingerprint(shardPaths) || m.Model != model || m.NumBlobs != numBlobs {
		return nil, false
	}
	st, err := embed.LoadStore(storePath)
	if err != nil {
		return nil, false
	}
	return st, true
}

// savePersistedStore writes the store and its validating meta sidecar.
func savePersistedStore(st *embed.Store, storePath string, shardPaths []string, numBlobs int, model string) error {
	if err := st.Save(storePath); err != nil {
		return err
	}
	meta, err := json.Marshal(storeMeta{
		Fingerprint: corpusFingerprint(shardPaths),
		Model:       model,
		NumBlobs:    numBlobs,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(storePath+".meta", meta, 0o644)
}

func chunkLines(cfg RankConfig) int {
	if cfg.LinesPerChunk <= 0 {
		return 40
	}
	return cfg.LinesPerChunk
}

func chunkOverlap(cfg RankConfig) int {
	if cfg.Overlap <= 0 {
		return 10
	}
	return cfg.Overlap
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

// DenseChunks reports how many embedded chunks back the dense arm, or 0 when the
// dense arm is disabled.
func (rc *RankCorpus) DenseChunks() int {
	if rc.store == nil {
		return 0
	}
	return rc.store.Len()
}

// DenseFromCache reports whether the dense arm's embeddings were loaded from a
// persisted sidecar (true) rather than embedded at boot (false).
func (rc *RankCorpus) DenseFromCache() bool { return rc.denseCached }
