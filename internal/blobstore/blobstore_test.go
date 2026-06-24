package blobstore

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/ingest"
)

// --- git fixture helpers (adapted from internal/parity/manifest_test.go) ----

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func gitEnv() []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// commitGitRepo creates a git repo with files staged AND committed.
func commitGitRepo(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q")
	writeFiles(t, dir, files)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "init")
}

func gitCommitAll(t *testing.T, dir, msg string) {
	t.Helper()
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", msg)
}

// --- A. round-trip --------------------------------------------------------

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	blobs := map[string][]byte{
		"sha-aaa": []byte("package a\nfunc Alpha() {}\n"),
		"sha-bbb": []byte("package b\nfunc Bravo() {}\n"),
		"sha-ccc": bytes.Repeat([]byte("x"), 5000),
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var wantBytes int64
	for sha, c := range blobs {
		added, err := s.Put(sha, c)
		if err != nil {
			t.Fatal(err)
		}
		if !added {
			t.Errorf("Put(%s) added=false on first insert", sha)
		}
		wantBytes += int64(len(c))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen and verify every blob round-trips byte-for-byte.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.Len() != len(blobs) {
		t.Fatalf("Len after reopen = %d, want %d", s2.Len(), len(blobs))
	}
	if s2.BytesStored() != wantBytes {
		t.Errorf("BytesStored = %d, want %d", s2.BytesStored(), wantBytes)
	}
	for sha, want := range blobs {
		if !s2.Has(sha) {
			t.Errorf("Has(%s) = false after reopen", sha)
		}
		got, err := s2.Get(sha)
		if err != nil {
			t.Fatalf("Get(%s): %v", sha, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("Get(%s) = %q, want %q", sha, got, want)
		}
	}
	if _, err := s2.Get("sha-missing"); err == nil {
		t.Error("Get of absent sha should error")
	}
}

// --- B. GLOBAL DEDUP (headline win) --------------------------------------

// TestGlobalCrossShardDedup proves the cross-shard dedup the per-shard model
// cannot do: repoA and repoB carry a byte-identical file (same git SHA); repoC is
// unique. After BuildCAS the shared blob is stored EXACTLY ONCE for the whole
// corpus, so StoredBytes == sum of UNIQUE blob lengths and is strictly less than
// RawBytes (the bytes a per-shard build would inline, counting the duplicate).
func TestGlobalCrossShardDedup(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	shared := "package shared\n// a long shared helper used by two repos\nfunc Helper() int { return 42 }\n"
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	repoC := filepath.Join(corpus, "repoC")
	commitGitRepo(t, repoA, map[string]string{"shared.go": shared, "a.go": "package a\n"})
	commitGitRepo(t, repoB, map[string]string{"shared.go": shared, "b.go": "package b\n"})
	commitGitRepo(t, repoC, map[string]string{"c.go": "package c\nfunc Cee() {}\n"})

	casDir := t.TempDir()
	m, err := BuildCAS(corpus, casDir)
	if err != nil {
		t.Fatalf("BuildCAS: %v", err)
	}

	// 5 files total across 3 repos (repoA: shared.go+a.go, repoB: shared.go+b.go,
	// repoC: c.go); 4 distinct contents (shared.go counted once via cross-repo dedup).
	if m.Stats.FileRefs != 5 {
		t.Errorf("FileRefs = %d, want 5", m.Stats.FileRefs)
	}
	if m.Stats.UniqueBlobs != 4 {
		t.Errorf("UniqueBlobs = %d, want 4 (shared.go deduped across repoA/repoB)", m.Stats.UniqueBlobs)
	}

	// The shared blob must appear in BOTH repoA and repoB's blob sets but be a
	// single physical blob in the store.
	s, err := Open(casDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Len() != 4 {
		t.Errorf("store.Len = %d, want 4", s.Len())
	}

	sharedSHA := shaInRepo(t, m, repoA, "shared.go")
	if sharedSHA != shaInRepo(t, m, repoB, "shared.go") {
		t.Fatal("repoA and repoB shared.go must have the same git SHA (test premise)")
	}
	got, err := s.Get(sharedSHA)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != shared {
		t.Errorf("shared blob content = %q, want %q", got, shared)
	}

	// StoredBytes is the sum of the 5 UNIQUE contents; RawBytes counts the
	// duplicate shared.go twice. The difference is exactly one shared.go.
	if m.Stats.StoredBytes != s.BytesStored() {
		t.Errorf("manifest StoredBytes %d != store.BytesStored %d", m.Stats.StoredBytes, s.BytesStored())
	}
	wantSaving := int64(len(shared))
	if got := m.Stats.RawBytes - m.Stats.StoredBytes; got != wantSaving {
		t.Errorf("dedup saving = %d bytes, want exactly one shared.go (%d)", got, wantSaving)
	}
	if !(m.Stats.StoredBytes < m.Stats.RawBytes) {
		t.Errorf("StoredBytes (%d) must be < RawBytes (%d) — no dedup happened", m.Stats.StoredBytes, m.Stats.RawBytes)
	}
	if r := m.Stats.DedupRatio(); !(r > 1.0) {
		t.Errorf("DedupRatio = %.3f, want > 1.0", r)
	}
}

// --- C. DELTA REFRESH (second win) ---------------------------------------

// TestDeltaRefreshAddsOnlyNetNewBlobs proves the per-blob delta: after a CAS
// build, mutating ONE repo (adding a new file) and refreshing adds exactly the
// new blob(s) and ZERO bytes for co-resident/unchanged repos.
func TestDeltaRefreshAddsOnlyNetNewBlobs(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	commitGitRepo(t, repoA, map[string]string{"a.go": "package a\nconst A = 1\n"})
	commitGitRepo(t, repoB, map[string]string{"b.go": "package b\nconst B = 1\n"})

	casDir := t.TempDir()
	m, err := BuildCAS(corpus, casDir)
	if err != nil {
		t.Fatalf("BuildCAS: %v", err)
	}
	blobsBefore := m.Stats.UniqueBlobs
	bytesBefore := m.Stats.StoredBytes

	// Add ONE new file to repoB (keep b.go unchanged content), commit.
	newFile := "package b\nfunc FreshFn() {}\n"
	writeFiles(t, repoB, map[string]string{"fresh.go": newFile})
	gitCommitAll(t, repoB, "add fresh.go")

	m2, ds, err := RefreshCAS(m, corpus, casDir)
	if err != nil {
		t.Fatalf("RefreshCAS: %v", err)
	}

	if len(ds.ChangedRepos) != 1 || ds.ChangedRepos[0] != repoB {
		t.Errorf("ChangedRepos = %v, want [repoB]", ds.ChangedRepos)
	}
	if len(ds.AddedRepos) != 0 || len(ds.RemovedRepos) != 0 {
		t.Errorf("unexpected added/removed: %+v", ds)
	}
	// Exactly one net-new blob (fresh.go); b.go's content SHA is unchanged so its
	// Put is a dedup no-op.
	if ds.BlobsAdded != 1 {
		t.Errorf("BlobsAdded = %d, want 1 (only fresh.go is new)", ds.BlobsAdded)
	}
	if ds.BytesAdded != int64(len(newFile)) {
		t.Errorf("BytesAdded = %d, want exactly fresh.go (%d)", ds.BytesAdded, len(newFile))
	}
	// repoB had 2 files at refresh (b.go + fresh.go); b.go was already present.
	if ds.PutsSkipped < 1 {
		t.Errorf("PutsSkipped = %d, want >=1 (b.go already present)", ds.PutsSkipped)
	}
	if m2.Stats.UniqueBlobs != blobsBefore+1 {
		t.Errorf("UniqueBlobs = %d, want %d", m2.Stats.UniqueBlobs, blobsBefore+1)
	}
	if m2.Stats.StoredBytes != bytesBefore+int64(len(newFile)) {
		t.Errorf("StoredBytes grew by %d, want %d", m2.Stats.StoredBytes-bytesBefore, len(newFile))
	}

	// repoA must be carried forward verbatim (same blob set, same HEAD).
	ra, _ := m2.RepoOf(repoA)
	raOld, _ := m.RepoOf(repoA)
	if ra.Head != raOld.Head {
		t.Errorf("repoA HEAD changed across refresh: %q -> %q", raOld.Head, ra.Head)
	}
	if !equalStrs(ra.SortedBlobs(), raOld.SortedBlobs()) {
		t.Error("repoA blob set changed across an unrelated refresh")
	}
}

// TestDeltaRefreshReusedBlobAddsZeroBytes proves the dedup also applies on
// CHANGE: a changed repo whose only new file's content matches a blob already in
// the store adds ZERO bytes (the content is already deduplicated globally).
func TestDeltaRefreshReusedBlobAddsZeroBytes(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	shared := "package shared\nfunc S() {}\n"
	commitGitRepo(t, repoA, map[string]string{"shared.go": shared, "a.go": "package a\n"})
	commitGitRepo(t, repoB, map[string]string{"b.go": "package b\n"})

	casDir := t.TempDir()
	m, err := BuildCAS(corpus, casDir)
	if err != nil {
		t.Fatalf("BuildCAS: %v", err)
	}
	bytesBefore := m.Stats.StoredBytes

	// repoB gains a copy of repoA's shared.go (byte-identical content -> same SHA).
	writeFiles(t, repoB, map[string]string{"copy.go": shared})
	gitCommitAll(t, repoB, "copy shared content")

	m2, ds, err := RefreshCAS(m, corpus, casDir)
	if err != nil {
		t.Fatalf("RefreshCAS: %v", err)
	}
	if len(ds.ChangedRepos) != 1 || ds.ChangedRepos[0] != repoB {
		t.Fatalf("ChangedRepos = %v, want [repoB]", ds.ChangedRepos)
	}
	if ds.BlobsAdded != 0 {
		t.Errorf("BlobsAdded = %d, want 0 (copy.go reuses an existing blob)", ds.BlobsAdded)
	}
	if ds.BytesAdded != 0 {
		t.Errorf("BytesAdded = %d, want 0", ds.BytesAdded)
	}
	if m2.Stats.StoredBytes != bytesBefore {
		t.Errorf("StoredBytes changed by %d, want 0", m2.Stats.StoredBytes-bytesBefore)
	}
	// But repoB's file refs grew, and the manifest reflects the new path.
	rb, _ := m2.RepoOf(repoB)
	hasCopy := false
	for _, f := range rb.Files {
		if f.RelPath == "copy.go" {
			hasCopy = true
		}
	}
	if !hasCopy {
		t.Error("repoB manifest missing copy.go after refresh")
	}
}

// --- D. removed repo / liveness ------------------------------------------

// TestRemovedRepoDropsFromManifestBlobsRemainUnreferenced removes a repo and
// asserts (1) it drops from the manifest and (2) its now-unreferenced blobs
// remain in the append-only pack but are no longer referenced — a correct
// liveness signal for a future compactor (compaction is deferred in slice 1).
func TestRemovedRepoDropsFromManifestBlobsRemainUnreferenced(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	uniqueA := "package a\nfunc OnlyInA() {}\n"
	commitGitRepo(t, repoA, map[string]string{"a.go": uniqueA})
	commitGitRepo(t, repoB, map[string]string{"b.go": "package b\n"})

	casDir := t.TempDir()
	m, err := BuildCAS(corpus, casDir)
	if err != nil {
		t.Fatalf("BuildCAS: %v", err)
	}
	storedBefore := m.Stats.UniqueBlobs
	aSHA := shaInRepo(t, m, repoA, "a.go")

	if err := os.RemoveAll(repoA); err != nil {
		t.Fatal(err)
	}
	m2, ds, err := RefreshCAS(m, corpus, casDir)
	if err != nil {
		t.Fatalf("RefreshCAS: %v", err)
	}
	if len(ds.RemovedRepos) != 1 || ds.RemovedRepos[0] != repoA {
		t.Errorf("RemovedRepos = %v, want [repoA]", ds.RemovedRepos)
	}
	if _, ok := m2.RepoOf(repoA); ok {
		t.Error("repoA still present in manifest after removal")
	}
	// Slice-1 behavior: the pack still physically holds repoA's unique blob...
	if m2.Stats.UniqueBlobs != storedBefore {
		t.Errorf("UniqueBlobs = %d, want %d (append-only: blobs are NOT GC'd in slice 1)", m2.Stats.UniqueBlobs, storedBefore)
	}
	s, err := Open(casDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Has(aSHA) {
		t.Error("repoA's blob should still be in the append-only pack (no compaction yet)")
	}
	// ...but it is now UNREFERENCED: in the pack, not in any repo's blob set.
	live := m2.referencedBlobs()
	if live[aSHA] {
		t.Error("repoA's unique blob should be unreferenced after removal")
	}
	// Count unreferenced blobs (the compactor's liveness signal): exactly 1.
	unref := 0
	for _, sha := range s.SHAs() {
		if !live[sha] {
			unref++
		}
	}
	if unref != 1 {
		t.Errorf("unreferenced blob count = %d, want 1 (repoA's a.go); TODO: compaction reclaims these", unref)
	}
}

// --- E. manifest round-trip ----------------------------------------------

func TestBlobManifestRoundTrip(t *testing.T) {
	m := &BlobManifest{
		Version: BlobManifestVersion,
		Root:    "/corpus",
		CASDir:  "/cas",
		Repos: []RepoBlobs{
			{Dir: "/corpus/r1", Label: "r1", Head: strings.Repeat("a", 40), Files: []FileEntry{
				{SHA: "sha2", RelPath: "z.go"}, {SHA: "sha1", RelPath: "a.go"},
			}},
			{Dir: "/corpus/r2", Label: "r2", Head: "", Files: nil},
		},
		Stats: Stats{UniqueBlobs: 2, StoredBytes: 100, RawBytes: 150, FileRefs: 2},
	}
	path := filepath.Join(t.TempDir(), BlobManifestName)
	if err := WriteBlobManifest(path, m); err != nil {
		t.Fatal(err)
	}
	m2, err := LoadBlobManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Version != m.Version || m2.Root != m.Root || len(m2.Repos) != 2 {
		t.Fatalf("round-trip mismatch: %+v", m2)
	}
	if m2.Stats != m.Stats {
		t.Errorf("stats mismatch: %+v vs %+v", m2.Stats, m.Stats)
	}
	// SortedBlobs is order-independent of the recorded file order.
	got := m2.Repos[0].SortedBlobs()
	want := []string{"sha1", "sha2"}
	if !equalStrs(got, want) {
		t.Errorf("SortedBlobs = %v, want %v", got, want)
	}
	r2, ok := m2.RepoOf("/corpus/r2")
	if !ok || r2.Head != "" {
		t.Errorf("commitless repo head not preserved: %+v", r2)
	}
}

// --- F. export parity bridge ---------------------------------------------

// TestExportShardDirIsServable builds a CAS, exports a shard dir, and asserts the
// exported dir is a valid input to the existing diskstore loader and serving
// spine: every unique blob's content is findable, every repo appears in the
// exported parity.Manifest, and the unified blob count matches the corpus.
func TestExportShardDirIsServable(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	shared := "package shared\nfunc Help() {}\n"
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	commitGitRepo(t, repoA, map[string]string{"shared.go": shared, "a.go": "package a\nfunc MarkerA() {}\n"})
	commitGitRepo(t, repoB, map[string]string{"shared.go": shared, "b.go": "package b\nfunc MarkerB() {}\n"})

	casDir := t.TempDir()
	m, err := BuildCAS(corpus, casDir)
	if err != nil {
		t.Fatalf("BuildCAS: %v", err)
	}

	shardDir := filepath.Join(t.TempDir(), "shards")
	// Large shardBytes so everything lands in one shard (within-shard dedup active).
	em, err := ExportShardDir(casDir, shardDir, 1<<30)
	if err != nil {
		t.Fatalf("ExportShardDir: %v", err)
	}

	// Every repo must appear in the exported parity.Manifest's heads.
	if len(em.Heads) != 2 {
		t.Fatalf("exported manifest heads = %d, want 2", len(em.Heads))
	}
	for _, dir := range []string{repoA, repoB} {
		if _, ok := em.HeadOf(dir); !ok {
			t.Errorf("repo %s missing from exported manifest", dir)
		}
	}

	// Every unique blob's content must be findable by loading the exported shards.
	for _, sha := range openStore(t, casDir).SHAs() {
		s := openStore(t, casDir)
		want, _ := s.Get(sha)
		s.Close()
		if !shardDirFinds(t, shardDir, string(want)) {
			t.Errorf("exported shards do not contain blob %s content", sha)
		}
	}
	// And the marker strings (real content) are present.
	for _, needle := range []string{"MarkerA", "MarkerB", "func Help"} {
		if !shardDirFinds(t, shardDir, needle) {
			t.Errorf("exported shards missing %q", needle)
		}
	}

	// In a single shard, byte-identical shared.go is deduped: the exported shard
	// holds exactly the distinct-content count. repoA: shared.go, a.go; repoB:
	// shared.go, b.go => 3 distinct contents (shared.go once).
	wantUnique := distinctContentCount(t, m)
	gotBlobs := totalShardBlobs(t, shardDir)
	if gotBlobs != wantUnique {
		t.Errorf("exported single-shard blob count = %d, want %d (within-shard dedup)", gotBlobs, wantUnique)
	}
}

// --- G. commitless repo stability ----------------------------------------

func TestCommitlessRepoStableAcrossRefresh(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	repoA := filepath.Join(corpus, "repoA")
	commitGitRepo(t, repoA, map[string]string{"a.go": "package a\n"})

	// A commitless repo: git init only, no commit -> rev-parse HEAD errors.
	repoEmpty := filepath.Join(corpus, "repoEmpty")
	if err := os.MkdirAll(repoEmpty, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repoEmpty, "init", "-q")
	if _, err := ingest.Head(repoEmpty); err == nil {
		t.Fatal("expected ingest.Head to error on a commitless repo (test premise)")
	}

	casDir := t.TempDir()
	m, err := BuildCAS(corpus, casDir)
	if err != nil {
		t.Fatalf("BuildCAS: %v", err)
	}
	// The commitless repo records head "".
	re, ok := m.RepoOf(repoEmpty)
	if !ok || re.Head != "" {
		t.Fatalf("commitless repo head = %q, ok=%v; want \"\"", re.Head, ok)
	}

	// Two refreshes: the commitless repo must NOT be a perpetual change trigger
	// and ZERO blobs are added either time.
	for i := 0; i < 2; i++ {
		m2, ds, err := RefreshCAS(m, corpus, casDir)
		if err != nil {
			t.Fatalf("refresh #%d: %v", i+1, err)
		}
		if len(ds.ChangedRepos) != 0 {
			t.Errorf("refresh #%d: ChangedRepos = %v, want none (commitless repo stable)", i+1, ds.ChangedRepos)
		}
		if ds.BlobsAdded != 0 {
			t.Errorf("refresh #%d: BlobsAdded = %d, want 0", i+1, ds.BlobsAdded)
		}
		m = m2
	}
}

// --- recovery: truncated-pack open ---------------------------------------

// TestOpenRecoversTruncatedPack asserts the append-only recovery: a pack with
// extra bytes beyond the last persisted index (a crash between pack-append and
// index-rewrite) is rolled back to the last durable state on Open.
func TestOpenRecoversTruncatedPack(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("sha1", []byte("durable content\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil { // persists index + pack
		t.Fatal(err)
	}

	// Simulate a crash mid-Put: append garbage to the pack WITHOUT rewriting idx.
	packPath := filepath.Join(dir, packName)
	f, err := os.OpenFile(packPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("partial garbage from a crashed append")); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Open must truncate the unindexed tail and recover the durable state.
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open after partial append: %v", err)
	}
	defer s2.Close()
	if s2.Len() != 1 {
		t.Errorf("recovered Len = %d, want 1", s2.Len())
	}
	got, err := s2.Get("sha1")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "durable content\n" {
		t.Errorf("recovered content = %q", got)
	}
	// A subsequent Put must succeed (pack offset is consistent again).
	added, err := s2.Put("sha2", []byte("after recovery\n"))
	if err != nil || !added {
		t.Fatalf("Put after recovery: added=%v err=%v", added, err)
	}
}

// --- shared test helpers --------------------------------------------------

func shaInRepo(t *testing.T, m *BlobManifest, repoDir, rel string) string {
	t.Helper()
	r, ok := m.RepoOf(repoDir)
	if !ok {
		t.Fatalf("repo %s not in manifest", repoDir)
	}
	for _, f := range r.Files {
		if f.RelPath == rel {
			return f.SHA
		}
	}
	t.Fatalf("file %s not in repo %s", rel, repoDir)
	return ""
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func openStore(t *testing.T, casDir string) *Store {
	t.Helper()
	s, err := Open(casDir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// distinctContentCount returns how many distinct blob SHAs the corpus produced,
// reading the manifest's recorded file entries.
func distinctContentCount(t *testing.T, m *BlobManifest) int {
	t.Helper()
	set := map[string]bool{}
	for _, r := range m.Repos {
		for _, f := range r.Files {
			set[f.SHA] = true
		}
	}
	return len(set)
}

// shardDirFinds reports whether any *.idx shard under shardDir holds a blob whose
// content contains needle (independent of the query layer).
func shardDirFinds(t *testing.T, shardDir, needle string) bool {
	t.Helper()
	entries, err := os.ReadDir(shardDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".idx" {
			continue
		}
		ix, err := diskstore.Load(filepath.Join(shardDir, e.Name()))
		if err != nil {
			t.Fatalf("load %s: %v", e.Name(), err)
		}
		for id := 0; id < ix.NumBlobs(); id++ {
			if strings.Contains(string(ix.Blob(uint64(id)).Content), needle) {
				return true
			}
		}
	}
	return false
}

// totalShardBlobs sums distinct blobs across all exported shards.
func totalShardBlobs(t *testing.T, shardDir string) int {
	t.Helper()
	entries, err := os.ReadDir(shardDir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".idx" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	n := 0
	for _, name := range names {
		ix, err := diskstore.Load(filepath.Join(shardDir, name))
		if err != nil {
			t.Fatal(err)
		}
		n += ix.NumBlobs()
	}
	return n
}
