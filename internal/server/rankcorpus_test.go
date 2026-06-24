package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/embed"
	"moedex/internal/rank"
)

// conceptEmbedder is a deterministic, network-free Embedder for tests. It
// projects text onto two concept axes by substring, so lexically-disjoint text
// about the same concept (the doc says "Authenticate", the query says "verify
// user identity") lands on the same axis and scores high — exactly the
// synonym/no-overlap recall the dense arm exists to recover.
type conceptEmbedder struct{}

func (conceptEmbedder) Dim() int { return 2 }

func (conceptEmbedder) Embed(_ context.Context, texts []string) ([]embed.Vector, error) {
	identity := []string{"authenticate", "auth", "verify", "identity", "credential", "login", "user", "session"}
	math := []string{"add", "sum", "math", "arithmetic", "multiply", "divide"}
	out := make([]embed.Vector, len(texts))
	for i, t := range texts {
		low := strings.ToLower(t)
		v := embed.Vector{0, 0}
		for _, kw := range identity {
			if strings.Contains(low, kw) {
				v[0]++
			}
		}
		for _, kw := range math {
			if strings.Contains(low, kw) {
				v[1]++
			}
		}
		out[i] = v
	}
	return out, nil
}

func TestRankCorpusDenseArmRecoversNoOverlapQuery(t *testing.T) {
	dir := t.TempDir()
	buildShard(t, dir, "shard-0000.idx", map[string]string{
		"auth/login.go": "package auth\n\nfunc Authenticate(token string) bool {\n\treturn token != \"\"\n}\n",
		"calc/add.go":   "package calc\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n",
	})

	// Query shares NO token with the auth file ("verify"/"user"/"identity" vs
	// "authenticate"/"token"). Lexical alone cannot reach it.
	const q = "verify user identity"

	lexOnly, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("OpenRank lexical: %v", err)
	}
	win, err := lexOnly.SearchContext(context.Background(), q, 800, 5)
	if err != nil {
		t.Fatalf("SearchContext lexical: %v", err)
	}
	if len(win.Blocks) != 0 {
		t.Fatalf("lexical-only should not reach the no-overlap query, got %d blocks", len(win.Blocks))
	}

	// Rank.DenseMinQueryTerms:-1 disables the production query-length gate: this test
	// uses a short (3-term) synonym query to exercise the dense MECHANISM, which the
	// default gate (>= 5 terms) would otherwise suppress.
	dense, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5, Emb: conceptEmbedder{}, LinesPerChunk: 20, Overlap: 5, Rank: rank.Config{DenseMinQueryTerms: -1}})
	if err != nil {
		t.Fatalf("OpenRank dense: %v", err)
	}
	if dense.DenseChunks() == 0 {
		t.Fatal("expected embedded chunks, got 0")
	}
	dwin, err := dense.SearchContext(context.Background(), q, 800, 5)
	if err != nil {
		t.Fatalf("SearchContext dense: %v", err)
	}
	if len(dwin.Blocks) == 0 || !strings.Contains(dwin.Blocks[0].RelPath, "login.go") {
		t.Errorf("dense arm should surface auth/login.go for %q, got %+v", q, dwin.Blocks)
	}
}

func TestRankCorpusRanksAcrossShards(t *testing.T) {
	dir := t.TempDir()
	// Two shards, each with a distinct Go definition. The symbol arm should fire
	// (Go extractor) and the BM25 arm should pull the defining file to the top.
	buildShard(t, dir, "shard-0000.idx", map[string]string{
		"billing/refund.go": "package billing\n\n// Refund reverses a captured charge.\nfunc Refund(id string) error {\n\treturn nil\n}\n",
		"util/strings.go":   "package util\n\nfunc TrimSpace(s string) string { return s }\n",
	})
	buildShard(t, dir, "shard-0001.idx", map[string]string{
		"auth/login.go": "package auth\n\n// Authenticate verifies user credentials.\nfunc Authenticate(user, pass string) bool {\n\treturn false\n}\n",
	})

	rc, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("OpenRank: %v", err)
	}
	if rc.NumBlobs() != 3 {
		t.Errorf("NumBlobs = %d, want 3", rc.NumBlobs())
	}
	if rc.NumDocs() != 3 {
		t.Errorf("NumDocs = %d, want 3", rc.NumDocs())
	}
	if rc.NumSymbolBlobs() == 0 {
		t.Error("expected Go symbol extraction to populate symbol blobs, got 0")
	}

	// Query for a term that defines a symbol in shard 0; the top context block
	// must come from the defining file, proving ranking works over the merged
	// global blob-ID space.
	win, err := rc.SearchContext(context.Background(), "refund", 800, 5)
	if err != nil {
		t.Fatalf("SearchContext: %v", err)
	}
	if len(win.Blocks) == 0 {
		t.Fatal("expected at least one context block for 'refund', got none")
	}
	if !strings.Contains(win.Blocks[0].RelPath, "refund.go") {
		t.Errorf("top block = %q, want billing/refund.go", win.Blocks[0].RelPath)
	}

	// A term that only exists in shard 1 must still be reachable from the merged
	// corpus, proving the second shard's blobs are ranked too.
	win2, err := rc.SearchContext(context.Background(), "authenticate", 800, 5)
	if err != nil {
		t.Fatalf("SearchContext authenticate: %v", err)
	}
	if len(win2.Blocks) == 0 || !strings.Contains(win2.Blocks[0].RelPath, "login.go") {
		t.Errorf("expected auth/login.go on top for 'authenticate', got %+v", win2.Blocks)
	}
}

func TestRankCorpusPersistsAndReloadsEmbeddings(t *testing.T) {
	dir := t.TempDir()
	buildShard(t, dir, "shard-0000.idx", map[string]string{
		"auth/login.go": "package auth\n\nfunc Authenticate(token string) bool { return token != \"\" }\n",
		"calc/add.go":   "package calc\n\nfunc Add(a, b int) int { return a + b }\n",
	})
	storePath := filepath.Join(dir, "corpus-embeddings.store")
	// Rank.DenseMinQueryTerms:-1 disables the production query-length gate so the short
	// synonym query after reload exercises the dense arm (see the no-overlap test).
	cfg := RankConfig{TopK: 5, Emb: conceptEmbedder{}, EmbedModel: "concept-v1", StorePath: storePath, LinesPerChunk: 20, Overlap: 5, Rank: rank.Config{DenseMinQueryTerms: -1}}

	// First open builds and persists.
	first, err := OpenRank(context.Background(), dir, cfg)
	if err != nil {
		t.Fatalf("OpenRank build: %v", err)
	}
	if first.DenseFromCache() {
		t.Error("first open should BUILD, not load from cache")
	}
	if first.DenseChunks() == 0 {
		t.Fatal("expected embedded chunks on build")
	}
	if _, err := os.Stat(storePath); err != nil {
		t.Fatalf("store sidecar not written: %v", err)
	}
	if _, err := os.Stat(storePath + ".meta"); err != nil {
		t.Fatalf("store meta sidecar not written: %v", err)
	}

	// Second open over the unchanged corpus must LOAD from cache.
	second, err := OpenRank(context.Background(), dir, cfg)
	if err != nil {
		t.Fatalf("OpenRank reload: %v", err)
	}
	if !second.DenseFromCache() {
		t.Error("second open should LOAD from the persisted sidecar")
	}
	if second.DenseChunks() != first.DenseChunks() {
		t.Errorf("cached chunk count %d != built %d", second.DenseChunks(), first.DenseChunks())
	}
	// The dense arm must still work after a cache load.
	win, err := second.SearchContext(context.Background(), "verify user identity", 800, 5)
	if err != nil {
		t.Fatalf("SearchContext after reload: %v", err)
	}
	if len(win.Blocks) == 0 || !strings.Contains(win.Blocks[0].RelPath, "login.go") {
		t.Errorf("cached dense arm failed to surface auth/login.go, got %+v", win.Blocks)
	}

	// A model change must invalidate the cache (rebuild, not load).
	cfg2 := cfg
	cfg2.EmbedModel = "concept-v2"
	third, err := OpenRank(context.Background(), dir, cfg2)
	if err != nil {
		t.Fatalf("OpenRank model-change: %v", err)
	}
	if third.DenseFromCache() {
		t.Error("a model change must invalidate the cache (expected rebuild)")
	}
}

func TestRankCorpusCacheInvalidatedByCorpusChange(t *testing.T) {
	dir := t.TempDir()
	buildShard(t, dir, "shard-0000.idx", map[string]string{"a/x.go": "package a\nfunc Foo() {}\n"})
	storePath := filepath.Join(dir, "corpus-embeddings.store")
	cfg := RankConfig{TopK: 5, Emb: conceptEmbedder{}, EmbedModel: "m", StorePath: storePath, LinesPerChunk: 20, Overlap: 5}

	if _, err := OpenRank(context.Background(), dir, cfg); err != nil {
		t.Fatalf("initial build: %v", err)
	}
	// Add a second shard: the fingerprint changes, so the cache is stale.
	buildShard(t, dir, "shard-0001.idx", map[string]string{"b/y.go": "package b\nfunc Bar() {}\n"})
	rc, err := OpenRank(context.Background(), dir, cfg)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if rc.DenseFromCache() {
		t.Error("adding a shard must invalidate the cache (expected rebuild)")
	}
	if rc.NumBlobs() != 2 {
		t.Errorf("NumBlobs = %d, want 2 after adding a shard", rc.NumBlobs())
	}
}

func TestOpenRankEmptyDirErrors(t *testing.T) {
	if _, err := OpenRank(context.Background(), t.TempDir(), RankConfig{}); err == nil {
		t.Error("expected error opening dir with no shards, got nil")
	}
}

func TestRankCorpusPersistsAndReloadsTokenAndSymbolSidecars(t *testing.T) {
	dir := t.TempDir()
	buildShard(t, dir, "shard-0000.idx", map[string]string{
		"billing/refund.go": "package billing\n\n// Refund reverses a captured charge.\nfunc Refund(id string) error { return nil }\n",
		"util/strings.go":   "package util\n\nfunc TrimSpace(s string) string { return s }\n",
	})

	// First open BUILDS both indexes and persists the four sidecar files.
	first, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("OpenRank build: %v", err)
	}
	if first.TokensFromCache() || first.SymbolsFromCache() {
		t.Error("first open should BUILD, not load token/symbol from cache")
	}
	for _, name := range []string{"corpus-tokens.tki", "corpus-tokens.tki.meta", "corpus-symbols.sym", "corpus-symbols.sym.meta"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("sidecar %s not written: %v", name, err)
		}
	}

	// Second open over the unchanged corpus must LOAD both from cache.
	second, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("OpenRank reload: %v", err)
	}
	if !second.TokensFromCache() {
		t.Error("second open should LOAD the token sidecar")
	}
	if !second.SymbolsFromCache() {
		t.Error("second open should LOAD the symbol sidecar")
	}
	if second.NumDocs() != first.NumDocs() {
		t.Errorf("cached NumDocs %d != built %d", second.NumDocs(), first.NumDocs())
	}
	if second.NumSymbolBlobs() != first.NumSymbolBlobs() {
		t.Errorf("cached NumSymbolBlobs %d != built %d", second.NumSymbolBlobs(), first.NumSymbolBlobs())
	}

	// The loaded indexes must still rank: the defining file tops the results.
	win, err := second.SearchContext(context.Background(), "refund", 800, 5)
	if err != nil {
		t.Fatalf("SearchContext after reload: %v", err)
	}
	if len(win.Blocks) == 0 || !strings.Contains(win.Blocks[0].RelPath, "refund.go") {
		t.Errorf("cached indexes failed to surface billing/refund.go, got %+v", win.Blocks)
	}
}

func TestRankCorpusSidecarsInvalidatedByCorpusChange(t *testing.T) {
	dir := t.TempDir()
	buildShard(t, dir, "shard-0000.idx", map[string]string{"a/x.go": "package a\nfunc Foo() {}\n"})

	if _, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5}); err != nil {
		t.Fatalf("initial build: %v", err)
	}
	// Add a second shard: the fingerprint changes, so both sidecars are stale.
	buildShard(t, dir, "shard-0001.idx", map[string]string{"b/y.go": "package b\nfunc Bar() {}\n"})
	rc, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if rc.TokensFromCache() || rc.SymbolsFromCache() {
		t.Error("adding a shard must invalidate both sidecars (expected rebuild)")
	}
	if rc.NumBlobs() != 2 {
		t.Errorf("NumBlobs = %d, want 2 after adding a shard", rc.NumBlobs())
	}

	// A third open over the now-stable set must LOAD the re-persisted sidecars.
	third, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	if !third.TokensFromCache() || !third.SymbolsFromCache() {
		t.Error("third open should LOAD the re-persisted sidecars")
	}
}

func TestRankCorpusCorruptSidecarFallsBackToRebuild(t *testing.T) {
	dir := t.TempDir()
	buildShard(t, dir, "shard-0000.idx", map[string]string{"a/x.go": "package a\nfunc Foo() {}\n"})

	if _, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5}); err != nil {
		t.Fatalf("initial build: %v", err)
	}
	// Corrupt the token data file (the .meta still matches), so Load must fail and
	// OpenRank must fall back to a clean rebuild — never erroring the boot.
	if err := os.WriteFile(filepath.Join(dir, "corpus-tokens.tki"), []byte("not a valid TKI file"), 0o644); err != nil {
		t.Fatalf("corrupt token sidecar: %v", err)
	}
	rc, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("OpenRank over corrupt sidecar must not error: %v", err)
	}
	if rc.TokensFromCache() {
		t.Error("a corrupt token sidecar must trigger a rebuild, not a load")
	}

	// The rebuild re-persisted a valid file; a subsequent open loads it.
	again, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("re-open after rebuild: %v", err)
	}
	if !again.TokensFromCache() {
		t.Error("re-persisted token sidecar should load on the next open")
	}
}
