package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"moedex/internal/rank"
)

// geometryDir builds a two-file corpus big enough to chunk under both the default
// (40-line) and a small explicit window.
func geometryDir(t *testing.T) (dir, storePath string) {
	t.Helper()
	dir = t.TempDir()
	long := "package auth\n"
	for i := 0; i < 60; i++ {
		long += "// line of context to make this blob span several chunks\n"
	}
	long += "func Authenticate(token string) bool { return token != \"\" }\n"
	buildShard(t, dir, "shard-0000.idx", map[string]string{
		"auth/login.go": long,
		"calc/add.go":   "package calc\n\nfunc Add(a, b int) int { return a + b }\n",
	})
	return dir, filepath.Join(dir, "corpus-embeddings.store")
}

// geometryCfg is a dense-arm config at an explicit chunk geometry.
func geometryCfg(storePath string, lines, overlap int) RankConfig {
	return RankConfig{
		TopK:          5,
		Emb:           conceptEmbedder{},
		EmbedModel:    "concept-v1",
		StorePath:     storePath,
		LinesPerChunk: lines,
		Overlap:       overlap,
		Rank:          rank.Config{DenseMinQueryTerms: -1},
	}
}

// readStoreMetaMap reads an embedding .meta as a raw map so a test can assert on
// field presence as well as value.
func readStoreMetaMap(t *testing.T, storePath string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(storePath + ".meta")
	if err != nil {
		t.Fatalf("read store meta: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal store meta: %v", err)
	}
	return m
}

func writeStoreMetaMap(t *testing.T, storePath string, m map[string]any) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal store meta: %v", err)
	}
	if err := os.WriteFile(storePath+".meta", raw, 0o644); err != nil {
		t.Fatalf("write store meta: %v", err)
	}
}

// TestEmbeddingMetaRecordsChunkGeometry pins what lands on disk: the RESOLVED
// geometry, so an unset config records the defaults rather than zeros.
func TestEmbeddingMetaRecordsChunkGeometry(t *testing.T) {
	// Explicit geometry is recorded as given.
	dir, storePath := geometryDir(t)
	rc, err := OpenRank(context.Background(), dir, geometryCfg(storePath, 20, 5))
	if err != nil {
		t.Fatalf("OpenRank: %v", err)
	}
	defer rc.Close()
	m := readStoreMetaMap(t, storePath)
	if got, want := m["lines_per_chunk"], float64(20); got != want {
		t.Errorf("lines_per_chunk = %v, want %v", got, want)
	}
	if got, want := m["overlap"], float64(5); got != want {
		t.Errorf("overlap = %v, want %v", got, want)
	}

	// An unset geometry records the resolved DEFAULTS, not 0 — otherwise a later
	// config that spells out the defaults would spuriously invalidate the store.
	dir2, storePath2 := geometryDir(t)
	cfg := geometryCfg(storePath2, 0, 0)
	rc2, err := OpenRank(context.Background(), dir2, cfg)
	if err != nil {
		t.Fatalf("OpenRank defaults: %v", err)
	}
	defer rc2.Close()
	m2 := readStoreMetaMap(t, storePath2)
	if got, want := m2["lines_per_chunk"], float64(defaultChunkLines); got != want {
		t.Errorf("default lines_per_chunk = %v, want %v", got, want)
	}
	if got, want := m2["overlap"], float64(defaultChunkOverlap); got != want {
		t.Errorf("default overlap = %v, want %v", got, want)
	}
	// And that store must reload from cache when the defaults are spelled out.
	explicit := geometryCfg(storePath2, defaultChunkLines, defaultChunkOverlap)
	rc3, err := OpenRank(context.Background(), dir2, explicit)
	if err != nil {
		t.Fatalf("OpenRank explicit defaults: %v", err)
	}
	defer rc3.Close()
	if !rc3.DenseFromCache() {
		t.Error("spelling out the default geometry must not invalidate a default-built store")
	}
}

// TestEmbeddingStoreInvalidatedByChunkGeometry is the behavior these fields exist
// for: same corpus, same model, different chunk window or overlap means the
// vectors describe text that no longer exists in that form, so the store must be
// rebuilt rather than served.
func TestEmbeddingStoreInvalidatedByChunkGeometry(t *testing.T) {
	for _, tc := range []struct {
		name           string
		lines, overlap int
	}{
		{"window change", 30, 5},
		{"overlap change", 20, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, storePath := geometryDir(t)
			built, err := OpenRank(context.Background(), dir, geometryCfg(storePath, 20, 5))
			if err != nil {
				t.Fatalf("OpenRank build: %v", err)
			}
			defer built.Close()
			if built.DenseFromCache() {
				t.Fatal("first open should build")
			}

			changed, err := OpenRank(context.Background(), dir, geometryCfg(storePath, tc.lines, tc.overlap))
			if err != nil {
				t.Fatalf("OpenRank after geometry change: %v", err)
			}
			defer changed.Close()
			if changed.DenseFromCache() {
				t.Error("a chunk-geometry change must invalidate the embedding store")
			}
			if changed.DenseChunks() == 0 {
				t.Error("expected the store to be rebuilt at the new geometry")
			}

			// The rebuild re-persisted the new geometry, so the next open is warm.
			warm, err := OpenRank(context.Background(), dir, geometryCfg(storePath, tc.lines, tc.overlap))
			if err != nil {
				t.Fatalf("OpenRank warm: %v", err)
			}
			defer warm.Close()
			if !warm.DenseFromCache() {
				t.Error("after rebuilding at the new geometry the store should load from cache")
			}
		})
	}
}

// TestPreGeometryEmbeddingMetaReusedAtDefaults covers the compat case that shaped
// the design: a .meta written before these fields existed carries no geometry, and
// such a store was necessarily built at the defaults (nothing outside this package
// can set the geometry). It must therefore still load under default geometry — an
// embedding store is the one sidecar whose invalidation costs a full corpus
// re-embed, so a needless invalidation here is expensive.
func TestPreGeometryEmbeddingMetaReusedAtDefaults(t *testing.T) {
	dir, storePath := geometryDir(t)
	cfg := geometryCfg(storePath, 0, 0) // defaults
	built, err := OpenRank(context.Background(), dir, cfg)
	if err != nil {
		t.Fatalf("OpenRank build: %v", err)
	}
	defer built.Close()

	// Strip the geometry, leaving a pre-versioning .meta.
	m := readStoreMetaMap(t, storePath)
	delete(m, "lines_per_chunk")
	delete(m, "overlap")
	writeStoreMetaMap(t, storePath, m)

	reopened, err := OpenRank(context.Background(), dir, cfg)
	if err != nil {
		t.Fatalf("OpenRank reload: %v", err)
	}
	defer reopened.Close()
	if !reopened.DenseFromCache() {
		t.Error("a pre-geometry store must still load at default geometry (no forced re-embed)")
	}

	// But a NON-default geometry against that same pre-geometry meta must still
	// invalidate — the backfill assumes the defaults, it does not wave the check.
	changed, err := OpenRank(context.Background(), dir, geometryCfg(storePath, 20, 5))
	if err != nil {
		t.Fatalf("OpenRank non-default: %v", err)
	}
	defer changed.Close()
	if changed.DenseFromCache() {
		t.Error("a non-default geometry must invalidate a pre-geometry store")
	}
}

// TestRefreshEmbeddings_GeometryChangeRebuilds guards the incremental refresh path:
// its up-to-date/re-key fast path must not report a differently-chunked store as
// current, or the rebuild would be skipped entirely.
func TestRefreshEmbeddings_GeometryChangeRebuilds(t *testing.T) {
	dir, storePath := geometryDir(t)
	var calls int
	cfg := geometryCfg(storePath, 20, 5)
	cfg.Emb = countingEmbedder{inner: conceptEmbedder{}, calls: &calls}

	first, err := RefreshEmbeddings(context.Background(), dir, cfg)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if first.TotalChunks == 0 {
		t.Fatal("expected chunks from the first refresh")
	}

	// Unchanged corpus AND geometry: up to date, nothing embedded.
	calls = 0
	same, err := RefreshEmbeddings(context.Background(), dir, cfg)
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if !same.UpToDate {
		t.Errorf("unchanged corpus+geometry should be UpToDate, got %+v", same)
	}
	if calls != 0 {
		t.Errorf("up-to-date refresh embedded %d texts, want 0", calls)
	}

	// Same corpus, different geometry: must NOT be up to date, and must re-chunk.
	calls = 0
	changed := cfg
	changed.LinesPerChunk = 30
	third, err := RefreshEmbeddings(context.Background(), dir, changed)
	if err != nil {
		t.Fatalf("geometry-change refresh: %v", err)
	}
	if third.UpToDate {
		t.Error("a geometry change must not report UpToDate")
	}
	if calls == 0 {
		t.Error("a geometry change must embed the newly-chunked text")
	}
	// The store now on disk is the re-chunked one, and it serves warm.
	if got := readStoreMetaMap(t, storePath)["lines_per_chunk"]; got != float64(30) {
		t.Errorf("refreshed meta lines_per_chunk = %v, want 30", got)
	}
	rc, err := OpenRank(context.Background(), dir, changed)
	if err != nil {
		t.Fatalf("OpenRank after refresh: %v", err)
	}
	defer rc.Close()
	if !rc.DenseFromCache() {
		t.Error("the refreshed store should load from cache at the same geometry")
	}
	if rc.DenseChunks() != third.TotalChunks {
		t.Errorf("served %d chunks, refresh wrote %d", rc.DenseChunks(), third.TotalChunks)
	}
}
