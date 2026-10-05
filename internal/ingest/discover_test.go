package ingest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/corpus"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v (%s)", dir, err, out)
	}
}

func TestDiscoverReposRecognizesGitFilesWithoutDescendingMetadata(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "worktree")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: /outside/metadata\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverRepos(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != repo {
		t.Fatalf("DiscoverRepos = %v, want [%s]", got, repo)
	}
	count, err := CountGitEntries(root)
	if err != nil || count != 1 {
		t.Fatalf("CountGitEntries = %d, %v; want 1", count, err)
	}
}

func TestDiscoverSourcesManagedUsesStableLockIdentity(t *testing.T) {
	root, commits := managedSourceFixture(t)
	sources, err := DiscoverSources(root)
	if err != nil {
		t.Fatalf("DiscoverSources: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want 2: %+v", len(sources), sources)
	}
	if sources[0].ProjectID != 10 || sources[0].Namespace != "group-a/same" || !sources[0].Managed {
		t.Fatalf("source[0] = %+v, want project 10 namespace identity", sources[0])
	}
	if sources[1].ProjectID != 20 || sources[1].Namespace != "group-b/same" || !sources[1].Managed {
		t.Fatalf("source[1] = %+v, want project 20 namespace identity", sources[1])
	}
	if sources[0].LockedCommit != commits[10] || sources[1].LockedCommit != commits[20] {
		t.Fatalf("locked commits not retained: %+v", sources)
	}
	for _, source := range sources {
		if source.Dir == root {
			t.Fatal("managed superproject was returned as an indexing source")
		}
	}

	// Advancing a checked-out managed source without advancing the lock must fail
	// before any caller can ingest mutable bytes.
	repo := filepath.Join(root, "group-a", "same")
	if err := os.WriteFile(filepath.Join(repo, "advanced.txt"), []byte("advanced\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "advanced.txt")
	runGit(t, repo, "-c", "user.name=Moedex Test", "-c", "user.email=test@localhost", "commit", "-m", "advance")
	if _, err := DiscoverSources(root); err == nil || !strings.Contains(err.Error(), "does not match locked default commit") {
		t.Fatalf("HEAD mismatch error = %v, want fail-closed mismatch", err)
	}
}

func TestDiscoverSourcesManagedMissingAndCorruptLockFailClosed(t *testing.T) {
	t.Run("missing locked submodule", func(t *testing.T) {
		root, _ := managedSourceFixture(t)
		missing := filepath.Join(root, "group-b", "same")
		if err := os.Rename(missing, missing+".away"); err != nil {
			t.Fatal(err)
		}
		if _, err := DiscoverSources(root); err == nil || !strings.Contains(err.Error(), "not materialized") {
			t.Fatalf("missing source error = %v", err)
		}
	})

	t.Run("corrupt lock", func(t *testing.T) {
		root, _ := managedSourceFixture(t)
		if err := os.WriteFile(corpus.LockPath(root), []byte(`{"version":1,"projects":`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := DiscoverSources(root); err == nil || !strings.Contains(err.Error(), "load managed corpus lock") {
			t.Fatalf("corrupt lock error = %v", err)
		}
	})
}

func TestDiscoverSourcesManagedConfiguredHost(t *testing.T) {
	root, _ := managedSourceFixture(t)
	const host = "gitlab.company.example"
	cat, err := corpus.LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := corpus.LoadLock(root, cat.Host)
	if err != nil {
		t.Fatal(err)
	}
	cat.Host = host
	if err := corpus.WriteCatalog(root, cat); err != nil {
		t.Fatal(err)
	}
	// The marker alone must not admit a lock acquired from another host.
	if _, err := DiscoverSources(root); err == nil || !strings.Contains(err.Error(), "does not match pinned host") {
		t.Fatalf("mismatched lock error = %v, want host rejection", err)
	}
	for i := range lock.Projects {
		lock.Projects[i].CloneURL = "git@" + host + ":" + lock.Projects[i].PathWithNamespace + ".git"
	}
	if err := corpus.WriteLock(root, host, lock); err != nil {
		t.Fatal(err)
	}
	sources, err := DiscoverSources(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || sources[0].Namespace != "group-a/same" || !sources[0].Managed {
		t.Fatalf("configured-host source identity = %+v", sources)
	}
}

func managedSourceFixture(t *testing.T) (string, map[int64]string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	projects := []struct {
		id   int64
		path string
	}{
		{id: 20, path: "group-b/same"},
		{id: 10, path: "group-a/same"},
	}
	commits := map[int64]string{}
	locked := make([]corpus.LockedProject, 0, len(projects))
	for _, project := range projects {
		dir := filepath.Join(root, filepath.FromSlash(project.path))
		gitInit(t, dir)
		if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte(project.path+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "add", "marker.txt")
		runGit(t, dir, "-c", "user.name=Moedex Test", "-c", "user.email=test@localhost", "commit", "-m", "fixture")
		head := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
		commits[project.id] = head
		locked = append(locked, corpus.LockedProject{
			ID: project.id, PathWithNamespace: project.path,
			CloneURL:      "git@" + corpus.DefaultHost + ":" + project.path + ".git",
			DefaultBranch: "main", DefaultCommit: head, Status: corpus.LockStatusCurrent,
		})
	}
	cfg := corpus.Config{Host: corpus.DefaultHost, Root: root, Groups: []string{"group-a", "group-b"}}
	catalog, err := corpus.NewCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := corpus.WriteCatalog(root, catalog); err != nil {
		t.Fatal(err)
	}
	lock, err := corpus.NewLock(corpus.DefaultHost, true, locked)
	if err != nil {
		t.Fatal(err)
	}
	if err := corpus.WriteLock(root, corpus.DefaultHost, lock); err != nil {
		t.Fatal(err)
	}
	return root, commits
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
	return string(out)
}

// DiscoverRepos must find every repo, and its count must equal CountGitEntries
// (the `find -name .git | wc -l` ground truth, AC-B1), including a repo nested
// inside another repo's working tree.
func TestDiscoverReposMatchesGitCount(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	want := []string{
		filepath.Join(root, "a"),
		filepath.Join(root, "group", "b"),
		filepath.Join(root, "group", "sub", "c"),
		filepath.Join(root, "a", "nested"), // repo inside repo a's working tree
	}
	for _, d := range want {
		gitInit(t, d)
	}

	got, err := DiscoverRepos(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("discovered %d repos, want %d: %v", len(got), len(want), got)
	}
	set := map[string]bool{}
	for _, g := range got {
		set[g] = true
	}
	for _, d := range want {
		if !set[d] {
			t.Errorf("missing repo %s", d)
		}
	}

	n, err := CountGitEntries(root)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(want) {
		t.Errorf("CountGitEntries=%d, want %d", n, len(want))
	}
	if n != len(got) {
		t.Errorf("discovery count %d != .git entry count %d", len(got), n)
	}
}
