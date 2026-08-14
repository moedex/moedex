package corpus

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	fixtureCommitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fixtureCommitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestProjectIDUnmarshal(t *testing.T) {
	projects, err := ParseProjects(strings.NewReader(`[{"id":731,"path_with_namespace":"g/repo","ssh_url_to_repo":"git@` + DefaultHost + `:g/repo.git","default_branch":"main"}]`))
	if err != nil {
		t.Fatalf("ParseProjects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != 731 {
		t.Fatalf("stable GitLab project ID was not retained: %+v", projects)
	}
}

func TestCatalogCanonicalRoundTrip(t *testing.T) {
	firstRoot := filepath.Join(t.TempDir(), "first")
	secondRoot := filepath.Join(t.TempDir(), "second")
	first := Catalog{
		Version: ManagedSchemaVersion,
		Host:    DefaultHost,
		GroupPolicy: GroupPolicy{
			TopLevelGroups: []string{"Services.Payment", "Libraries.Common"},
		},
		RefPolicy: RefPolicyDefault,
	}
	second := first
	second.GroupPolicy.TopLevelGroups = []string{"Libraries.Common", "Services.Payment"}

	if err := WriteCatalog(firstRoot, first); err != nil {
		t.Fatalf("WriteCatalog(first): %v", err)
	}
	if err := WriteCatalog(secondRoot, second); err != nil {
		t.Fatalf("WriteCatalog(second): %v", err)
	}
	assertSameFileBytes(t, CatalogPath(firstRoot), CatalogPath(secondRoot))

	loaded, err := LoadCatalog(firstRoot)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if loaded.Version != ManagedSchemaVersion || loaded.Host != DefaultHost ||
		len(loaded.GroupPolicy.TopLevelGroups) != 2 {
		t.Fatalf("catalog round trip changed data: %+v", loaded)
	}
}

func TestCatalogRejectsUnknownSchemaAndFields(t *testing.T) {
	root := t.TempDir()
	unknownVersion := Catalog{
		Version: 99, Host: DefaultHost,
		GroupPolicy: GroupPolicy{TopLevelGroups: []string{}},
		RefPolicy:   RefPolicyDefault,
	}
	if err := WriteCatalog(root, unknownVersion); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("unknown catalog version should fail closed, got %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(CatalogPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"version":1,"host":"` + DefaultHost + `","group_policy":{"top_level_groups":[]},"ref_policy":"default","future":true}`
	if err := os.WriteFile(CatalogPath(root), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCatalog(root); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown catalog field should fail closed, got %v", err)
	}
}

func TestLockCanonicalRoundTrip(t *testing.T) {
	projects := []LockedProject{
		lockedProject(22, "g/two", strings.ToUpper(fixtureCommitB)),
		lockedProject(11, "g/one", fixtureCommitA),
	}
	lock, err := NewLock(DefaultHost, true, projects)
	if err != nil {
		t.Fatalf("NewLock: %v", err)
	}
	if got := []int64{lock.Projects[0].ID, lock.Projects[1].ID}; got[0] != 11 || got[1] != 22 {
		t.Fatalf("lock was not sorted by stable project ID: %v", got)
	}
	if lock.Projects[1].DefaultCommit != fixtureCommitB {
		t.Fatalf("commit was not canonicalized: %q", lock.Projects[1].DefaultCommit)
	}

	firstRoot := filepath.Join(t.TempDir(), "first")
	secondRoot := filepath.Join(t.TempDir(), "second")
	if err := WriteLock(firstRoot, DefaultHost, lock); err != nil {
		t.Fatalf("WriteLock(first): %v", err)
	}
	reversed := lock
	reversed.Projects = []LockedProject{lock.Projects[1], lock.Projects[0]}
	if err := WriteLock(secondRoot, DefaultHost, reversed); err != nil {
		t.Fatalf("WriteLock(second): %v", err)
	}
	assertSameFileBytes(t, LockPath(firstRoot), LockPath(secondRoot))

	loaded, err := LoadLock(firstRoot, DefaultHost)
	if err != nil {
		t.Fatalf("LoadLock: %v", err)
	}
	if !loaded.EnumerationComplete || len(loaded.Projects) != 2 {
		t.Fatalf("lock round trip changed data: %+v", loaded)
	}
}

func TestLockRejectsDuplicateIDsAndPaths(t *testing.T) {
	base := lockedProject(1, "g/one", fixtureCommitA)

	duplicateID := lockedProject(1, "g/two", fixtureCommitB)
	if _, err := NewLock(DefaultHost, true, []LockedProject{base, duplicateID}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate IDs should be rejected, got %v", err)
	}
	duplicatePath := lockedProject(2, base.PathWithNamespace, fixtureCommitB)
	if _, err := NewLock(DefaultHost, true, []LockedProject{base, duplicatePath}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate paths should be rejected, got %v", err)
	}
}

func TestLockRejectsUnsafePathsURLsAndCommits(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*LockedProject)
	}{
		{name: "parent traversal", mutate: func(p *LockedProject) { p.PathWithNamespace = "../repo" }},
		{name: "nested traversal", mutate: func(p *LockedProject) { p.PathWithNamespace = "g/../../repo" }},
		{name: "absolute path", mutate: func(p *LockedProject) { p.PathWithNamespace = "/g/repo" }},
		{name: "other host", mutate: func(p *LockedProject) { p.CloneURL = "git@evil.example:g/repo.git" }},
		{name: "credential URL", mutate: func(p *LockedProject) { p.CloneURL = "https://oauth2:secret@" + DefaultHost + "/g/repo.git" }},
		{name: "missing commit", mutate: func(p *LockedProject) { p.DefaultCommit = "" }},
		{name: "malformed commit", mutate: func(p *LockedProject) { p.DefaultCommit = strings.Repeat("z", 40) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project := lockedProject(1, "g/repo", fixtureCommitA)
			test.mutate(&project)
			if _, err := NewLock(DefaultHost, true, []LockedProject{project}); err == nil {
				t.Fatalf("unsafe lock entry was accepted: %+v", project)
			}
		})
	}
}

func TestLockFailedWritePreservesPrior(t *testing.T) {
	root := t.TempDir()
	prior, err := NewLock(DefaultHost, true, []LockedProject{lockedProject(1, "g/repo", fixtureCommitA)})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteLock(root, DefaultHost, prior); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(LockPath(root))
	if err != nil {
		t.Fatal(err)
	}

	next, err := NewLock(DefaultHost, true, []LockedProject{lockedProject(1, "g/repo", fixtureCommitB)})
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected failure before rename")
	err = writeCanonicalJSON(LockPath(root), next, func(string) error { return injected })
	if !errors.Is(err, injected) {
		t.Fatalf("write error = %v, want injected failure", err)
	}
	after, err := os.ReadFile(LockPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed write changed the prior lock bytes")
	}
	if _, err := LoadLock(root, DefaultHost); err != nil {
		t.Fatalf("prior lock is no longer readable: %v", err)
	}
}

func TestManagedFixtureUsesLocalBareRemote(t *testing.T) {
	fixture := newManagedGitFixture(t)
	res := runFixtureGit(t, "ls-remote", fixture.Project.SSHURL, "refs/heads/main")
	fields := strings.Fields(res)
	if len(fields) != 2 || fields[0] != fixture.Commit {
		t.Fatalf("local URL rewrite did not resolve fixture branch: %q", res)
	}
	if fixture.Project.ID == 0 {
		t.Fatal("fixture project must carry a stable numeric ID")
	}
}

type managedGitFixture struct {
	Project Project
	Commit  string
}

// newManagedGitFixture creates a local bare remote while exposing it through a
// host-valid GitLab SSH URL. A test-scoped Git config rewrites that URL to file://
// and isolates HOME/global/system config, so lifecycle tests cannot use network,
// credentials, SSH, or the operator's Git identity by accident.
func newManagedGitFixture(t *testing.T) managedGitFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for managed-corpus lifecycle fixtures")
	}

	base := t.TempDir()
	home := filepath.Join(base, "home")
	configPath := filepath.Join(base, "gitconfig")
	remoteRoot := filepath.Join(base, "remotes")
	remote := filepath.Join(remoteRoot, "g", "repo.git")
	work := filepath.Join(base, "work")
	for _, dir := range []string{home, filepath.Dir(remote)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "xdg"))
	t.Setenv("GIT_CONFIG_GLOBAL", configPath)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")

	runFixtureGit(t, "init", "--bare", "--", remote)
	runFixtureGit(t, "init", "--", work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("managed fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, "-C", work, "add", "--", "README.md")
	runFixtureGit(t, "-C", work,
		"-c", "user.name=Moedex Test",
		"-c", "user.email=moedex-test@localhost",
		"commit", "-m", "fixture")
	runFixtureGit(t, "-C", work, "branch", "-M", "main")
	runFixtureGit(t, "-C", work, "remote", "add", "origin", remote)
	runFixtureGit(t, "-C", work, "push", "origin", "main")
	commit := strings.TrimSpace(runFixtureGit(t, "-C", work, "rev-parse", "HEAD"))

	filePrefix := "file://" + filepath.ToSlash(remoteRoot) + "/"
	runFixtureGit(t, "config", "--file", configPath, "url."+filePrefix+".insteadOf", "git@"+DefaultHost+":")
	runFixtureGit(t, "config", "--file", configPath, "protocol.file.allow", "always")

	return managedGitFixture{
		Project: Project{
			ID:                101,
			PathWithNamespace: "g/repo",
			SSHURL:            "git@" + DefaultHost + ":g/repo.git",
			DefaultBranch:     "main",
		},
		Commit: commit,
	}
}

func runFixtureGit(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func lockedProject(id int64, projectPath, commit string) LockedProject {
	return LockedProject{
		ID:                id,
		PathWithNamespace: projectPath,
		CloneURL:          fmt.Sprintf("git@%s:%s.git", DefaultHost, projectPath),
		DefaultBranch:     "main",
		DefaultCommit:     commit,
		Status:            LockStatusCurrent,
	}
}

func assertSameFileBytes(t *testing.T, first, second string) {
	t.Helper()
	a, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("files differ:\n%s\n---\n%s", a, b)
	}
}
