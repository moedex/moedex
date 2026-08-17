package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"moedex/internal/contextwin"
	"moedex/internal/diskstore"
	"moedex/internal/embed"
	"moedex/internal/index"
	"moedex/internal/mcp"
	"moedex/internal/parity"
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
	//     current corpus (shard fingerprint + model + blob count + chunk geometry),
	//     it is loaded
	//     instantly; otherwise OpenRank builds the store and saves it there for
	//     next boot. This is what makes the dense arm practical at full-corpus
	//     scale — embed once, reuse across boots.
	//   - Otherwise OpenRank embeds the whole corpus in memory (the boot cost).
	Emb           embed.Embedder
	Store         *embed.Store
	StorePath     string // persisted corpus-embedding sidecar (load-or-build-and-save)
	EmbedModel    string // recorded in the sidecar meta; a model change invalidates it
	LinesPerChunk int    // dense chunk window (default 40); recorded in the meta, a change invalidates it
	Overlap       int    // dense chunk overlap (default 10); recorded in the meta, a change invalidates it

	// Token/symbol sidecars (load-or-build-and-save, mirroring StorePath). Both
	// default to a path under dir when empty, so the feature is on by default; a
	// matching sidecar skips the rebuild on reload, otherwise OpenRank builds and
	// re-persists. Persistence is best-effort and never fails a boot.
	TokenPath  string // persisted BM25 token-index sidecar (default dir/corpus-tokens.tki)
	SymbolPath string // persisted syntactic symbol-index sidecar (default dir/corpus-symbols.sym)
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
	store       *embed.Store            // nil when the dense arm is disabled
	content     *diskstore.ContentStore // non-nil only for a deduped (MOEDEX05) dir
	denseCached bool                    // true when the store was loaded from a persisted sidecar
	tokenCached bool                    // true when the token index was loaded from a persisted sidecar
	symsCached  bool                    // true when the symbol index was loaded from a persisted sidecar
	searcher    *mcp.IndexSearcher
	// corpusRoot is the directory the corpus was built under, recovered from the
	// shard dir's manifest.json (parity.Manifest.Root). Empty if the manifest is
	// absent/unreadable. It lets the MCP layer emit each hit's full
	// path_with_namespace (the mirror is laid out as <root>/<path_with_namespace>).
	corpusRoot string
}

// CorpusRoot returns the directory the corpus was built under (from the shard
// dir's manifest), or "" if it could not be determined. The MCP server uses it
// (via mcp.WithCorpusRoot) to recover each hit's full path_with_namespace.
func (rc *RankCorpus) CorpusRoot() string { return rc.corpusRoot }

// Close releases the shared content store mmap (deduped dir) backing the corpus'
// blob content. It is required for a deduped dir, where the unified index's blob
// content aliases the shared store's mapping; a legacy dir copies content onto the
// heap, so Close is a harmless no-op there. Close is idempotent and safe to call
// even on a RankCorpus over a legacy dir.
func (rc *RankCorpus) Close() error {
	if rc.content != nil {
		err := rc.content.Close()
		rc.content = nil
		return err
	}
	return nil
}

// OpenRank builds the corpus ranker from every "*.idx" shard under dir. When
// cfg.Emb is set (and no prebuilt cfg.Store is supplied), it embeds the whole
// corpus under ctx — the boot-time dense-arm cost — so callers should pass a
// cancellable context.
func OpenRank(ctx context.Context, dir string, cfg RankConfig) (*RankCorpus, error) {
	ix, paths, cs, err := loadUnified(dir)
	if err != nil {
		return nil, err
	}
	// For a deduped dir the unified index's blob content aliases cs's mmap, so cs
	// must stay open for the RankCorpus' lifetime; on any error before we hand
	// ownership to the returned RankCorpus, close it here to avoid a leaked mapping.
	ok := false
	defer func() {
		if !ok && cs != nil {
			cs.Close()
		}
	}()

	// Token index: load a matching persisted sidecar, else build + best-effort
	// persist. Default the path under dir so the cache is on by default.
	tokenPath := cfg.TokenPath
	if tokenPath == "" {
		tokenPath = defaultTokenPath(dir)
	}
	ti, tokenCached := loadPersistedTokens(tokenPath, paths, ix.NumBlobs())
	if ti == nil {
		ti = tokenindex.Build(ix)
		if err := savePersistedTokens(ti, tokenPath, paths, ix.NumBlobs()); err != nil {
			// Best-effort: a failed cache write must not take down a working ranker.
			fmt.Fprintf(os.Stderr, "server: persist token index (continuing): %v\n", err)
		}
	}
	fmt.Fprintf(os.Stderr, "server: token index %s\n", cacheTag(tokenCached))

	// Dense arm: a prebuilt store wins; else load a matching persisted sidecar;
	// else embed the corpus now (and persist if StorePath is set).
	store := cfg.Store
	cached := false
	if store == nil && cfg.Emb != nil {
		if cfg.StorePath != "" {
			if st, ok := loadPersistedStore(cfg.StorePath, paths, ix.NumBlobs(), cfg.EmbedModel, chunkSpecOf(cfg)); ok {
				store, cached = st, true
			}
		}
		if store == nil {
			store, err = embed.BuildStore(ctx, ix, cfg.Emb, chunkLines(cfg), chunkOverlap(cfg))
			if err != nil {
				return nil, fmt.Errorf("server: build embeddings: %w", err)
			}
			if cfg.StorePath != "" {
				if err := savePersistedStore(store, cfg.StorePath, paths, ix.NumBlobs(), cfg.EmbedModel, chunkSpecOf(cfg)); err != nil {
					// Persistence is best-effort: a failed cache write must not
					// take down a working ranker.
					fmt.Fprintf(os.Stderr, "server: persist embeddings (continuing): %v\n", err)
				}
			}
		}
	}

	ranker := rank.New(ix, ti, store, cfg.Emb, cfg.Rank)
	ranker.UseTokenCandidates(true)

	// Symbol index: load a matching persisted sidecar, else build + best-effort
	// persist. Default the path under dir so the cache is on by default.
	symbolPath := cfg.SymbolPath
	if symbolPath == "" {
		symbolPath = defaultSymbolPath(dir)
	}
	syms, symsCached := loadPersistedSymbols(symbolPath, paths, ix.NumBlobs())
	if syms == nil {
		syms = symbol.BuildMulti(ix)
		if err := savePersistedSymbols(syms, symbolPath, paths, ix.NumBlobs()); err != nil {
			fmt.Fprintf(os.Stderr, "server: persist symbol index (continuing): %v\n", err)
		}
	}
	fmt.Fprintf(os.Stderr, "server: symbol index %s\n", cacheTag(symsCached))
	ranker.SetSymbols(syms)

	searcher := mcp.NewIndexSearcher(ix, ranker, cfg.TopK)
	searcher.SetEnclosingBytes(syms.EnclosingBytesFunc())

	ok = true // hand cs ownership to the RankCorpus; the deferred close is now a no-op
	return &RankCorpus{ix: ix, ti: ti, syms: syms, store: store, content: cs, denseCached: cached, tokenCached: tokenCached, symsCached: symsCached, searcher: searcher, corpusRoot: loadCorpusRoot(dir)}, nil
}

// loadCorpusRoot best-effort reads the corpus build root from the shard dir's
// parity manifest (written by every build/export into the served dir). A missing
// or unparseable manifest yields "" — never an error: the corpus serves fine
// without it, the path_with_namespace field is simply omitted.
func loadCorpusRoot(shardDir string) string {
	m, err := parity.LoadManifest(filepath.Join(shardDir, parity.ManifestName))
	if err != nil {
		return ""
	}
	return m.Root
}

// loadUnified globs the "*.idx" shards under dir (sorted), concatenates their
// blob content, and restores a single content-only index. index.Restore assigns
// each blob the ID equal to its position in the sorted slice, so IDs are global
// and dense — and identical across OpenRank and BuildSidecars (which is why both
// share this helper). Returns the sorted shard paths so callers can fingerprint
// the same set. The postings are nil: the corpus ranker never does trigram
// search (candidates come from the token index).
//
// For a deduped dir (MOEDEX05 + a shared content store) it opens the shared store
// ONCE and resolves every shard's blob content from it as zero-copy mmap sub-
// slices; the returned *ContentStore must stay open for as long as the returned
// index's blob content is used (its Content aliases the store mmap). For a legacy
// dir the store is nil and content is heap-copied as before. The CONCATENATION
// ORDER and resulting global blob IDs are identical to a legacy dir built from the
// same repos, because the deduped export packs repos into shards identically — so
// the corpus fingerprint and sidecar reuse semantics are unchanged.
func loadUnified(dir string) (*index.Index, []string, *diskstore.ContentStore, error) {
	paths, err := globShards(dir)
	if err != nil {
		return nil, nil, nil, err
	}

	cs, err := openSharedContent(dir, paths)
	if err != nil {
		return nil, nil, nil, err
	}

	var blobs []index.BlobData
	for _, p := range paths {
		var bs []index.BlobData
		if cs != nil && diskstore.IsDeduped(p) {
			bs, err = diskstore.LoadBlobsDeduped(p, cs)
		} else {
			bs, err = diskstore.LoadBlobs(p)
		}
		if err != nil {
			if cs != nil {
				cs.Close()
			}
			return nil, nil, nil, fmt.Errorf("server: load blobs %s: %w", p, err)
		}
		blobs = append(blobs, bs...)
	}
	return index.Restore(blobs, nil), paths, cs, nil
}

// cacheTag renders the load-vs-build outcome for a one-line stderr trace.
func cacheTag(cached bool) string {
	if cached {
		return "loaded from cache"
	}
	return "built"
}

// defaultTokenPath / defaultSymbolPath are the on-by-default sidecar locations
// next to the shards, so OpenRank persists+reuses with zero caller wiring.
func defaultTokenPath(dir string) string  { return filepath.Join(dir, "corpus-tokens.tki") }
func defaultSymbolPath(dir string) string { return filepath.Join(dir, "corpus-symbols.sym") }

// Default dense-chunk geometry, applied by chunkLines/chunkOverlap when
// RankConfig leaves them unset. They are named because storeMeta also needs them:
// see chunkSpecFromMeta.
const (
	defaultChunkLines   = 40
	defaultChunkOverlap = 10
)

// chunkSpec is the RESOLVED dense-chunk geometry a store was built with —
// resolved meaning defaults already applied, so an unset RankConfig and one that
// spells out the defaults compare equal instead of spuriously invalidating a
// cache.
type chunkSpec struct {
	Lines   int
	Overlap int
}

// chunkSpecOf returns cfg's resolved chunk geometry.
func chunkSpecOf(cfg RankConfig) chunkSpec {
	return chunkSpec{Lines: chunkLines(cfg), Overlap: chunkOverlap(cfg)}
}

// storeMeta validates a persisted corpus-embedding sidecar against the current
// corpus. A mismatch on any field means the store was built from a different
// shard set, model, corpus size, or CHUNK GEOMETRY and must not be reused.
//
// Geometry belongs here for the same reason the model does: the vectors are a
// function of the text that was embedded, and the chunk window/overlap decide
// what that text is. Without it, changing LinesPerChunk from 40 to 80 silently
// reuses vectors for 40-line chunks under an index that now chunks by 80 — every
// dense score computed over text that no longer exists in that form.
type storeMeta struct {
	Fingerprint   string `json:"fingerprint"`     // shard set (sorted name+size)
	Model         string `json:"model"`           // embedding model
	NumBlobs      int    `json:"num_blobs"`       // unified-index blob count (global IDs)
	LinesPerChunk int    `json:"lines_per_chunk"` // resolved dense chunk window
	Overlap       int    `json:"overlap"`         // resolved dense chunk overlap
}

// chunkSpecFromMeta reads m's geometry, treating an ABSENT field (0 — a .meta
// written before these fields existed) as the default geometry rather than as a
// mismatch.
//
// That is sound rather than lenient: nothing outside this package sets
// RankConfig.LinesPerChunk/Overlap (no moedex-serve or moedex-index flag exposes
// them), so every store persisted before this field existed was necessarily built
// with the resolved defaults. Backfilling them is therefore what the file would
// have said, and it matters because the two invalidation costs are wildly
// asymmetric: a stale symbol sidecar rebuilds in seconds, whereas invalidating an
// embedding store sends OpenRank through a full corpus re-embed (embed.BuildStore
// has no reuse path — only RefreshEmbeddings does). A geometry change made through
// this package's config still mismatches and still rebuilds, which is the point.
func chunkSpecFromMeta(m storeMeta) chunkSpec {
	spec := chunkSpec{Lines: m.LinesPerChunk, Overlap: m.Overlap}
	if spec.Lines <= 0 {
		spec.Lines = defaultChunkLines
	}
	if spec.Overlap <= 0 {
		spec.Overlap = defaultChunkOverlap
	}
	return spec
}

// corpusFingerprint hashes the shard set (sorted basename + byte size) so any
// add/remove/regrow of shards changes it — invalidating a stale embedding cache.
//
// For a DEDUPED dir the shards are content-less (sha + file refs only); the actual
// indexed bytes live in the shared content store (blobs.dat). So a change to the
// shared content store — same shard files, different content (e.g. a re-export that
// rewrote blobs.dat) — would NOT show up in the shard-file sizes alone, and a
// token/symbol/embedding sidecar built over the OLD content could be silently reused
// over the NEW content (a ranking-freshness bug: retrieval reads the verified live
// content, but BM25/symbol/embedding scores would be computed on stale content).
//
// We therefore fold the content store's CONTENT-TRUE identity — a hash of its
// DIRECTORY SECTION (the list of content-hash keys + offsets + lengths) — into the
// fingerprint when blobs.dat is present. Because each key IS the content hash of its
// blob, the directory uniquely identifies the entire content set: any added/removed/
// changed/reordered blob changes a key (or the record set) and thus the fingerprint,
// EVEN when the file's total size and MOECONT1 header (numBlobs/dirOff) are
// unchanged. This is O(numBlobs) (the directory), not O(total content bytes), so it
// stays cheap on boot. The store is a sibling of the shards, so we derive it from
// their dir; a legacy dir has no blobs.dat and the fingerprint is unchanged.
func corpusFingerprint(shardPaths []string) string {
	h := sha256.New()
	for _, p := range shardPaths {
		size := int64(-1)
		if fi, err := os.Stat(p); err == nil {
			size = fi.Size()
		}
		fmt.Fprintf(h, "%s:%d\n", filepath.Base(p), size)
	}
	if len(shardPaths) > 0 {
		csPath := filepath.Join(filepath.Dir(shardPaths[0]), diskstore.ContentStoreName)
		if fi, err := os.Stat(csPath); err == nil {
			fmt.Fprintf(h, "%s:%d\n", diskstore.ContentStoreName, fi.Size())
			// Content-true identity: a hash of the directory of content-hash keys, so
			// a same-size/same-header re-export with different content still changes
			// the fingerprint. Best-effort — an unreadable store degrades to size only.
			if dig := diskstore.ContentStoreDirDigest(csPath); dig != "" {
				fmt.Fprintf(h, "dir:%s\n", dig)
			}
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// loadPersistedStore loads storePath iff its sibling .meta matches the current
// corpus, embedding model, AND chunk geometry. Any mismatch or read error returns
// ok=false so OpenRank rebuilds.
func loadPersistedStore(storePath string, shardPaths []string, numBlobs int, model string, spec chunkSpec) (*embed.Store, bool) {
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
	if chunkSpecFromMeta(m) != spec {
		return nil, false // vectors are for differently-chunked text
	}
	st, err := embed.LoadStore(storePath)
	if err != nil {
		return nil, false
	}
	return st, true
}

// readStoreMeta reads the validating .meta sidecar next to a persisted embedding
// store, if present. ok=false on any missing/corrupt meta.
func readStoreMeta(storePath string) (storeMeta, bool) {
	raw, err := os.ReadFile(storePath + ".meta")
	if err != nil {
		return storeMeta{}, false
	}
	var m storeMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return storeMeta{}, false
	}
	return m, true
}

// EmbeddingRefreshStats summarizes a RefreshEmbeddings run for logging.
type EmbeddingRefreshStats struct {
	TotalChunks int  // chunks in the resulting store
	Reused      int  // chunks whose vector was carried over unchanged (no embed)
	Embedded    int  // DISTINCT new texts sent to the embedder
	UpToDate    bool // fingerprint already matched a key-bearing store; nothing rebuilt
	Migrated    bool // a legacy (keyless) but current store was re-keyed in place, no re-embed
}

// RefreshEmbeddings builds or INCREMENTALLY refreshes the persisted dense embedding
// sidecar for the shard set under dir, reusing the vectors of every unchanged chunk
// so a corpus that touched a few files re-embeds in minutes instead of re-embedding
// the whole corpus. It is the out-of-band builder the refresh pipeline calls before
// SIGHUP'ing the warm daemon; the daemon's OpenRank then just LOADS the fresh,
// fingerprint-matching sidecar.
//
// Decision (cfg.Emb required; cfg.StorePath defaults under dir):
//   - no usable prior store (missing, unreadable, or a DIFFERENT embedding model):
//     full embed.
//   - prior store matches the current corpus AND already carries content keys:
//     up to date — nothing to do.
//   - prior store matches the current corpus but is a legacy keyless (v1) store:
//     re-key it in place (no embedding) so the NEXT refresh can be incremental.
//   - prior store is from this model but the corpus changed: incremental rebuild,
//     reusing every unchanged chunk's vector and embedding only new text.
//
// The result is byte-identical to a full rebuild for every reused chunk, so ranking
// does not move. Reuse is gated on the embedding model (and vector dim): vectors
// from a different model are never carried over.
func RefreshEmbeddings(ctx context.Context, dir string, cfg RankConfig) (EmbeddingRefreshStats, error) {
	if cfg.Emb == nil {
		return EmbeddingRefreshStats{}, fmt.Errorf("server: refresh embeddings: no embedder configured")
	}
	storePath := cfg.StorePath
	if storePath == "" {
		storePath = filepath.Join(dir, "corpus-embeddings.store")
	}

	ix, paths, cs, err := loadUnified(dir)
	if err != nil {
		return EmbeddingRefreshStats{}, err
	}
	if cs != nil {
		defer cs.Close()
	}
	numBlobs := ix.NumBlobs()
	fp := corpusFingerprint(paths)

	// Load the prior store for reuse, but ONLY if it was built with this same model
	// and dimension — never carry vectors across models. A model/dim mismatch (or no
	// store) leaves prev nil, forcing a clean full embed.
	var prev *embed.Store
	meta, haveMeta := readStoreMeta(storePath)
	modelMatches := haveMeta && meta.Model == cfg.EmbedModel
	if modelMatches {
		if st, err := embed.LoadStore(storePath); err == nil && (st.Dim() == 0 || st.Dim() == cfg.Emb.Dim()) {
			prev = st
		}
	}

	// Corpus unchanged (same shard fingerprint, model, blob count) AND the same chunk
	// geometry, and we have the matching store: either it is already up to date, or it
	// is a legacy keyless store we can re-key for free. Geometry has to gate this fast
	// path too — a store chunked differently is not "up to date" no matter how well the
	// corpus matches, and reporting UpToDate here would skip the rebuild entirely.
	spec := chunkSpecOf(cfg)
	if prev != nil && meta.Fingerprint == fp && meta.NumBlobs == numBlobs && chunkSpecFromMeta(meta) == spec {
		if prev.HasKeys() {
			return EmbeddingRefreshStats{TotalChunks: prev.Len(), Reused: prev.Len(), UpToDate: true}, nil
		}
		if err := prev.FillKeys(ix); err != nil {
			return EmbeddingRefreshStats{}, fmt.Errorf("server: re-key embedding store: %w", err)
		}
		if err := savePersistedStore(prev, storePath, paths, numBlobs, cfg.EmbedModel, spec); err != nil {
			return EmbeddingRefreshStats{}, fmt.Errorf("server: persist re-keyed store: %w", err)
		}
		return EmbeddingRefreshStats{TotalChunks: prev.Len(), Reused: prev.Len(), Migrated: true}, nil
	}

	// Changed (or missing/foreign) store: incremental build, reusing whatever the
	// prior store can offer (nil/keyless -> a clean full embed). Reuse stays correct
	// under a geometry change without any extra guard: a ChunkKey is sha256 of the
	// chunk TEXT, so re-chunked text yields different keys and simply finds no match —
	// except where the text is byte-identical anyway, where reuse is right.
	var reuse map[embed.ChunkKey]embed.Vector
	if prev != nil {
		reuse = prev.KeyVectors()
	}
	store, st, err := embed.BuildStoreIncremental(ctx, ix, cfg.Emb, chunkLines(cfg), chunkOverlap(cfg), reuse)
	if err != nil {
		return EmbeddingRefreshStats{}, fmt.Errorf("server: build embeddings: %w", err)
	}
	if err := savePersistedStore(store, storePath, paths, numBlobs, cfg.EmbedModel, spec); err != nil {
		return EmbeddingRefreshStats{}, fmt.Errorf("server: persist embeddings: %w", err)
	}
	return EmbeddingRefreshStats{TotalChunks: st.Total, Reused: st.Reused, Embedded: st.Embedded}, nil
}

// savePersistedStore writes the store and its validating meta sidecar, recording
// the resolved chunk geometry the vectors were built with.
func savePersistedStore(st *embed.Store, storePath string, shardPaths []string, numBlobs int, model string, spec chunkSpec) error {
	if err := st.Save(storePath); err != nil {
		return err
	}
	meta, err := json.Marshal(storeMeta{
		Fingerprint:   corpusFingerprint(shardPaths),
		Model:         model,
		NumBlobs:      numBlobs,
		LinesPerChunk: spec.Lines,
		Overlap:       spec.Overlap,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(storePath+".meta", meta, 0o644)
}

// sidecarMeta validates a persisted token- or symbol-index sidecar against the
// current corpus. Unlike storeMeta it has no Model field (token/symbol indexes
// depend only on the shard set, not on an embedding model). It reuses
// corpusFingerprint, so any add/remove/regrow of shards invalidates the cache.
// Extractors versions the BUILDER, not the corpus: a sidecar is a cache of
// derived output, so a change in the code that derives it invalidates the cache
// even when the corpus is byte-identical. Only the symbol sidecar has a builder
// worth versioning (the language extractors — see symbol.ExtractorsVersion); the
// token index derives from tokenindex.Tokenize, which is frozen by contract, so
// it records 0 and `omitempty` keeps its .meta byte-identical to before this
// field existed.
//
// Forward-compat caveat: an OLDER binary reading a NEWER .meta ignores unknown
// fields, so it would reuse a sidecar this check would reject. That is inherent
// to adding a field; it only matters if an old and new binary share a shard dir.
type sidecarMeta struct {
	Fingerprint string `json:"fingerprint"` // shard set (sorted name+size)
	NumBlobs    int    `json:"num_blobs"`   // unified-index blob count (global IDs)
	Extractors  int    `json:"extractors,omitempty"`
}

// sidecarMetaMatches reports whether path's sibling .meta matches the current
// corpus AND was produced by the expected builder version (extractors; 0 for a
// sidecar with no extractor dependency). Any missing/corrupt/mismatched meta
// returns false so OpenRank rebuilds.
func sidecarMetaMatches(path string, shardPaths []string, numBlobs, extractors int) bool {
	raw, err := os.ReadFile(path + ".meta")
	if err != nil {
		return false
	}
	var m sidecarMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	if m.Extractors != extractors {
		// Same corpus, different extractor output: the cache is stale even though
		// the fingerprint still matches. A pre-versioning sidecar lands here too
		// (its absent field reads as 0).
		return false
	}
	return m.Fingerprint == corpusFingerprint(shardPaths) && m.NumBlobs == numBlobs
}

// writeSidecarMeta writes the validating .meta sibling for a token/symbol sidecar.
func writeSidecarMeta(path string, shardPaths []string, numBlobs, extractors int) error {
	meta, err := json.Marshal(sidecarMeta{
		Fingerprint: corpusFingerprint(shardPaths),
		NumBlobs:    numBlobs,
		Extractors:  extractors,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(path+".meta", meta, 0o644)
}

// loadPersistedTokens loads tokenPath iff its sibling .meta matches the current
// corpus. Returns (nil,false) on any missing/corrupt/mismatch so OpenRank rebuilds.
func loadPersistedTokens(tokenPath string, shardPaths []string, numBlobs int) (*tokenindex.TokenIndex, bool) {
	// extractors=0: the token index has no extractor dependency (see sidecarMeta).
	if !sidecarMetaMatches(tokenPath, shardPaths, numBlobs, 0) {
		return nil, false
	}
	ti, err := tokenindex.Load(tokenPath)
	if err != nil {
		return nil, false
	}
	return ti, true
}

// savePersistedTokens writes the token index and its validating meta sidecar.
func savePersistedTokens(ti *tokenindex.TokenIndex, tokenPath string, shardPaths []string, numBlobs int) error {
	if err := tokenindex.Save(ti, tokenPath); err != nil {
		return err
	}
	return writeSidecarMeta(tokenPath, shardPaths, numBlobs, 0)
}

// loadPersistedSymbols loads symbolPath iff its sibling .meta matches the current
// corpus. Returns (nil,false) on any missing/corrupt/mismatch so OpenRank rebuilds.
func loadPersistedSymbols(symbolPath string, shardPaths []string, numBlobs int) (*symbol.Index, bool) {
	if !sidecarMetaMatches(symbolPath, shardPaths, numBlobs, symbol.ExtractorsVersion) {
		return nil, false
	}
	syms, err := symbol.Load(symbolPath)
	if err != nil {
		return nil, false
	}
	return syms, true
}

// savePersistedSymbols writes the symbol index and its validating meta sidecar.
func savePersistedSymbols(syms *symbol.Index, symbolPath string, shardPaths []string, numBlobs int) error {
	if err := symbol.Save(syms, symbolPath); err != nil {
		return err
	}
	return writeSidecarMeta(symbolPath, shardPaths, numBlobs, symbol.ExtractorsVersion)
}

// BuildSidecars builds and persists the token and symbol sidecars for the shard
// set under dir, to the same default paths OpenRank reads. The offline indexer
// calls this after a build/refresh so the daemon finds the caches warm and skips
// both rebuilds on its next boot. It shares loadUnified with OpenRank, so the
// blob IDs and fingerprint match byte-for-byte. Embeddings are not built here
// (they need the embedder; that is a serve-time concern). Returns the two data
// paths written.
func BuildSidecars(dir string) (tokenPath, symbolPath string, err error) {
	ix, paths, cs, err := loadUnified(dir)
	if err != nil {
		return "", "", err
	}
	// The sidecar builders read content to derive tokens/symbols but retain none of
	// the content slices, so a deduped dir's shared store can be released as soon as
	// both sidecars are built (it need not outlive this function).
	if cs != nil {
		defer cs.Close()
	}
	tokenPath = defaultTokenPath(dir)
	symbolPath = defaultSymbolPath(dir)
	if err := savePersistedTokens(tokenindex.Build(ix), tokenPath, paths, ix.NumBlobs()); err != nil {
		return "", "", fmt.Errorf("server: persist token index: %w", err)
	}
	if err := savePersistedSymbols(symbol.BuildMulti(ix), symbolPath, paths, ix.NumBlobs()); err != nil {
		return "", "", fmt.Errorf("server: persist symbol index: %w", err)
	}
	return tokenPath, symbolPath, nil
}

func chunkLines(cfg RankConfig) int {
	if cfg.LinesPerChunk <= 0 {
		return defaultChunkLines
	}
	return cfg.LinesPerChunk
}

func chunkOverlap(cfg RankConfig) int {
	if cfg.Overlap <= 0 {
		return defaultChunkOverlap
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

// TokensFromCache reports whether the BM25 token index was loaded from a
// persisted sidecar (true) rather than rebuilt at boot (false).
func (rc *RankCorpus) TokensFromCache() bool { return rc.tokenCached }

// SymbolsFromCache reports whether the symbol index was loaded from a persisted
// sidecar (true) rather than rebuilt at boot (false).
func (rc *RankCorpus) SymbolsFromCache() bool { return rc.symsCached }
