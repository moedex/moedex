package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"moedex/internal/ingest"
	"moedex/internal/parity"
	"moedex/internal/server"
)

// TestBuildCheckRefresh exercises the full offline lifecycle over real temp git
// repos: build a servable shard dir, confirm check sees no drift, commit a change
// to one repo, confirm check detects exactly that repo, refresh, and confirm the
// swapped-in dir is servable and now contains the new content — and that a second
// refresh is a clean no-op. Skips when git is unavailable.
func TestBuildCheckRefresh(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	repoA := initRepo(t, filepath.Join(root, "repoA"), map[string]string{
		"alpha.go": "package a\n\nfunc AlphaUniqueToken() {}\n",
	})
	initRepo(t, filepath.Join(root, "repoB"), map[string]string{
		"beta.go": "package b\n\nfunc BetaThing() {}\n",
	})

	shardDir := filepath.Join(t.TempDir(), "shards")

	// --- build ---
	if err := runBuild([]string{"-corpus", root, "-shard-dir", shardDir}); err != nil {
		t.Fatalf("build: %v", err)
	}
	m, err := parity.LoadManifest(filepath.Join(shardDir, parity.ManifestName))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if len(m.Heads) != 2 {
		t.Fatalf("manifest heads = %d, want 2 (repoA, repoB)", len(m.Heads))
	}
	absRoot, _ := filepath.Abs(root)
	if m.Root != absRoot {
		t.Errorf("manifest root = %q, want %q", m.Root, absRoot)
	}
	// Servable: the daemon's Corpus opens it and finds the indexed content.
	assertServableContains(t, shardDir, "AlphaUniqueToken", 1)

	// --- check: no drift right after build ---
	ch := detect(t, shardDir)
	if ch.Any() {
		t.Fatalf("fresh build reports drift: %+v", ch)
	}

	// --- mutate repoA: add a new tracked file + commit (HEAD moves) ---
	writeFile(t, filepath.Join(repoA, "gamma.go"), "package a\n\nfunc GammaFreshToken() {}\n")
	gitCommit(t, repoA, "add gamma")

	ch = detect(t, shardDir)
	if len(ch.Changed) != 1 || filepath.Base(ch.Changed[0]) != "repoA" {
		t.Fatalf("check after mutation: Changed=%v Added=%v Removed=%v; want only repoA changed",
			ch.Changed, ch.Added, ch.Removed)
	}

	// --- refresh: rebuild the affected shard and swap it in ---
	if err := runRefresh([]string{"-shard-dir", shardDir}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	// The new file is now servable; repoA's pre-existing file survives the
	// rebuild; repoB survives (re-ingested as a co-resident of the affected
	// shard); the manifest records repoA's new HEAD.
	assertServableContains(t, shardDir, "GammaFreshToken", 1)
	assertServableContains(t, shardDir, "AlphaUniqueToken", 1)
	assertServableContains(t, shardDir, "BetaThing", 1)
	m2, err := parity.LoadManifest(filepath.Join(shardDir, parity.ManifestName))
	if err != nil {
		t.Fatalf("reload manifest: %v", err)
	}
	headNow, _ := ingest.Head(repoA)
	if got, _ := m2.HeadOf(mustAbs(t, repoA)); got != headNow {
		t.Errorf("manifest repoA head = %q, want current %q", got, headNow)
	}
	// Manifest paths must point at the live dir (not the temp rebuild dir), so a
	// later refresh can carry these shards forward.
	for _, sm := range m2.Shards {
		if filepath.Dir(sm.Path) != shardDir {
			t.Errorf("manifest shard path %q not under live dir %q", sm.Path, shardDir)
		}
		if _, err := os.Stat(sm.Path); err != nil {
			t.Errorf("manifest shard path %q does not exist: %v", sm.Path, err)
		}
	}

	// --- refresh again: clean no-op ---
	if ch := detect(t, shardDir); ch.Any() {
		t.Fatalf("post-refresh check still reports drift: %+v", ch)
	}
}

func detect(t *testing.T, shardDir string) parity.Changes {
	t.Helper()
	m, err := parity.LoadManifest(filepath.Join(shardDir, parity.ManifestName))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	ch, err := parity.DetectChanges(m, m.Root, ingest.DiscoverRepos, ingest.Head)
	if err != nil {
		t.Fatalf("detect changes: %v", err)
	}
	return ch
}

// assertServableContains opens the shard dir as the daemon would and asserts a
// literal query returns at least wantAtLeast matches.
func assertServableContains(t *testing.T, shardDir, literal string, wantAtLeast int) {
	t.Helper()
	c, err := server.Open(shardDir)
	if err != nil {
		t.Fatalf("server.Open(%s): %v", shardDir, err)
	}
	defer c.Close()
	matches, _ := c.Literal(literal)
	if len(matches) < wantAtLeast {
		t.Errorf("literal %q: %d matches, want >= %d", literal, len(matches), wantAtLeast)
	}
}

// --- git fixture helpers ---

func initRepo(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "test@moedex.local")
	git(t, dir, "config", "user.name", "moedex test")
	for name, content := range files {
		writeFile(t, filepath.Join(dir, name), content)
	}
	git(t, dir, "add", "-A")
	gitCommit(t, dir, "init")
	return dir
}

func gitCommit(t *testing.T, dir, msg string) {
	t.Helper()
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", msg)
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	// Deterministic, env-independent commits.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
