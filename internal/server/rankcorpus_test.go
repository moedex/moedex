package server

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/embed"
	"moedex/internal/index"
	"moedex/internal/rank"
)

// buildDedupedDir writes a real deduped served dir (one MOEDEX05 shard per repo +
// one shared blobs.dat), keyed by the canonical git-blob SHA-1 so the default-on
// content verification accepts it. repos maps repo label -> {relpath -> content}.
// It returns the dir. This stays within diskstore + index (no blobstore/git
// dependency) so it is a fast, hermetic server-package unit fixture.
func buildDedupedDir(t *testing.T, repos map[string]map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	cw := diskstore.NewContentStoreWriter()
	labels := make([]string, 0, len(repos))
	for label := range repos {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for i, label := range labels {
		ix := index.New()
		files := repos[label]
		rels := make([]string, 0, len(files))
		for rel := range files {
			rels = append(rels, rel)
		}
		sort.Strings(rels)
		for _, rel := range rels {
			content := []byte(files[rel])
			sha := diskstore.GitBlobSHA1(content)
			ix.AddFile(label, rel, filepath.Join("/abs", label, rel), sha, content)
		}
		shardPath := filepath.Join(dir, "shard-"+pad4(i)+".idx")
		if err := diskstore.SaveDeduped(ix, shardPath, cw); err != nil {
			t.Fatalf("SaveDeduped %s: %v", shardPath, err)
		}
	}
	if err := cw.Write(filepath.Join(dir, diskstore.ContentStoreName)); err != nil {
		t.Fatalf("write content store: %v", err)
	}
	return dir
}

func pad4(i int) string {
	s := []byte{'0', '0', '0', '0'}
	for d := 3; d >= 0 && i > 0; d-- {
		s[d] = byte('0' + i%10)
		i /= 10
	}
	return string(s)
}

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

// TestCorpusFingerprintIncludesContentStore is the GAP-2 test: the rank sidecar
// fingerprint must fold in the shared content store (blobs.dat), so a change to the
// shared content store invalidates the fingerprint even when the shard files are
// byte-for-byte unchanged. Without this, a deduped dir could reuse token/symbol/
// embedding sidecars built over OLD content after blobs.dat was rewritten.
func TestCorpusFingerprintIncludesContentStore(t *testing.T) {
	dir := t.TempDir()
	shard := filepath.Join(dir, "shard-0000.idx")
	if err := os.WriteFile(shard, []byte("MOEDEX05 stand-in shard bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	shardPaths := []string{shard}

	// Legacy dir (no blobs.dat): a baseline fingerprint.
	legacyFP := corpusFingerprint(shardPaths)

	// Write a shared content store; the fingerprint MUST change (a legacy vs deduped
	// dir with the same shard files are different corpora).
	csPath := filepath.Join(dir, "blobs.dat")
	writeFakeContentStore(t, csPath, 3, "alpha")
	withStoreFP := corpusFingerprint(shardPaths)
	if withStoreFP == legacyFP {
		t.Error("adding blobs.dat did not change the fingerprint (content store not folded in)")
	}

	// Change ONLY the content store (shard files untouched): the fingerprint MUST
	// change again. This is the core GAP-2 invariant — stale-content detection.
	writeFakeContentStore(t, csPath, 4, "alpha-grown") // different numBlobs AND size
	changedFP := corpusFingerprint(shardPaths)
	if changedFP == withStoreFP {
		t.Error("changing blobs.dat did not change the fingerprint (stale sidecars would be reused)")
	}

	// A content store with the SAME byte length but a different MOECONT1 header
	// (numBlobs/dirOff) must STILL change the fingerprint — header folding, not just
	// size. Rewrite with the same payload length but a different blob count.
	writeFakeContentStore(t, csPath, 4, "alpha-grown") // re-establish a known state
	sameSizeFP := corpusFingerprint(shardPaths)
	writeFakeContentStoreSameLen(t, csPath, 9) // same total length, different numBlobs in header
	headerChangedFP := corpusFingerprint(shardPaths)
	if headerChangedFP == sameSizeFP {
		t.Error("a same-size content store with a different MOECONT1 header did not change the fingerprint")
	}
}

// writeFakeContentStore writes a minimal MOECONT1-shaped file: a 32-byte header
// (magic, version, reserved, numBlobs, dirOff) followed by payload bytes. It is
// only for fingerprint tests (corpusFingerprint reads name+size+header, not the
// directory), so the body need not be a valid directory.
func writeFakeContentStore(t *testing.T, path string, numBlobs uint64, payload string) {
	t.Helper()
	buf := make([]byte, 32)
	copy(buf[0:8], "MOECONT1")
	binary.LittleEndian.PutUint32(buf[8:12], 1)
	binary.LittleEndian.PutUint64(buf[16:24], numBlobs)
	binary.LittleEndian.PutUint64(buf[24:32], uint64(32+len(payload)))
	buf = append(buf, []byte(payload)...)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeFakeContentStoreSameLen rewrites path keeping its TOTAL byte length but with
// a different numBlobs in the header, to prove the fingerprint folds the header
// (not just the file size).
func writeFakeContentStoreSameLen(t *testing.T, path string, numBlobs uint64) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	total := int(fi.Size())
	if total < 32 {
		t.Fatalf("existing store too small (%d)", total)
	}
	buf := make([]byte, total)
	copy(buf[0:8], "MOECONT1")
	binary.LittleEndian.PutUint32(buf[8:12], 1)
	binary.LittleEndian.PutUint64(buf[16:24], numBlobs)
	binary.LittleEndian.PutUint64(buf[24:32], uint64(total))
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDedupedSidecarsInvalidatedByContentStoreChange is the end-to-end GAP-2 proof:
// over a REAL deduped dir, mutating blobs.dat after sidecars are built makes the
// next OpenRank treat them as stale and rebuild (rather than silently reusing
// sidecars derived from the old content). It exercises the actual load-or-build
// path, not just corpusFingerprint in isolation.
func TestDedupedSidecarsInvalidatedByContentStoreChange(t *testing.T) {
	dir := buildDedupedDir(t, map[string]map[string]string{
		"repoA": {"a.go": "package a\nfunc Alpha() {}\n"},
		"repoB": {"b.go": "package b\nfunc Bravo() {}\n"},
	})

	// First open builds + persists the sidecars.
	rc1, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("initial OpenRank: %v", err)
	}
	_ = rc1.Close()

	// Second open over the unchanged dir LOADS the sidecars from cache.
	rc2, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("second OpenRank: %v", err)
	}
	cachedBefore := rc2.TokensFromCache() && rc2.SymbolsFromCache()
	_ = rc2.Close()
	if !cachedBefore {
		t.Fatal("second open over an unchanged deduped dir should LOAD both sidecars from cache")
	}

	// Mutate ONLY blobs.dat (append a byte): its size + header-derived identity
	// changes, the shard files do not. The fingerprint must now differ, forcing a
	// sidecar rebuild — proving stale-content sidecars are not reused.
	csPath := filepath.Join(dir, "blobs.dat")
	data, err := os.ReadFile(csPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(csPath, append(data, 0x00), 0o644); err != nil {
		t.Fatal(err)
	}
	// The appended trailing byte does not corrupt the directory (it is read via the
	// dirOff in the header), so the store still opens + verifies; only the
	// fingerprint changes. Disable content verification for this open since the
	// padding byte is not part of any blob's content but does not affect parsing.
	t.Setenv("MOEDEX_VERIFY_CONTENT", "0")
	rc3, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("OpenRank after blobs.dat change: %v", err)
	}
	defer rc3.Close()
	if rc3.TokensFromCache() || rc3.SymbolsFromCache() {
		t.Error("a changed blobs.dat must invalidate both sidecars (expected rebuild, got cache load)")
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
