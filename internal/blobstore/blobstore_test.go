package blobstore

import (
	"bytes"
	"fmt"
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

// --- BUG 2 regression: transient re-ingest failure must not drop a repo ----

// TestRefreshIngestFailureCarriesForwardRepo is the regression for the silent
// data-loss bug: a CHANGED repo that is STILL ON DISK but whose re-ingest
// transiently fails (a git/read hiccup) must NOT be classified as removed and
// dropped. Doing so would erase all of that repo's previously searchable content
// from every subsequent export. The repo's prior manifest entry must be carried
// forward unchanged and the failure surfaced (ds.FailedRepos), so a future export
// still finds its blobs and the next refresh retries it.
func TestRefreshIngestFailureCarriesForwardRepo(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	uniqueB := "package b\nconst ONLY_IN_B = 7\n"
	commitGitRepo(t, repoA, map[string]string{"a.go": "package a\nconst A = 1\n"})
	commitGitRepo(t, repoB, map[string]string{"b.go": uniqueB})

	casDir := t.TempDir()
	// Build with the REAL ingest so the store holds both repos' blobs.
	m, err := buildCAS(corpus, casDir, ingest.DiscoverRepos, ingest.Repo, ingest.Head)
	if err != nil {
		t.Fatalf("buildCAS: %v", err)
	}
	rbOld, _ := m.RepoOf(repoB)
	if len(rbOld.Files) == 0 {
		t.Fatal("repoB should have files in the initial manifest")
	}

	// Make repoB "changed" (so refresh tries to re-ingest it) by committing a new
	// file (bumps HEAD), then inject a re-ingest failure for repoB specifically.
	// repoB is STILL ON DISK — discover returns it — so this is a transient
	// failure, not a removal.
	writeFiles(t, repoB, map[string]string{"c.go": "package b\nconst C = 9\n"})
	gitCommitAll(t, repoB, "add c.go")

	failingIngest := func(repoName, dir string) ([]ingest.File, error) {
		if dir == repoB {
			return nil, fmt.Errorf("injected transient git failure for %s", dir)
		}
		return ingest.Repo(repoName, dir)
	}

	m2, ds, err := refreshCAS(m, corpus, casDir, ingest.DiscoverRepos, failingIngest, ingest.Head)
	if err != nil {
		t.Fatalf("refreshCAS: %v", err)
	}

	// repoB must NOT be classified as removed, and MUST be surfaced as failed.
	for _, r := range ds.RemovedRepos {
		if r == repoB {
			t.Error("BUG 2: still-present repoB was misclassified as removed on transient ingest failure")
		}
	}
	if !contains(ds.FailedRepos, repoB) {
		t.Errorf("repoB should be in FailedRepos, got %v", ds.FailedRepos)
	}

	// repoB's manifest entry must SURVIVE unchanged (carried forward).
	rbNew, ok := m2.RepoOf(repoB)
	if !ok {
		t.Fatal("BUG 2: repoB dropped from manifest after a transient re-ingest failure (data loss)")
	}
	if !equalStrs(rbNew.SortedBlobs(), rbOld.SortedBlobs()) {
		t.Errorf("repoB blob set changed despite carry-forward: old=%v new=%v",
			rbOld.SortedBlobs(), rbNew.SortedBlobs())
	}
	if rbNew.Head != rbOld.Head {
		t.Errorf("repoB HEAD changed despite carry-forward: %q -> %q", rbOld.Head, rbNew.Head)
	}

	// End-to-end: repoB's unique content is STILL findable after export.
	shardDir := filepath.Join(t.TempDir(), "shards")
	if _, err := ExportShardDir(casDir, shardDir, 1<<30); err != nil {
		t.Fatalf("ExportShardDir: %v", err)
	}
	if !shardDirFinds(t, shardDir, "ONLY_IN_B") {
		t.Error("BUG 2: repoB's content vanished from exported shards after a transient refresh failure")
	}

	// A subsequent refresh with ingest WORKING must recover repoB cleanly (retry).
	m3, ds3, err := refreshCAS(m2, corpus, casDir, ingest.DiscoverRepos, ingest.Repo, ingest.Head)
	if err != nil {
		t.Fatalf("recovery refreshCAS: %v", err)
	}
	if contains(ds3.FailedRepos, repoB) {
		t.Errorf("repoB still failing after ingest recovered: %v", ds3.FailedRepos)
	}
	rbRecovered, _ := m3.RepoOf(repoB)
	if len(rbRecovered.Files) < 2 {
		t.Errorf("after recovery repoB should have b.go+c.go, got %d files", len(rbRecovered.Files))
	}
}

// --- BUG 2b regression: transient discovery MISS must not drop a repo ------

// TestRefreshDiscoveryMissCarriesForwardStillPresentRepo is the regression for
// the discovery-miss data-loss path (same class as BUG 2, different trigger): an
// old-manifest repo that the current discovery walk did NOT return — but whose
// .git is still on disk — must NOT be classified as removed and dropped. A
// DiscoverRepos walk can transiently skip a repo (an unreadable parent dir is
// SkipDir'd mid-walk, a racing rename, an I/O hiccup); treating that as removal
// would erase all of the repo's searchable content from subsequent exports. Such
// a repo must be carried forward unchanged and surfaced as failed; ONLY a repo
// whose .git is genuinely gone is a real removal.
func TestRefreshDiscoveryMissCarriesForwardStillPresentRepo(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	repoGone := filepath.Join(corpus, "repoGone")
	uniqueB := "package b\nconst ONLY_IN_B = 7\n"
	uniqueGone := "package g\nconst ONLY_IN_GONE = 9\n"
	commitGitRepo(t, repoA, map[string]string{"a.go": "package a\nconst A = 1\n"})
	commitGitRepo(t, repoB, map[string]string{"b.go": uniqueB})
	commitGitRepo(t, repoGone, map[string]string{"g.go": uniqueGone})

	casDir := t.TempDir()
	m, err := buildCAS(corpus, casDir, ingest.DiscoverRepos, ingest.Repo, ingest.Head)
	if err != nil {
		t.Fatalf("buildCAS: %v", err)
	}
	rbBOld, _ := m.RepoOf(repoB)
	if len(rbBOld.Files) == 0 {
		t.Fatal("repoB should have files in the initial manifest (test premise)")
	}

	// repoGone is genuinely deleted from disk (a real removal). repoB is STILL on
	// disk but the injected discovery returns ONLY repoA — simulating a transient
	// miss of repoB AND not returning repoGone either. So both repoB and repoGone
	// are "absent from the discovered set", but only repoGone's .git is gone.
	if err := os.RemoveAll(repoGone); err != nil {
		t.Fatal(err)
	}
	missingDiscover := func(string) ([]string, error) { return []string{repoA}, nil }

	m2, ds, err := refreshCAS(m, corpus, casDir, missingDiscover, ingest.Repo, ingest.Head)
	if err != nil {
		t.Fatalf("refreshCAS: %v", err)
	}

	// repoB (still on disk, missed by discovery) must be carried forward, NOT removed.
	if contains(ds.RemovedRepos, repoB) {
		t.Error("BUG 2b: still-present repoB (discovery miss) was misclassified as removed")
	}
	if !contains(ds.FailedRepos, repoB) {
		t.Errorf("repoB should be surfaced in FailedRepos, got %v", ds.FailedRepos)
	}
	rbBNew, ok := m2.RepoOf(repoB)
	if !ok {
		t.Fatal("BUG 2b: repoB dropped from manifest on a transient discovery miss (data loss)")
	}
	if !equalStrs(rbBNew.SortedBlobs(), rbBOld.SortedBlobs()) {
		t.Errorf("repoB blob set changed despite carry-forward: old=%v new=%v",
			rbBOld.SortedBlobs(), rbBNew.SortedBlobs())
	}
	if rbBNew.Head != rbBOld.Head {
		t.Errorf("repoB HEAD changed despite carry-forward: %q -> %q", rbBOld.Head, rbBNew.Head)
	}

	// repoGone (genuinely deleted) MUST still be correctly removed, NOT carried.
	if !contains(ds.RemovedRepos, repoGone) {
		t.Errorf("repoGone (.git deleted) should be removed, got RemovedRepos=%v", ds.RemovedRepos)
	}
	if contains(ds.FailedRepos, repoGone) {
		t.Errorf("repoGone is genuinely gone and must not be carried forward as failed: %v", ds.FailedRepos)
	}
	if _, ok := m2.RepoOf(repoGone); ok {
		t.Error("repoGone should be dropped from the manifest (genuine removal)")
	}

	// End-to-end: repoB's unique content STILL findable after export; repoGone's
	// content is no longer referenced (it was genuinely removed).
	shardDir := filepath.Join(t.TempDir(), "shards")
	if _, err := ExportShardDir(casDir, shardDir, 1<<30); err != nil {
		t.Fatalf("ExportShardDir: %v", err)
	}
	if !shardDirFinds(t, shardDir, "ONLY_IN_B") {
		t.Error("BUG 2b: repoB's content vanished from exported shards after a discovery miss")
	}

	// A subsequent refresh with discovery WORKING recovers repoB cleanly (retry).
	m3, ds3, err := refreshCAS(m2, corpus, casDir, ingest.DiscoverRepos, ingest.Repo, ingest.Head)
	if err != nil {
		t.Fatalf("recovery refreshCAS: %v", err)
	}
	if contains(ds3.FailedRepos, repoB) {
		t.Errorf("repoB still failing after discovery recovered: %v", ds3.FailedRepos)
	}
	if _, ok := m3.RepoOf(repoB); !ok {
		t.Error("repoB missing from manifest after recovery refresh")
	}
}

// TestRefreshAmbiguousStatErrorCarriesForwardRepo hardens the removal path: a
// repo absent from the discovered set whose .git STAT returns a NON-ENOENT error
// (EACCES, EIO, a racing parent-dir failure — i.e. "couldn't tell whether it's
// gone") must NOT be classified as removed. Collapsing every stat error into
// "gone" would drop a still-present repo's content on a transient FS hiccup —
// the same silent-data-loss class, one level deeper. Only a definitive ENOENT
// removes; any ambiguous outcome carries the entry forward (recorded failed).
//
// The stat is injected via the statFn seam so the test does not depend on real
// (and unreliable) FS error conditions.
func TestRefreshAmbiguousStatErrorCarriesForwardRepo(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	uniqueB := "package b\nconst ONLY_IN_B = 7\n"
	commitGitRepo(t, repoA, map[string]string{"a.go": "package a\nconst A = 1\n"})
	commitGitRepo(t, repoB, map[string]string{"b.go": uniqueB})

	casDir := t.TempDir()
	m, err := buildCAS(corpus, casDir, ingest.DiscoverRepos, ingest.Repo, ingest.Head)
	if err != nil {
		t.Fatalf("buildCAS: %v", err)
	}
	rbBOld, _ := m.RepoOf(repoB)
	if len(rbBOld.Files) == 0 {
		t.Fatal("repoB should have files in the initial manifest (test premise)")
	}

	// Discovery omits repoB (a transient miss). repoB is STILL on disk, but inject
	// a NON-ENOENT stat error for its .git so the refresh "cannot tell" if it is
	// gone. A correct classifier must NOT remove it.
	missingDiscover := func(string) ([]string, error) { return []string{repoA}, nil }
	bGit := filepath.Join(repoB, ".git")
	orig := statFn
	t.Cleanup(func() { statFn = orig })
	statFn = func(name string) (os.FileInfo, error) {
		if name == bGit {
			// A non-IsNotExist error: the "couldn't tell" class (mimics EACCES/EIO).
			return nil, fmt.Errorf("injected permission/IO error stat-ing %s", name)
		}
		return orig(name)
	}
	// Sanity: the injected error must NOT satisfy os.IsNotExist (else the test
	// would not be exercising the ambiguous branch).
	if _, e := statFn(bGit); os.IsNotExist(e) {
		t.Fatal("test premise: injected stat error must NOT be IsNotExist")
	}

	m2, ds, err := refreshCAS(m, corpus, casDir, missingDiscover, ingest.Repo, ingest.Head)
	if err != nil {
		t.Fatalf("refreshCAS: %v", err)
	}

	// repoB (ambiguous stat) must be carried forward, NOT removed.
	if contains(ds.RemovedRepos, repoB) {
		t.Error("HARDENING: repoB with an ambiguous (non-ENOENT) stat error was misclassified as removed")
	}
	if !contains(ds.FailedRepos, repoB) {
		t.Errorf("repoB should be surfaced in FailedRepos, got %v", ds.FailedRepos)
	}
	rbBNew, ok := m2.RepoOf(repoB)
	if !ok {
		t.Fatal("HARDENING: repoB dropped from manifest on a non-ENOENT stat error (data loss)")
	}
	if !equalStrs(rbBNew.SortedBlobs(), rbBOld.SortedBlobs()) {
		t.Errorf("repoB blob set changed despite carry-forward: old=%v new=%v",
			rbBOld.SortedBlobs(), rbBNew.SortedBlobs())
	}

	// End-to-end: repoB's unique content STILL findable after export.
	shardDir := filepath.Join(t.TempDir(), "shards")
	if _, err := ExportShardDir(casDir, shardDir, 1<<30); err != nil {
		t.Fatalf("ExportShardDir: %v", err)
	}
	if !shardDirFinds(t, shardDir, "ONLY_IN_B") {
		t.Error("HARDENING: repoB's content vanished from exported shards after an ambiguous stat error")
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

// --- BUG 1 regression: dirty-file SHA skew (content-true CAS) -------------

// TestDirtyFileContentIsStoredNotSkipped is the regression for the dirty-file SHA
// skew. ingest reads WORKING-TREE bytes but f.SHA is git's COMMITTED/index blob
// SHA; for a file whose working tree differs from its committed blob the two
// disagree. If the CAS keyed by f.SHA, a dirty file whose committed SHA is already
// stored (by a sibling repo carrying that exact committed content) would be
// dedup-skipped — its real working-tree bytes never stored, unfindable after
// export. The CAS must be CONTENT-TRUE: a Put only dedups when the stored bytes
// equal the new bytes.
//
// Construction: repoA commits content X (so the store will hold blob(X) keyed by
// X's git SHA). repoB commits the SAME content X (so `git ls-files -s` reports
// the SAME committed SHA), then its working tree is mutated to a DIFFERENT content
// Y WITHOUT committing — so ingest reads Y while git still reports X's SHA. With a
// SHA-keyed store the Put(SHA_X, Y) is a dedup no-op and Y is lost. With the
// content-true key Y is stored and findable.
func TestDirtyFileContentIsStoredNotSkipped(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")

	committed := "package shared\nconst COMMITTED_NEEDLE = 1\n"
	commitGitRepo(t, repoA, map[string]string{"shared.go": committed})
	commitGitRepo(t, repoB, map[string]string{"shared.go": committed})

	// Confirm the test premise: both repos' shared.go has the SAME committed SHA.
	shaA := lsFilesSHA(t, repoA, "shared.go")
	shaB := lsFilesSHA(t, repoB, "shared.go")
	if shaA == "" || shaA != shaB {
		t.Fatalf("test premise: committed SHAs must match, got %q vs %q", shaA, shaB)
	}

	// Make repoB's working tree DIRTY: replace the bytes WITHOUT committing, so
	// `git ls-files -s` still reports the old committed SHA but ingest reads Y.
	dirty := "package shared\nconst DIRTY_NEEDLE = 2\n"
	writeFiles(t, repoB, map[string]string{"shared.go": dirty})
	// Sanity: git still reports the OLD committed SHA for the dirty file.
	if got := lsFilesSHA(t, repoB, "shared.go"); got != shaB {
		t.Fatalf("test premise: dirty file should still report committed SHA %q, got %q", shaB, got)
	}
	// Sanity: ingest reads the DIRTY working-tree bytes.
	if !ingestReads(t, repoB, "shared.go", dirty) {
		t.Fatal("test premise: ingest must read the dirty working-tree bytes")
	}

	casDir := t.TempDir()
	m, err := BuildCAS(corpus, casDir)
	if err != nil {
		t.Fatalf("BuildCAS: %v", err)
	}

	// The dirty bytes (Y) must be physically stored and findable in the CAS — the
	// committed bytes (X) from repoA are stored too; they are distinct content.
	s := openStore(t, casDir)
	defer s.Close()
	if !storeHoldsContent(t, s, dirty) {
		t.Error("BUG 1: dirty working-tree content was dedup-skipped and is NOT in the CAS")
	}
	if !storeHoldsContent(t, s, committed) {
		t.Error("committed content (repoA) missing from CAS")
	}

	// repoB's manifest entry must key its dirty file by the CONTENT of Y, not X's
	// stale committed SHA, so Get returns the dirty bytes.
	rbBKey := shaInRepo(t, m, repoB, "shared.go")
	gotB, err := s.Get(rbBKey)
	if err != nil {
		t.Fatalf("Get(repoB shared.go key): %v", err)
	}
	if string(gotB) != dirty {
		t.Errorf("repoB shared.go content = %q, want dirty %q", gotB, dirty)
	}

	// End-to-end: after export, the dirty needle is searchable in the served shards.
	shardDir := filepath.Join(t.TempDir(), "shards")
	if _, err := ExportShardDir(casDir, shardDir, 1<<30); err != nil {
		t.Fatalf("ExportShardDir: %v", err)
	}
	if !shardDirFinds(t, shardDir, "DIRTY_NEEDLE") {
		t.Error("BUG 1: exported shards cannot find the dirty file's content (under-approximation)")
	}
	if !shardDirFinds(t, shardDir, "COMMITTED_NEEDLE") {
		t.Error("exported shards missing the committed content")
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

// lsFilesSHA returns the committed/index blob SHA `git ls-files -s` reports for a
// repo-relative path (the value ingest takes as File.SHA), or "" if absent.
func lsFilesSHA(t *testing.T, repoDir, rel string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repoDir, "ls-files", "-s", "--", rel)
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files -s %s: %v", rel, err)
	}
	// "<mode> <sha> <stage>\t<path>"
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return ""
	}
	return fields[1]
}

// ingestReads reports whether ingest.Repo reads `want` as the content of rel in
// the repo at dir (i.e. the working-tree bytes, confirming the dirty-file premise).
func ingestReads(t *testing.T, dir, rel, want string) bool {
	t.Helper()
	files, err := ingest.Repo(filepath.Base(dir), dir)
	if err != nil {
		t.Fatalf("ingest.Repo(%s): %v", dir, err)
	}
	for _, f := range files {
		if f.RelPath == rel {
			return string(f.Content) == want
		}
	}
	return false
}

// storeHoldsContent reports whether any blob physically stored in the CAS has the
// given content (independent of how it is keyed) — the content-true check.
func storeHoldsContent(t *testing.T, s *Store, content string) bool {
	t.Helper()
	for _, sha := range s.SHAs() {
		got, err := s.Get(sha)
		if err != nil {
			t.Fatalf("Get(%s): %v", sha, err)
		}
		if string(got) == content {
			return true
		}
	}
	return false
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
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
