package parity

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/ingest"
)

// commitGitRepo creates a git repo with files staged AND committed, so
// `git rev-parse HEAD` resolves (the freshness key). Returns the repo dir.
func commitGitRepo(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	writeFiles(t, dir, files)
	run("add", "-A")
	run("commit", "-q", "-m", "init")
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

func gitCommitAll(t *testing.T, dir, msg string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "add", "-A")
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "-C", dir, "commit", "-q", "-m", msg)
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

func gitEnv() []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
}

// shardFinds reports whether any shard under shardDir contains a blob whose
// content contains needle. It loads each shard and scans blob content directly
// (independent of the query layer, which is owned by another lane).
func shardFinds(t *testing.T, shardDir, needle string) bool {
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
			if contains2(string(ix.Blob(uint64(id)).Content), needle) {
				return true
			}
		}
	}
	return false
}

func contains2(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestManifestRoundTrip checks Build emits a manifest, and Write/LoadManifest
// preserve it.
func TestManifestRoundTrip(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	commitGitRepo(t, repoA, map[string]string{"a.go": "package a\nfunc Alpha() {}\n"})
	commitGitRepo(t, repoB, map[string]string{"b.go": "package b\nfunc Bravo() {}\n"})

	work := t.TempDir()
	b, err := Build(Config{Root: corpus, WorkDir: work, Seed: 1, ShardBytes: 1 << 30})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	mpath := filepath.Join(work, "shards", ManifestName)
	m, err := LoadManifest(mpath)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Version != ManifestVersion {
		t.Errorf("version = %d, want %d", m.Version, ManifestVersion)
	}
	if len(m.Heads) != 2 {
		t.Fatalf("heads = %d, want 2", len(m.Heads))
	}
	for _, h := range m.Heads {
		if len(h.Head) != 40 {
			t.Errorf("repo %s HEAD = %q, want 40-char sha", h.Dir, h.Head)
		}
	}
	if len(m.Shards) != len(b.Shards) {
		t.Errorf("manifest shards = %d, built shards = %d", len(m.Shards), len(b.Shards))
	}
	// Every ingested repo must appear in some shard's repo set.
	seen := map[string]bool{}
	for _, sm := range m.Shards {
		for _, r := range sm.Repos {
			seen[r] = true
		}
	}
	if !seen[repoA] || !seen[repoB] {
		t.Errorf("not all repos attributed to shards: %v", seen)
	}

	// Round-trip identity.
	tmp := filepath.Join(t.TempDir(), "m.json")
	now := time.Now().Truncate(time.Second)
	m.BuiltAt = now
	if err := WriteManifest(tmp, m); err != nil {
		t.Fatal(err)
	}
	m2, err := LoadManifest(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if !m2.BuiltAt.Equal(now) || len(m2.Heads) != len(m.Heads) || len(m2.Shards) != len(m.Shards) {
		t.Errorf("round-trip mismatch: %+v", m2)
	}
}

// TestDetectChanges classifies changed/added/removed repos correctly.
func TestDetectChanges(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	commitGitRepo(t, repoA, map[string]string{"a.go": "package a\n"})
	commitGitRepo(t, repoB, map[string]string{"b.go": "package b\n"})

	work := t.TempDir()
	if _, err := Build(Config{Root: corpus, WorkDir: work, Seed: 1, ShardBytes: 1 << 30}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	m, err := LoadManifest(filepath.Join(work, "shards", ManifestName))
	if err != nil {
		t.Fatal(err)
	}

	// No changes yet.
	ch, err := DetectChanges(m, corpus, ingest.DiscoverRepos, ingest.Head)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Any() {
		t.Errorf("expected no changes, got %+v", ch)
	}

	// Mutate repoB (new commit -> new HEAD), add repoC, remove repoA.
	writeFiles(t, repoB, map[string]string{"b.go": "package b\nfunc New() {}\n"})
	gitCommitAll(t, repoB, "change")
	repoC := filepath.Join(corpus, "repoC")
	commitGitRepo(t, repoC, map[string]string{"c.go": "package c\n"})
	if err := os.RemoveAll(repoA); err != nil {
		t.Fatal(err)
	}

	ch, err = DetectChanges(m, corpus, ingest.DiscoverRepos, ingest.Head)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Changed) != 1 || ch.Changed[0] != repoB {
		t.Errorf("Changed = %v, want [%s]", ch.Changed, repoB)
	}
	if len(ch.Added) != 1 || ch.Added[0] != repoC {
		t.Errorf("Added = %v, want [%s]", ch.Added, repoC)
	}
	if len(ch.Removed) != 1 || ch.Removed[0] != repoA {
		t.Errorf("Removed = %v, want [%s]", ch.Removed, repoA)
	}
}

// TestDetectChangesCommitlessRepoStable guards a freshness regression: a repo
// with no commits (unborn HEAD, e.g. an empty placeholder repo in the corpus)
// must NOT be reported as "changed" on every check. ingest.Head records "" for
// such a repo at build time; DetectChanges must compare an unreadable current
// HEAD against that recorded "" and find them equal, rather than conservatively
// flagging it changed forever (which would make the steady-state refresh cron
// rebuild needlessly every cycle).
func TestDetectChangesCommitlessRepoStable(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	repoA := filepath.Join(corpus, "repoA")
	commitGitRepo(t, repoA, map[string]string{"a.go": "package a\n"})

	// A commitless repo: `git init` only, no commit -> `git rev-parse HEAD` errors.
	repoEmpty := filepath.Join(corpus, "repoEmpty")
	if err := os.MkdirAll(repoEmpty, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", repoEmpty, "init", "-q")
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if _, err := ingest.Head(repoEmpty); err == nil {
		t.Fatal("expected ingest.Head to error on a commitless repo (test premise)")
	}

	work := t.TempDir()
	if _, err := Build(Config{Root: corpus, WorkDir: work, Seed: 1, ShardBytes: 1 << 30}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	m, err := LoadManifest(filepath.Join(work, "shards", ManifestName))
	if err != nil {
		t.Fatal(err)
	}

	// Right after build, and again on a second check, nothing has changed.
	for i := 0; i < 2; i++ {
		ch, err := DetectChanges(m, corpus, ingest.DiscoverRepos, ingest.Head)
		if err != nil {
			t.Fatal(err)
		}
		if ch.Any() {
			t.Errorf("check #%d: expected no changes for a commitless repo, got %+v", i+1, ch)
		}
	}
}

// TestRebuildOnlyAffectedShard mutates one repo, rebuilds, and asserts the
// unchanged shard is carried forward byte-for-byte while the changed repo's new
// content becomes findable.
func TestRebuildOnlyAffectedShard(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	// Each repo's content exceeds ShardBytes so they land in separate shards.
	commitGitRepo(t, repoA, map[string]string{"a.go": "package a\nconst MarkerAAA = 1\n"})
	commitGitRepo(t, repoB, map[string]string{"b.go": "package b\nconst MarkerBBB = 1\n"})

	work := t.TempDir()
	if _, err := Build(Config{Root: corpus, WorkDir: work, Seed: 1, ShardBytes: 10}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	oldShardDir := filepath.Join(work, "shards")
	old, err := LoadManifest(filepath.Join(oldShardDir, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if len(old.Shards) < 2 {
		t.Fatalf("expected >=2 shards (one per repo), got %d", len(old.Shards))
	}

	// Capture the shard file that holds ONLY repoA (the unaffected one).
	var repoAShard string
	for _, sm := range old.Shards {
		if len(sm.Repos) == 1 && sm.Repos[0] == repoA {
			repoAShard = sm.Path
		}
	}
	if repoAShard == "" {
		t.Fatal("could not find repoA-only shard")
	}
	repoABytesBefore, err := os.ReadFile(repoAShard)
	if err != nil {
		t.Fatal(err)
	}

	// Mutate repoB.
	writeFiles(t, repoB, map[string]string{"b.go": "package b\nconst MarkerBBB = 2\nfunc FreshBBB() {}\n"})
	gitCommitAll(t, repoB, "change B")

	ch, err := DetectChanges(old, corpus, ingest.DiscoverRepos, ingest.Head)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Changed) != 1 || ch.Changed[0] != repoB {
		t.Fatalf("Changed = %v, want [repoB]", ch.Changed)
	}

	newShardDir := filepath.Join(work, "shards2")
	nm, err := Rebuild(old, ch, newShardDir, time.Now(), ingest.Head)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// The carried-forward repoA shard must be byte-identical to before.
	var carriedA string
	for _, sm := range nm.Shards {
		if len(sm.Repos) == 1 && sm.Repos[0] == repoA {
			carriedA = sm.Path
		}
	}
	if carriedA == "" {
		t.Fatal("repoA shard not present in rebuilt manifest")
	}
	repoABytesAfter, err := os.ReadFile(carriedA)
	if err != nil {
		t.Fatal(err)
	}
	if string(repoABytesBefore) != string(repoABytesAfter) {
		t.Error("repoA shard was rebuilt; expected byte-for-byte carry-forward")
	}

	// The new repoB content must be findable in the rebuilt shards.
	if !shardFinds(t, newShardDir, "FreshBBB") {
		t.Error("rebuilt index does not contain repoB's new content (FreshBBB)")
	}
	// And repoA's content must still be present (carried forward).
	if !shardFinds(t, newShardDir, "MarkerAAA") {
		t.Error("rebuilt index lost repoA content (MarkerAAA)")
	}
	// repoB's HEAD must now be updated in the new manifest.
	bHead, _ := nm.HeadOf(repoB)
	oldBHead, _ := old.HeadOf(repoB)
	if bHead == oldBHead || bHead == "" {
		t.Errorf("repoB HEAD not updated: old=%q new=%q", oldBHead, bHead)
	}
}

func TestDetectChangesAIPrivacyWithoutHEADChange(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	repo := filepath.Join(corpus, "repo")
	commitGitRepo(t, repo, map[string]string{"content.txt": "PrivacyFreshnessMarker\n"})

	work := t.TempDir()
	if _, err := Build(Config{Root: corpus, WorkDir: work, Seed: 1, ShardBytes: 1 << 30}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	manifest, err := LoadManifest(filepath.Join(work, "shards", ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	headBefore, err := ingest.Head(repo)
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, repo, map[string]string{ingest.AIPrivacyFileName: "global_privacy_level: 1\n"})
	headAfter, err := ingest.Head(repo)
	if err != nil {
		t.Fatal(err)
	}
	if headAfter != headBefore {
		t.Fatal("test premise: uncommitted privacy policy changed HEAD")
	}

	changes, err := DetectChanges(manifest, corpus, ingest.DiscoverRepos, ingest.Head)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Changed) != 1 || changes.Changed[0] != repo {
		t.Fatalf("Changed = %v, want privacy-only change for %s", changes.Changed, repo)
	}
}

func TestRebuildPreservesRestrictedHeadAndReindexesPolicyRelaxation(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	restricted := filepath.Join(corpus, "restricted")
	allowed := filepath.Join(corpus, "allowed")
	commitGitRepo(t, restricted, map[string]string{
		ingest.AIPrivacyFileName: "global_privacy_level: 1\n",
		"content.txt":            "PrivacyRelaxationMarker\n",
	})
	commitGitRepo(t, allowed, map[string]string{"allowed.txt": "AllowedRebuildMarker\n"})

	work := t.TempDir()
	if _, err := Build(Config{Root: corpus, WorkDir: work, Seed: 1, ShardBytes: 1 << 30}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	old, err := LoadManifest(filepath.Join(work, "shards", ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := old.HeadOf(restricted); !ok {
		t.Fatal("globally Restricted repository is missing from freshness manifest")
	}
	if shardFinds(t, filepath.Join(work, "shards"), "PrivacyRelaxationMarker") {
		t.Fatal("globally Restricted content was indexed")
	}

	writeFiles(t, allowed, map[string]string{"allowed.txt": "AllowedRebuildMarker changed\n"})
	gitCommitAll(t, allowed, "change allowed repo")
	changes, err := DetectChanges(old, corpus, ingest.DiscoverRepos, ingest.Head)
	if err != nil {
		t.Fatal(err)
	}
	intermediateDir := filepath.Join(work, "shards2")
	intermediate, err := Rebuild(old, changes, intermediateDir, time.Now(), ingest.Head)
	if err != nil {
		t.Fatalf("Rebuild unrelated change: %v", err)
	}
	if _, ok := intermediate.HeadOf(restricted); !ok {
		t.Fatal("unrelated rebuild dropped Restricted repository freshness identity")
	}
	steady, err := DetectChanges(intermediate, corpus, ingest.DiscoverRepos, ingest.Head)
	if err != nil {
		t.Fatal(err)
	}
	if steady.Any() {
		t.Fatalf("Restricted repository became a perpetual freshness change: %+v", steady)
	}

	writeFiles(t, restricted, map[string]string{ingest.AIPrivacyFileName: "global_privacy_level: 3\n"})
	relaxed, err := DetectChanges(intermediate, corpus, ingest.DiscoverRepos, ingest.Head)
	if err != nil {
		t.Fatal(err)
	}
	if len(relaxed.Changed) != 1 || relaxed.Changed[0] != restricted {
		t.Fatalf("Changed = %v, want relaxed Restricted repository", relaxed.Changed)
	}
	finalDir := filepath.Join(work, "shards3")
	if _, err := Rebuild(intermediate, relaxed, finalDir, time.Now(), ingest.Head); err != nil {
		t.Fatalf("Rebuild relaxed policy: %v", err)
	}
	if !shardFinds(t, finalDir, "PrivacyRelaxationMarker") {
		t.Fatal("policy relaxation did not re-index the previously Restricted repository")
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}
