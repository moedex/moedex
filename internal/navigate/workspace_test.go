package navigate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/corpus/catalog"
)

func TestWorkspaceManagerIsolatesManagedProjectAtLockedCommit(t *testing.T) {
	root, repo, commit := newManagedWorkspaceFixture(t)
	cache := filepath.Join(t.TempDir(), "cache")
	manager := newWorkspaceManager(cache)
	canonicalRoot := filepath.Join(repo, "src")

	scratchRoot, mapping, err := manager.isolate(context.Background(), canonicalRoot)
	if err != nil {
		t.Fatalf("isolate: %v", err)
	}
	if scratchRoot == canonicalRoot || !mapping.enabled() {
		t.Fatalf("managed root was not isolated: root=%q mapping=%+v", scratchRoot, mapping)
	}
	if !strings.HasPrefix(scratchRoot, cache+string(filepath.Separator)) {
		t.Fatalf("scratch root %q is outside cache %q", scratchRoot, cache)
	}

	canonicalFile := filepath.Join(canonicalRoot, "main.go")
	scratchFile, err := mapping.toScratch(canonicalFile)
	if err != nil {
		t.Fatalf("map canonical file: %v", err)
	}
	data, err := os.ReadFile(scratchFile)
	if err != nil {
		t.Fatalf("read isolated tracked file: %v", err)
	}
	if string(data) != "package fixture\n" {
		t.Fatalf("isolated content = %q", data)
	}
	if got := mapping.toCanonical(scratchFile); got != canonicalFile {
		t.Fatalf("reverse mapping = %q, want %q", got, canonicalFile)
	}

	// Untracked acquisition-tree pollution is excluded because projection is
	// sourced from git archive at the locked commit, not a recursive copy.
	if _, err := os.Stat(filepath.Join(mapping.scratchRepo, "obj", "generated.cs")); !os.IsNotExist(err) {
		t.Fatalf("untracked canonical file entered isolated tree: %v", err)
	}

	// A tool may freely mutate its disposable projection without touching the
	// canonical managed checkout.
	generated := filepath.Join(mapping.scratchRepo, "obj", "lsp-generated.cs")
	if err := os.MkdirAll(filepath.Dir(generated), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(generated, []byte("generated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "obj", "lsp-generated.cs")); !os.IsNotExist(err) {
		t.Fatalf("scratch mutation reached canonical corpus: %v", err)
	}

	// A second route for the same project/commit reuses the verified projection.
	again, againMap, err := manager.isolate(context.Background(), canonicalRoot)
	if err != nil {
		t.Fatalf("isolate cached: %v", err)
	}
	if again != scratchRoot || againMap.scratchRepo != mapping.scratchRepo {
		t.Fatalf("cache not reused: first=%q second=%q", scratchRoot, again)
	}
	if _, err := os.Stat(generated); err != nil {
		t.Fatalf("cached projection did not persist tool output: %v", err)
	}

	_, managed, err := findManagedWorkspaceProject(root)
	if err == nil || !managed {
		t.Fatalf("managed metadata root should not be routable as a project: managed=%v err=%v", managed, err)
	}
	_ = commit
}

func TestWorkspaceManagerLeavesUnmanagedRootAlone(t *testing.T) {
	root := t.TempDir()
	manager := newWorkspaceManager(filepath.Join(t.TempDir(), "cache"))
	got, mapping, err := manager.isolate(context.Background(), root)
	if err != nil {
		t.Fatalf("isolate unmanaged: %v", err)
	}
	if got != root || mapping.enabled() {
		t.Fatalf("unmanaged root changed: root=%q mapping=%+v", got, mapping)
	}
}

func TestWorkspacePathMapRejectsCanonicalEscape(t *testing.T) {
	base := t.TempDir()
	mapping := workspacePathMap{
		canonicalRepo: filepath.Join(base, "canonical"),
		scratchRepo:   filepath.Join(base, "scratch"),
	}
	if _, err := mapping.toScratch(filepath.Join(base, "other", "file.go")); err == nil {
		t.Fatal("expected path outside canonical repository to fail closed")
	}
	external := filepath.Join(base, "sdk", "metadata.go")
	if got := mapping.toCanonical(external); got != external {
		t.Fatalf("external result path rewritten: %q", got)
	}
}

func newManagedWorkspaceFixture(t *testing.T) (root, repo, commit string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "managed")
	repo = filepath.Join(root, "group", "project")
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "main.go"), []byte("package fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runWorkspaceGit(t, repo, "init", "-q")
	runWorkspaceGit(t, repo, "config", "user.name", "Moe Test")
	runWorkspaceGit(t, repo, "config", "user.email", "moe-test@localhost")
	runWorkspaceGit(t, repo, "add", "src/main.go")
	runWorkspaceGit(t, repo, "commit", "-q", "-m", "fixture")
	commit = strings.TrimSpace(runWorkspaceGit(t, repo, "rev-parse", "HEAD"))

	if err := os.MkdirAll(filepath.Join(repo, "obj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "obj", "generated.cs"), []byte("untracked"), 0o644); err != nil {
		t.Fatal(err)
	}

	cat, err := catalog.NewCatalog(catalog.DefaultHost, []string{"group"})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.WriteCatalog(root, cat); err != nil {
		t.Fatal(err)
	}
	lock, err := catalog.NewLock(catalog.DefaultHost, true, []catalog.LockedProject{{
		ID:                101,
		PathWithNamespace: "group/project",
		CloneURL:          "git@gitlab.example.com:group/project.git",
		DefaultBranch:     "main",
		DefaultCommit:     commit,
		Status:            catalog.LockStatusCurrent,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.WriteLock(root, catalog.DefaultHost, lock); err != nil {
		t.Fatal(err)
	}
	return root, repo, commit
}

func runWorkspaceGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"-C", dir}, args...)
	output, err := exec.Command("git", cmdArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(cmdArgs, " "), err, output)
	}
	return string(output)
}
