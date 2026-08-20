package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"moedex/internal/corpus"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/ingest"
	"moedex/internal/parity"
	"moedex/internal/server"
)

func TestRunGraphRebuildsMissingClusterSidecarFromExistingShards(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	initRepo(t, filepath.Join(root, "repo"), map[string]string{
		"main.go": "package repo\nfunc Root() { Target() }\nfunc Target() {}\n",
	})
	shardDir := filepath.Join(t.TempDir(), "shards")
	if err := runBuild([]string{"-corpus", root, "-shard-dir", shardDir}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(server.ClusterPath(shardDir)); err != nil {
		t.Fatal(err)
	}
	if err := runGraph([]string{"-shard-dir", shardDir}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(server.ClusterPath(shardDir)); err != nil {
		t.Fatalf("graph-only refresh did not regenerate cluster sidecar: %v", err)
	}
}

func TestManagedDefaultBuildUsesNamespaceIdentityAndExcludesSuperproject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := initRepo(t, filepath.Join(t.TempDir(), "managed"), map[string]string{
		"superproject-only.txt": "SuperprojectOnlyMarker\n",
	})
	first := initRepo(t, filepath.Join(root, "group-a", "same"), map[string]string{
		"same.txt": "ManagedLeafMarker\n",
	})
	second := initRepo(t, filepath.Join(root, "group-b", "same"), map[string]string{
		"same.txt": "ManagedLeafMarker\n",
	})
	writeManagedIndexLock(t, root, []struct {
		id        int64
		namespace string
		dir       string
	}{
		{id: 22, namespace: "group-b/same", dir: second},
		{id: 11, namespace: "group-a/same", dir: first},
	})

	shardDir := filepath.Join(t.TempDir(), "shards")
	if err := runBuild([]string{"-corpus", root, "-shard-dir", shardDir}); err != nil {
		t.Fatalf("managed build: %v", err)
	}
	m, err := parity.LoadManifest(filepath.Join(shardDir, parity.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Heads) != 2 || m.Heads[0].ProjectID != 11 || m.Heads[0].Label != "group-a/same" || !m.Heads[0].Managed {
		t.Fatalf("managed manifest identity = %+v", m.Heads)
	}

	c, err := server.Open(shardDir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	matches, _, err := c.Literal(context.Background(), "ManagedLeafMarker")
	if err != nil {
		t.Fatal(err)
	}
	repos := map[string]bool{}
	for _, match := range matches {
		repos[match.Repo] = true
	}
	if !repos["group-a/same"] || !repos["group-b/same"] || len(repos) != 2 {
		t.Fatalf("managed repo labels = %v, want both namespace-qualified leaves", repos)
	}
	superMatches, _, err := c.Literal(context.Background(), "SuperprojectOnlyMarker")
	if err != nil {
		t.Fatal(err)
	}
	if len(superMatches) != 0 {
		t.Fatalf("superproject content was indexed: %+v", superMatches)
	}
}

func writeManagedIndexLock(t *testing.T, root string, projects []struct {
	id        int64
	namespace string
	dir       string
}) {
	t.Helper()
	cfg := corpus.Config{Host: corpus.DefaultHost, Root: root, Groups: []string{"group-a", "group-b"}}
	catalog, err := corpus.NewCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := corpus.WriteCatalog(root, catalog); err != nil {
		t.Fatal(err)
	}
	locked := make([]corpus.LockedProject, 0, len(projects))
	for _, project := range projects {
		head, err := ingest.Head(project.dir)
		if err != nil {
			t.Fatal(err)
		}
		locked = append(locked, corpus.LockedProject{
			ID: project.id, PathWithNamespace: project.namespace,
			CloneURL:      "git@" + corpus.DefaultHost + ":" + project.namespace + ".git",
			DefaultBranch: "main", DefaultCommit: head, Status: corpus.LockStatusCurrent,
		})
	}
	lock, err := corpus.NewLock(corpus.DefaultHost, true, locked)
	if err != nil {
		t.Fatal(err)
	}
	if err := corpus.WriteLock(root, corpus.DefaultHost, lock); err != nil {
		t.Fatal(err)
	}
}

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

// TestMoedexIndexBuildProducesSidecars proves the offline indexer writes the
// token+symbol ranking sidecars plus the mmap graph. A fresh OpenRank over the
// built dir LOADS both ranking indexes from cache (no rebuild), while
// diskgraph.Open serves the offline graph. It also covers refresh: after a
// mutate+refresh, the live dir carries valid replacements for every artifact.
func TestMoedexIndexBuildProducesSidecars(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	repoA := initRepo(t, filepath.Join(root, "repoA"), map[string]string{
		"alpha.go": "package a\n\nfunc AlphaUniqueToken() {}\nfunc UsesAlpha() { AlphaUniqueToken() }\n",
	})
	shardDir := filepath.Join(t.TempDir(), "shards")

	// --- build writes the ranking files and graph ---
	if err := runBuild([]string{"-corpus", root, "-shard-dir", shardDir}); err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, name := range []string{"corpus-tokens.tki", "corpus-tokens.tki.meta", "corpus-symbols.sym", "corpus-symbols.sym.meta", server.GraphFileName} {
		if _, err := os.Stat(filepath.Join(shardDir, name)); err != nil {
			t.Fatalf("sidecar %s not written by build: %v", name, err)
		}
	}
	graph, err := diskgraph.Open(server.GraphPath(shardDir))
	if err != nil {
		t.Fatalf("open graph after build: %v", err)
	}
	if graph.NumEdges() == 0 {
		t.Error("build graph contains no edges for AlphaUniqueToken call")
	}
	if err := graph.Close(); err != nil {
		t.Fatalf("close graph after build: %v", err)
	}

	// A fresh daemon boot finds them warm (loads, does not rebuild).
	rc, err := server.OpenRank(context.Background(), shardDir, server.RankConfig{})
	if err != nil {
		t.Fatalf("OpenRank after build: %v", err)
	}
	if !rc.TokensFromCache() || !rc.SymbolsFromCache() {
		t.Errorf("daemon did not find warm sidecars: tokens=%v symbols=%v",
			rc.TokensFromCache(), rc.SymbolsFromCache())
	}

	// --- mutate + refresh rebuilds sidecars on the live dir ---
	writeFile(t, filepath.Join(repoA, "gamma.go"), "package a\n\nfunc GammaFreshToken() {}\n")
	gitCommit(t, repoA, "add gamma")
	if err := runRefresh([]string{"-shard-dir", shardDir}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	for _, name := range []string{"corpus-tokens.tki", "corpus-tokens.tki.meta", "corpus-symbols.sym", "corpus-symbols.sym.meta", server.GraphFileName} {
		if _, err := os.Stat(filepath.Join(shardDir, name)); err != nil {
			t.Fatalf("sidecar %s missing on live dir after refresh: %v", name, err)
		}
	}
	graph2, err := diskgraph.Open(server.GraphPath(shardDir))
	if err != nil {
		t.Fatalf("open graph after refresh: %v", err)
	}
	if graph2.NumEdges() == 0 {
		t.Error("refreshed graph lost existing AlphaUniqueToken call edge")
	}
	if got, want := graph2.Generation(), diskgraph.FirstGeneration+1; got != want {
		t.Errorf("graph generation after refresh = %d, want %d (prior graph not carried across the dir swap)", got, want)
	}
	var carried int
	graph2.EachEdge(func(_ diskgraph.Key, edge diskgraph.Edge) bool {
		if edge.Name == "AlphaUniqueToken" && edge.Generation == diskgraph.FirstGeneration {
			carried++
		}
		return true
	})
	if carried == 0 {
		t.Error("no AlphaUniqueToken edge kept its original generation; the refresh re-swept unaffected names")
	}
	if err := graph2.Close(); err != nil {
		t.Fatalf("close graph after refresh: %v", err)
	}
	rc2, err := server.OpenRank(context.Background(), shardDir, server.RankConfig{})
	if err != nil {
		t.Fatalf("OpenRank after refresh: %v", err)
	}
	if !rc2.TokensFromCache() || !rc2.SymbolsFromCache() {
		t.Errorf("post-refresh daemon did not find warm sidecars: tokens=%v symbols=%v",
			rc2.TokensFromCache(), rc2.SymbolsFromCache())
	}
}

// TestPrepareDirRejectsUnreadableNonEmptyDirWithoutForce guards F-059:
// prepareDir must not silently bypass the -force guard when os.ReadDir fails
// for a reason other than the dir not existing (e.g. permission denied). The
// old code only acted on err == nil, so a permission error fell through
// straight to os.MkdirAll, which is a silent no-op success for a dir that
// already exists — discarding the -force guard's intent for a dir whose
// contents could not even be inspected.
func TestPrepareDirRejectsUnreadableNonEmptyDirWithoutForce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not enforced the same way on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "shard-dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "child.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755) // let t.TempDir() clean up afterward

	if err := prepareDir(dir, false); err == nil {
		t.Fatal("prepareDir returned nil for an unreadable non-empty dir; the -force guard was silently bypassed")
	}
}

// TestPrepareDirCreatesMissingDir covers the legitimate not-exist path still
// works after distinguishing it from other ReadDir errors.
func TestPrepareDirCreatesMissingDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "fresh", "shard-dir")
	if err := prepareDir(dir, false); err != nil {
		t.Fatalf("prepareDir on missing dir: %v", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("prepareDir did not create %s: %v", dir, err)
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
	matches, _, err := c.Literal(context.Background(), literal)
	if err != nil {
		t.Fatalf("literal %q: %v", literal, err)
	}
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
