package semanticrun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/corpus/catalog"
)

func gitTest(t *testing.T, repo string, args ...string) string {
	t.Helper()
	b, e := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	if e != nil {
		t.Fatalf("git: %v %s", e, b)
	}
	return strings.TrimSpace(string(b))
}
func projectionFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "group/project")
	os.MkdirAll(repo, 0700)
	for name, content := range map[string]string{"A.cs": "class A {}", "hidden.cs": "class Hidden {}", "sub.txt": "$Format:%H$", ".gitattributes": "hidden.cs export-ignore\nsub.txt export-subst\n"} {
		if e := os.WriteFile(filepath.Join(repo, name), []byte(content), 0600); e != nil {
			t.Fatal(e)
		}
	}
	gitTest(t, repo, "init", "-q")
	gitTest(t, repo, "config", "user.name", "Test")
	gitTest(t, repo, "config", "user.email", "test@localhost")
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-qm", "fixture")
	cat, e := catalog.NewCatalog(catalog.DefaultHost, []string{"group"})
	if e != nil {
		t.Fatal(e)
	}
	if e = catalog.WriteCatalog(root, cat); e != nil {
		t.Fatal(e)
	}
	lock, e := catalog.NewLock(catalog.DefaultHost, true, []catalog.LockedProject{{ID: 101, PathWithNamespace: "group/project", CloneURL: "git@gitlab.example.com:group/project.git", DefaultBranch: "main", DefaultCommit: gitTest(t, repo, "rev-parse", "HEAD"), Status: catalog.LockStatusCurrent}})
	if e != nil {
		t.Fatal(e)
	}
	if e = catalog.WriteLock(root, catalog.DefaultHost, lock); e != nil {
		t.Fatal(e)
	}
	return root, repo
}
func TestProjectionExactCommitAndVerify(t *testing.T) {
	root, repo := projectionFixture(t)
	os.WriteFile(filepath.Join(repo, "A.cs"), []byte("dirty"), 0600)
	os.WriteFile(filepath.Join(repo, "untracked.cs"), []byte("extra"), 0600)
	p, e := CreateProjection(context.Background(), ProjectionOptions{ManagedRoot: root, Repo: "group/project", TempParent: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	for name, want := range map[string]string{"A.cs": "class A {}", "hidden.cs": "class Hidden {}", "sub.txt": "$Format:%H$"} {
		b, e := os.ReadFile(filepath.Join(p.Root, name))
		if e != nil || string(b) != want {
			t.Fatalf("%s: %q %v", name, b, e)
		}
	}
	if _, e := os.Stat(filepath.Join(p.Root, "untracked.cs")); !os.IsNotExist(e) {
		t.Fatal("untracked copied")
	}
	os.Mkdir(filepath.Join(p.Root, "obj"), 0700)
	os.WriteFile(filepath.Join(p.Root, "obj/new"), []byte("generated"), 0600)
	if e = p.Verify(context.Background()); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(p.Root, "A.cs"), []byte("mutated"), 0600)
	if p.Verify(context.Background()) == nil {
		t.Fatal("tracked mutation accepted")
	}
	b, _ := os.ReadFile(filepath.Join(repo, "A.cs"))
	if string(b) != "dirty" {
		t.Fatal("acquisition modified")
	}
}
func TestProjectionLimitsCleanFailures(t *testing.T) {
	root, _ := projectionFixture(t)
	for _, o := range []ProjectionOptions{{MaxFiles: 1}, {MaxBytes: 1}} {
		parent := t.TempDir()
		o.ManagedRoot = root
		o.ProjectID = 101
		o.TempParent = parent
		if _, e := CreateProjection(context.Background(), o); e == nil {
			t.Fatal("limit ignored")
		}
		entries, _ := os.ReadDir(parent)
		if len(entries) != 0 {
			t.Fatal("failed projection retained")
		}
	}
}

func TestProjectionRejectsLinksAndCatalogDrift(t *testing.T) {
	root, repo := projectionFixture(t)
	p, e := CreateProjection(context.Background(), ProjectionOptions{ManagedRoot: root, Repo: "group/project", TempParent: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	cat, e := catalog.NewCatalog(catalog.DefaultHost, []string{"other"})
	if e != nil {
		t.Fatal(e)
	}
	if e = catalog.WriteCatalog(root, cat); e != nil {
		t.Fatal(e)
	}
	if p.Verify(context.Background()) == nil {
		t.Fatal("catalog policy drift accepted")
	}
	if _, e = CreateProjection(context.Background(), ProjectionOptions{ManagedRoot: root, Repo: "group/project", TempParent: t.TempDir()}); e == nil {
		t.Fatal("excluded group projected")
	}
	cat, _ = catalog.NewCatalog(catalog.DefaultHost, []string{"group"})
	catalog.WriteCatalog(root, cat)
	if e = os.Symlink("A.cs", filepath.Join(repo, "linked.cs")); e != nil {
		t.Fatal(e)
	}
	gitTest(t, repo, "add", "linked.cs")
	gitTest(t, repo, "commit", "-qm", "symlink")
	lock, e := catalog.LoadLock(root, catalog.DefaultHost)
	if e != nil {
		t.Fatal(e)
	}
	lock.Projects[0].DefaultCommit = gitTest(t, repo, "rev-parse", "HEAD")
	if e = catalog.WriteLock(root, catalog.DefaultHost, lock); e != nil {
		t.Fatal(e)
	}
	parent := t.TempDir()
	if _, e = CreateProjection(context.Background(), ProjectionOptions{ManagedRoot: root, Repo: "group/project", TempParent: parent}); e == nil {
		t.Fatal("symlink tree accepted")
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) > 0 {
		t.Fatal("failed tree retained")
	}
}
