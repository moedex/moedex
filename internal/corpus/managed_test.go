package corpus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	overlappingPath := lockedProject(2, base.PathWithNamespace+"/nested", fixtureCommitB)
	if _, err := NewLock(DefaultHost, true, []LockedProject{base, overlappingPath}); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("overlapping paths should be rejected, got %v", err)
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

func TestInitManagedCreatesCommittedSuperproject(t *testing.T) {
	fixture := newManagedGitFixture(t)
	root := filepath.Join(t.TempDir(), "managed corpus")
	cfg := Config{
		Host:        DefaultHost,
		Root:        root,
		Groups:      []string{"g"},
		Concurrency: 1,
	}

	result, err := InitManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project})
	if err != nil {
		t.Fatalf("InitManaged: %v", err)
	}
	if result.Root != root || !validGitObjectID(result.SuperprojectCommit) {
		t.Fatalf("unexpected init result: %+v", result)
	}
	for _, path := range []string{
		filepath.Join(root, ".git"),
		filepath.Join(root, ".gitmodules"),
		CatalogPath(root),
		LockPath(root),
		filepath.Join(root, "g", "repo", ".git"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected managed snapshot path %q: %v", path, err)
		}
	}

	catalog, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if catalog.Host != DefaultHost || len(catalog.GroupPolicy.TopLevelGroups) != 1 {
		t.Fatalf("unexpected catalog: %+v", catalog)
	}
	lock, err := LoadLock(root, DefaultHost)
	if err != nil {
		t.Fatalf("LoadLock: %v", err)
	}
	if len(lock.Projects) != 1 || lock.Projects[0].ID != fixture.Project.ID ||
		lock.Projects[0].DefaultCommit != fixture.Commit {
		t.Fatalf("unexpected initial lock: %+v", lock)
	}

	if got := strings.TrimSpace(runFixtureGit(t, "-C", root, "rev-list", "--count", "HEAD")); got != "1" {
		t.Fatalf("initialization created %s commits, want exactly one", got)
	}
	gitlink := runFixtureGit(t, "-C", root, "ls-tree", "HEAD", "--", fixture.Project.PathWithNamespace)
	if !strings.Contains(gitlink, "160000 commit "+fixture.Commit) {
		t.Fatalf("initial commit does not pin expected gitlink: %q", gitlink)
	}
	modulePath := strings.TrimSpace(runFixtureGit(t, "-C", root, "config", "-f", ".gitmodules", "--get", "submodule.project-101.path"))
	if modulePath != fixture.Project.PathWithNamespace {
		t.Fatalf("stable-ID submodule section path = %q", modulePath)
	}
	committedLock := runFixtureGit(t, "-C", root, "show", "HEAD:.moedex/corpus.lock.json")
	workingLock, err := os.ReadFile(LockPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if committedLock != string(workingLock) {
		t.Fatal("committed lock differs from working-tree lock")
	}
	if status := runFixtureGit(t, "-C", root, "status", "--porcelain"); status != "" {
		t.Fatalf("initialized superproject is dirty: %q", status)
	}

	cmd := exec.Command("git", "-C", root, "config", "--local", "--get", "user.name")
	if output, err := cmd.CombinedOutput(); err == nil || strings.TrimSpace(string(output)) != "" {
		t.Fatalf("InitManaged persisted a repository identity: err=%v output=%q", err, output)
	}
}

func TestInitManagedRejectsUserOwnedAndUnsafeDestinations(t *testing.T) {
	validProject := Project{
		ID:                9,
		PathWithNamespace: "g/repo",
		SSHURL:            "git@" + DefaultHost + ":g/repo.git",
		DefaultBranch:     "main",
	}

	t.Run("non-empty directory", func(t *testing.T) {
		root := t.TempDir()
		sentinel := filepath.Join(root, "must-survive")
		if err := os.WriteFile(sentinel, []byte("user data"), 0o644); err != nil {
			t.Fatal(err)
		}
		var calls []call
		_, err := InitManaged(t.Context(), fakeRunner{calls: &calls}, Config{Host: DefaultHost, Root: root}, []Project{validProject})
		if err == nil || !strings.Contains(err.Error(), "refusing non-empty") {
			t.Fatalf("non-empty destination error = %v", err)
		}
		if data, readErr := os.ReadFile(sentinel); readErr != nil || string(data) != "user data" {
			t.Fatalf("user content changed: data=%q err=%v", data, readErr)
		}
		if len(calls) != 0 {
			t.Fatalf("Git ran before non-empty destination rejection: %+v", calls)
		}
	})

	t.Run("unmarked Git repository", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := InitManaged(t.Context(), fakeRunner{}, Config{Host: DefaultHost, Root: root}, []Project{validProject})
		if err == nil || !strings.Contains(err.Error(), "unmarked") {
			t.Fatalf("unmarked repository error = %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(root, ".git")); statErr != nil {
			t.Fatalf("unmarked repository was modified: %v", statErr)
		}
	})

	tests := []struct {
		name   string
		mutate func(*Project)
	}{
		{name: "parent traversal", mutate: func(p *Project) { p.PathWithNamespace = "../repo" }},
		{name: "absolute path", mutate: func(p *Project) { p.PathWithNamespace = "/g/repo" }},
		{name: "marker collision", mutate: func(p *Project) { p.PathWithNamespace = ".moedex/repo" }},
		{name: "host mismatch", mutate: func(p *Project) { p.SSHURL = "git@evil.example:g/repo.git" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "must-not-exist")
			project := validProject
			test.mutate(&project)
			var calls []call
			_, err := InitManaged(t.Context(), fakeRunner{calls: &calls}, Config{Host: DefaultHost, Root: root}, []Project{project})
			if err == nil {
				t.Fatalf("unsafe project was accepted: %+v", project)
			}
			if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("unsafe preflight created destination: %v", statErr)
			}
			if len(calls) != 0 {
				t.Fatalf("Git ran before project validation: %+v", calls)
			}
		})
	}
}

func TestInitManagedFailureLeavesMarkedRecoverableRoot(t *testing.T) {
	fixture := newManagedGitFixture(t)
	root := filepath.Join(t.TempDir(), "interrupted")
	missing := fixture.Project
	missing.ID = 404
	missing.PathWithNamespace = "g/missing"
	missing.SSHURL = "git@" + DefaultHost + ":g/missing.git"

	_, err := InitManaged(t.Context(), ExecRunner{}, Config{Host: DefaultHost, Root: root, Groups: []string{"g"}}, []Project{missing})
	if err == nil || !strings.Contains(err.Error(), root) || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("interrupted initialization did not report exact marked root: %v", err)
	}
	if _, err := LoadCatalog(root); err != nil {
		t.Fatalf("interrupted root is not recognizably marked: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Fatalf("interrupted root lost its superproject: %v", err)
	}
	if _, err := InitManaged(t.Context(), ExecRunner{}, Config{Host: DefaultHost, Root: root}, []Project{fixture.Project}); err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("rerun should not implicitly clean or adopt interrupted root: %v", err)
	}
}

func TestManagedReconcileUsesStableIDsAndPruneAuthority(t *testing.T) {
	previous, err := NewLock(DefaultHost, true, []LockedProject{
		lockedProject(1, "g/current", fixtureCommitA),
		lockedProject(2, "g/old-name", fixtureCommitA),
		lockedProject(3, "g/missing", fixtureCommitA),
	})
	if err != nil {
		t.Fatal(err)
	}
	incoming := []Project{
		managedProject(4, "g/new"),
		managedProject(2, "g/new-name"),
		managedProject(1, "g/current"),
	}

	incomplete := ReconcileManaged(DefaultHost, previous, incoming, ManagedSyncOptions{EnumerationComplete: false, Prune: true})
	assertManagedActionKinds(t, incomplete, map[int64]ManagedActionKind{
		1: ManagedActionUpdate,
		2: ManagedActionMove,
		3: ManagedActionCarryForward,
		4: ManagedActionAdd,
	})
	if got := incomplete.ActionsOfKind(ManagedActionPrune); len(got) != 0 {
		t.Fatalf("incomplete enumeration authorized prune: %+v", got)
	}

	missing := ReconcileManaged(DefaultHost, previous, incoming, ManagedSyncOptions{EnumerationComplete: true})
	assertManagedActionKinds(t, missing, map[int64]ManagedActionKind{1: ManagedActionUpdate, 2: ManagedActionMove, 3: ManagedActionMissing, 4: ManagedActionAdd})
	prune := ReconcileManaged(DefaultHost, previous, incoming, ManagedSyncOptions{EnumerationComplete: true, Prune: true})
	assertManagedActionKinds(t, prune, map[int64]ManagedActionKind{1: ManagedActionUpdate, 2: ManagedActionMove, 3: ManagedActionPrune, 4: ManagedActionAdd})
}

func TestManagedReconcileReportsIDAndPathConflicts(t *testing.T) {
	previous, err := NewLock(DefaultHost, true, []LockedProject{lockedProject(1, "g/owned", fixtureCommitA)})
	if err != nil {
		t.Fatal(err)
	}
	duplicateID := ReconcileManaged(DefaultHost, previous, []Project{
		managedProject(1, "g/one"), managedProject(1, "g/two"),
	}, ManagedSyncOptions{EnumerationComplete: true, Prune: true})
	if actions := duplicateID.ActionsOfKind(ManagedActionConflict); len(actions) != 1 || actions[0].ProjectID != 1 {
		t.Fatalf("duplicate ID did not become one conflict: %+v", duplicateID.Actions)
	}
	pathCollision := ReconcileManaged(DefaultHost, previous, []Project{managedProject(2, "g/owned")}, ManagedSyncOptions{EnumerationComplete: true, Prune: true})
	if actions := pathCollision.ActionsOfKind(ManagedActionConflict); len(actions) != 1 || actions[0].ProjectID != 2 {
		t.Fatalf("ID/path collision did not become a conflict: %+v", pathCollision.Actions)
	}
	if actions := pathCollision.ActionsOfKind(ManagedActionPrune); len(actions) != 1 || actions[0].ProjectID != 1 {
		t.Fatalf("existing owner should remain a separately explicit prune decision: %+v", pathCollision.Actions)
	}
}

func TestManagedSyncUpdateForcePushMoveAndNoChange(t *testing.T) {
	fixture := newManagedGitFixture(t)
	root := filepath.Join(t.TempDir(), "managed")
	cfg := Config{Host: DefaultHost, Root: root, Groups: []string{"g"}, Concurrency: 2}
	if _, err := InitManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project}); err != nil {
		t.Fatalf("InitManaged: %v", err)
	}
	initialHead := strings.TrimSpace(runFixtureGit(t, "-C", root, "rev-parse", "HEAD"))
	initialLock, err := os.ReadFile(LockPath(root))
	if err != nil {
		t.Fatal(err)
	}
	initialCatalog, err := os.ReadFile(CatalogPath(root))
	if err != nil {
		t.Fatal(err)
	}

	noChange, err := SyncManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project}, ManagedSyncOptions{EnumerationComplete: true})
	if err != nil {
		t.Fatalf("no-change SyncManaged: %v", err)
	}
	if noChange.Changed || noChange.SuperprojectCommit != "" {
		t.Fatalf("no-change sync created a snapshot: %+v", noChange)
	}
	assertFileBytes(t, LockPath(root), initialLock)
	assertFileBytes(t, CatalogPath(root), initialCatalog)
	if head := strings.TrimSpace(runFixtureGit(t, "-C", root, "rev-parse", "HEAD")); head != initialHead {
		t.Fatalf("no-change sync advanced superproject: %s -> %s", initialHead, head)
	}

	updatedCommit := commitManagedFixture(t, &fixture, "ordinary update\n", false)
	updated, err := SyncManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project}, ManagedSyncOptions{EnumerationComplete: true})
	if err != nil || !updated.Changed {
		t.Fatalf("ordinary update result=%+v err=%v", updated, err)
	}
	assertLockedCommit(t, root, fixture.Project.ID, updatedCommit, LockStatusCurrent)

	forceCommit := commitManagedFixture(t, &fixture, "force-pushed replacement\n", true)
	forced, err := SyncManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project}, ManagedSyncOptions{EnumerationComplete: true})
	if err != nil || !forced.Changed {
		t.Fatalf("force-push update result=%+v err=%v", forced, err)
	}
	assertLockedCommit(t, root, fixture.Project.ID, forceCommit, LockStatusCurrent)

	renamed := fixture.Project
	renamed.PathWithNamespace = "renamed group/repo"
	moved, err := SyncManaged(t.Context(), ExecRunner{}, cfg, []Project{renamed}, ManagedSyncOptions{EnumerationComplete: true})
	if err != nil || !moved.Changed {
		t.Fatalf("stable-ID move result=%+v err=%v", moved, err)
	}
	if _, err := os.Stat(filepath.Join(root, "g", "repo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old submodule path survived move: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(renamed.PathWithNamespace), ".git")); err != nil {
		t.Fatalf("new submodule path missing after move: %v", err)
	}
	lock, err := LoadLock(root, DefaultHost)
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Projects) != 1 || lock.Projects[0].PathWithNamespace != renamed.PathWithNamespace || lock.Projects[0].DefaultCommit != forceCommit {
		t.Fatalf("moved lock entry is inconsistent: %+v", lock)
	}
	modulePath := strings.TrimSpace(runFixtureGit(t, "-C", root, "config", "-f", ".gitmodules", "--get", "submodule.project-101.path"))
	if modulePath != renamed.PathWithNamespace {
		t.Fatalf(".gitmodules path = %q, want %q", modulePath, renamed.PathWithNamespace)
	}
}

func TestManagedSyncAddMissingDirtyAndExplicitPrune(t *testing.T) {
	fixture := newManagedGitFixture(t)
	second := addManagedFixtureProject(t, fixture, 202, "g/second")
	root := filepath.Join(t.TempDir(), "managed")
	cfg := Config{Host: DefaultHost, Root: root, Groups: []string{"g"}, Concurrency: 2}
	if _, err := InitManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project}); err != nil {
		t.Fatal(err)
	}
	if result, err := SyncManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project, second.Project}, ManagedSyncOptions{EnumerationComplete: true}); err != nil || !result.Changed {
		t.Fatalf("add result=%+v err=%v", result, err)
	}
	assertLockedCommit(t, root, second.Project.ID, second.Commit, LockStatusCurrent)

	missing, err := SyncManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project}, ManagedSyncOptions{EnumerationComplete: true})
	if err != nil || !missing.Changed {
		t.Fatalf("missing carry-forward result=%+v err=%v", missing, err)
	}
	assertLockedCommit(t, root, second.Project.ID, second.Commit, LockStatusCarriedForward)
	lockBytes, err := os.ReadFile(LockPath(root))
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(runFixtureGit(t, "-C", root, "rev-parse", "HEAD"))
	repeated, err := SyncManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project}, ManagedSyncOptions{EnumerationComplete: true})
	if err != nil || repeated.Changed {
		t.Fatalf("repeated missing sync result=%+v err=%v", repeated, err)
	}
	assertFileBytes(t, LockPath(root), lockBytes)
	if got := strings.TrimSpace(runFixtureGit(t, "-C", root, "rev-parse", "HEAD")); got != head {
		t.Fatalf("repeated sync created a commit: %s -> %s", head, got)
	}

	dirtyPath := filepath.Join(root, "g", "second", "do-not-delete.txt")
	if err := os.WriteFile(dirtyPath, []byte("local data"), 0o644); err != nil {
		t.Fatal(err)
	}
	conflicted, err := SyncManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project}, ManagedSyncOptions{EnumerationComplete: true, Prune: true})
	var partial *ManagedSyncError
	if !errors.As(err, &partial) || len(partial.Failures) != 1 || conflicted.Changed {
		t.Fatalf("dirty prune result=%+v err=%v", conflicted, err)
	}
	if _, err := os.Stat(dirtyPath); err != nil {
		t.Fatalf("dirty submodule content was removed: %v", err)
	}
	if err := os.Remove(dirtyPath); err != nil {
		t.Fatal(err)
	}

	pruned, err := SyncManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project}, ManagedSyncOptions{EnumerationComplete: true, Prune: true})
	if err != nil || !pruned.Changed {
		t.Fatalf("explicit prune result=%+v err=%v", pruned, err)
	}
	if _, err := os.Stat(filepath.Join(root, "g", "second")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pruned working tree still exists: %v", err)
	}
	lock, err := LoadLock(root, DefaultHost)
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Projects) != 1 || lock.Projects[0].ID != fixture.Project.ID {
		t.Fatalf("pruned project remains in lock: %+v", lock)
	}
}

func TestManagedSyncFailureAtFetchCarriesPriorWhileSuccessAdvances(t *testing.T) {
	fixture := newManagedGitFixture(t)
	second := addManagedFixtureProject(t, fixture, 202, "g/second")
	root := filepath.Join(t.TempDir(), "managed")
	cfg := Config{Host: DefaultHost, Root: root, Groups: []string{"g"}, Concurrency: 2}
	if _, err := InitManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project, second.Project}); err != nil {
		t.Fatal(err)
	}
	advanced := commitManagedFixture(t, &fixture, "first advances\n", false)
	unreachable := second.Project
	unreachable.SSHURL = "git@" + DefaultHost + ":g/not-present.git"

	result, err := SyncManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project, unreachable}, ManagedSyncOptions{EnumerationComplete: true})
	var partial *ManagedSyncError
	if !errors.As(err, &partial) || len(partial.Failures) != 1 || !result.Changed || result.SuperprojectCommit == "" {
		t.Fatalf("partial sync result=%+v err=%v", result, err)
	}
	assertLockedCommit(t, root, fixture.Project.ID, advanced, LockStatusCurrent)
	assertLockedCommit(t, root, second.Project.ID, second.Commit, LockStatusCarriedForward)
	gitlink := runFixtureGit(t, "-C", root, "ls-tree", "HEAD", "--", second.Project.PathWithNamespace)
	if !strings.Contains(gitlink, "160000 commit "+second.Commit) {
		t.Fatalf("failed project gitlink was not carried forward: %q", gitlink)
	}
}

func TestManagedSyncDirtyMoveIsConflictAndPreservesOldPath(t *testing.T) {
	fixture := newManagedGitFixture(t)
	root := filepath.Join(t.TempDir(), "managed")
	cfg := Config{Host: DefaultHost, Root: root, Groups: []string{"g"}, Concurrency: 1}
	if _, err := InitManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project}); err != nil {
		t.Fatal(err)
	}
	dirtyPath := filepath.Join(root, "g", "repo", "local-only.txt")
	if err := os.WriteFile(dirtyPath, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	renamed := fixture.Project
	renamed.PathWithNamespace = "g/renamed"

	result, err := SyncManaged(t.Context(), ExecRunner{}, cfg, []Project{renamed}, ManagedSyncOptions{EnumerationComplete: true})
	var partial *ManagedSyncError
	if !errors.As(err, &partial) || len(partial.Failures) != 1 || !result.Changed {
		t.Fatalf("dirty move result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(dirtyPath); err != nil {
		t.Fatalf("dirty content was lost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "g", "renamed")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("conflicted move created new path: %v", err)
	}
	lock, err := LoadLock(root, DefaultHost)
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Projects) != 1 || lock.Projects[0].PathWithNamespace != fixture.Project.PathWithNamespace || lock.Projects[0].Status != LockStatusCarriedForward {
		t.Fatalf("dirty move did not carry old entry: %+v", lock)
	}
}

func TestManagedConcurrentFetches(t *testing.T) {
	fixture := newManagedGitFixture(t)
	second := addManagedFixtureProject(t, fixture, 202, "g/second")
	root := filepath.Join(t.TempDir(), "managed")
	cfg := Config{Host: DefaultHost, Root: root, Groups: []string{"g"}, Concurrency: 2}
	projects := []Project{fixture.Project, second.Project}
	if _, err := InitManaged(t.Context(), ExecRunner{}, cfg, projects); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runner := newConcurrentFetchRunner(ExecRunner{}, 2)
	if _, err := SyncManaged(ctx, runner, cfg, projects, ManagedSyncOptions{EnumerationComplete: true}); err != nil {
		t.Fatalf("concurrent SyncManaged: %v", err)
	}
	if peak := runner.peak.Load(); peak < 2 {
		t.Fatalf("fetch peak = %d, want at least 2", peak)
	}
}

func TestManagedFailureBoundariesPreserveRecoverableCommittedSnapshot(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		move      bool
		lockHook  bool
	}{
		{name: "path move", operation: "mv", move: true},
		{name: "lock write", lockHook: true},
		{name: "stage", operation: "add"},
		{name: "commit", operation: "commit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newManagedGitFixture(t)
			root := filepath.Join(t.TempDir(), "managed")
			cfg := Config{Host: DefaultHost, Root: root, Groups: []string{"g"}, Concurrency: 1}
			if _, err := InitManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project}); err != nil {
				t.Fatal(err)
			}
			project := fixture.Project
			if test.move {
				project.PathWithNamespace = "g/moved"
			} else {
				commitManagedFixture(t, &fixture, "boundary update\n", false)
			}

			var runner Runner = ExecRunner{}
			if test.operation != "" {
				runner = &managedFaultRunner{Runner: runner, operation: test.operation}
			}
			hooks := managedSyncHooks{}
			if test.lockHook {
				hooks.beforeLockRename = func(string) error { return errors.New("injected lock-write failure") }
			}
			_, err := syncManaged(t.Context(), runner, cfg, []Project{project}, ManagedSyncOptions{EnumerationComplete: true}, hooks)
			if err == nil || !strings.Contains(err.Error(), root) || !strings.Contains(err.Error(), "last committed snapshot") {
				t.Fatalf("boundary failure did not report recoverable root: %v", err)
			}
			if _, loadErr := LoadCatalog(root); loadErr != nil {
				t.Fatalf("failure made root unrecognizable: %v", loadErr)
			}
			if _, loadErr := LoadLock(root, DefaultHost); loadErr != nil {
				t.Fatalf("failure left an unreadable worktree lock: %v", loadErr)
			}
			assertRecoverableCommittedSnapshot(t, root)
		})
	}
}

// TestManagedSyncResumesInterruptedCommitInsteadOfWedging is the regression
// for F-05: a kill (signal, deadline, OOM, reboot) landing between
// stageManagedSync's `git add` and the follow-up `git commit` used to leave
// the superproject index staged but uncommitted forever. That state failed
// doctor's cleanliness check closed with a manual-only fix, and since the
// shipped systemd unit runs `doctor` as ExecStartPre, every subsequent hourly
// sync was wedged behind it before sync ever got a chance to run. The fault
// injection below models the exact boundary: stageManagedSync's `git add`
// completes and the commit that would follow it never does.
func TestManagedSyncResumesInterruptedCommitInsteadOfWedging(t *testing.T) {
	fixture := newManagedGitFixture(t)
	root := filepath.Join(t.TempDir(), "managed")
	cfg := Config{Host: DefaultHost, Root: root, Groups: []string{"g"}, Concurrency: 1}
	if _, err := InitManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project}); err != nil {
		t.Fatal(err)
	}
	updatedCommit := commitManagedFixture(t, &fixture, "interrupted update\n", false)

	faulty := &managedFaultRunner{Runner: ExecRunner{}, operation: "commit"}
	if _, err := syncManaged(t.Context(), faulty, cfg, []Project{fixture.Project}, ManagedSyncOptions{EnumerationComplete: true}, managedSyncHooks{}); err == nil {
		t.Fatal("interrupted sync unexpectedly succeeded")
	}

	// Confirm the fault injection actually modeled the trigger: the
	// superproject index holds staged, uncommitted content.
	if status := runFixtureGit(t, "-C", root, "status", "--porcelain", "--untracked-files=no"); strings.TrimSpace(status) == "" {
		t.Fatal("interrupted sync left no staged content — fault injection did not reach the add/commit boundary")
	}

	lock, err := LoadLock(root, DefaultHost)
	if err != nil {
		t.Fatal(err)
	}
	if check := doctorManagedCleanliness(t.Context(), ExecRunner{}, root, lock); check.Status != StatusOK {
		t.Fatalf("doctor cleanliness = %+v, want OK — a staged-only stage from an interrupted sync must not fail-close doctor (it gates ExecStartPre)", check)
	}

	// The next real sync (no fault injection this time) must resume and
	// finish the interrupted commit on its own, leaving a clean, consistent,
	// fully committed snapshot — no manual fix required.
	if _, err := SyncManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project}, ManagedSyncOptions{EnumerationComplete: true}); err != nil {
		t.Fatalf("resuming the interrupted sync failed: %v", err)
	}
	assertLockedCommit(t, root, fixture.Project.ID, updatedCommit, LockStatusCurrent)
	assertRecoverableCommittedSnapshot(t, root)
	if status := runFixtureGit(t, "-C", root, "status", "--porcelain", "--untracked-files=all"); strings.TrimSpace(status) != "" {
		t.Fatalf("resumed managed corpus is not fully clean: %q", status)
	}
}

// TestManagedStagedOnly pins the exact porcelain-parsing rule behind the F-05
// fix: a listing counts as "staged only" (safe to resume as a commit) only
// when every line's worktree-status column (the second byte) is blank — any
// unstaged or untracked change anywhere disqualifies the whole listing.


func TestManagedStagedOnly(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty listing", "", true},
		{"single staged add", "A  path/one\n", true},
		{"staged add and staged modify", "A  path/one\nM  path/two\n", true},
		{"staged then further modified", "MM path/one\n", false},
		{"untracked file", "?? path/one\n", false},
		{"staged plus unrelated untracked", "A  path/one\n?? path/two\n", false},
		{"unstaged modify only", " M path/one\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := managedStagedOnly([]byte(tc.in)); got != tc.want {
				t.Errorf("managedStagedOnly(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
func TestManagedFailureAtCloneLeavesMarkedRootAndNoUserDeletion(t *testing.T) {
	fixture := newManagedGitFixture(t)
	root := filepath.Join(t.TempDir(), "managed")
	runner := &managedFaultRunner{Runner: ExecRunner{}, operation: "submodule"}
	_, err := InitManaged(t.Context(), runner, Config{Host: DefaultHost, Root: root, Groups: []string{"g"}}, []Project{fixture.Project})
	if err == nil || !strings.Contains(err.Error(), root) || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("clone failure did not report marked root: %v", err)
	}
	if _, err := LoadCatalog(root); err != nil {
		t.Fatalf("clone failure left an unmarked root: %v", err)
	}
	unmarkedRoot := filepath.Join(t.TempDir(), "empty-unmarked")
	if err := os.Mkdir(unmarkedRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	initFault := &managedFaultRunner{Runner: ExecRunner{}, operation: "init"}
	_, _ = InitManaged(t.Context(), initFault, Config{Host: DefaultHost, Root: unmarkedRoot}, []Project{fixture.Project})
	if _, err := os.Stat(CatalogPath(unmarkedRoot)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failure before the marker made an unmarked root managed: %v", err)
	}

	userRoot := t.TempDir()
	sentinel := filepath.Join(userRoot, "user-owned")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = InitManaged(t.Context(), runner, Config{Host: DefaultHost, Root: userRoot}, []Project{fixture.Project})
	if got, readErr := os.ReadFile(sentinel); readErr != nil || string(got) != "keep" {
		t.Fatalf("failed initialization removed user content: data=%q err=%v", got, readErr)
	}
	if _, err := os.Stat(CatalogPath(userRoot)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("user-owned root was marked: %v", err)
	}
}

func TestManagedUnsafeArgumentsAreRejectedOrSeparated(t *testing.T) {
	fixture := newManagedGitFixture(t)
	odd := addManagedFixtureProject(t, fixture, 303, "odd-group/repo with space")
	odd.Project.DefaultBranch = "feature/odd+name"
	runFixtureGit(t, "-C", odd.Work, "branch", odd.Project.DefaultBranch)
	runFixtureGit(t, "-C", odd.Work, "push", "origin", odd.Project.DefaultBranch)
	root := filepath.Join(t.TempDir(), "managed corpus")
	cfg := Config{Host: DefaultHost, Root: root, Groups: []string{"odd-group"}, Concurrency: 1}
	recorder := &managedRecordingRunner{Runner: ExecRunner{}}
	if _, err := InitManaged(t.Context(), recorder, cfg, []Project{odd.Project}); err != nil {
		t.Fatalf("InitManaged with unusual argv values: %v\ncalls=%q", err, recorder.snapshot())
	}
	if _, err := SyncManaged(t.Context(), recorder, cfg, nil, ManagedSyncOptions{EnumerationComplete: true, Prune: true}); err != nil {
		t.Fatalf("prune with whitespace-bearing path: %v", err)
	}
	for _, args := range recorder.snapshot() {
		pathIndex := stringIndex(args, gitPathArg(odd.Project.PathWithNamespace))
		if pathIndex >= 0 {
			separator := stringIndex(args, "--")
			if separator < 0 || separator > pathIndex {
				t.Fatalf("path was not protected by an argv separator: %q", args)
			}
		}
		urlIndex := stringIndex(args, odd.Project.SSHURL)
		if urlIndex >= 0 {
			separator := stringIndex(args, "--")
			if separator < 0 || separator > urlIndex {
				t.Fatalf("URL was not protected by an argv separator: %q", args)
			}
		}
		branchIndex := stringIndex(args, odd.Project.DefaultBranch)
		if branchIndex >= 0 && (branchIndex == 0 || args[branchIndex-1] != "--branch") {
			t.Fatalf("branch was not passed as an option value: %q", args)
		}
	}

	unsafe := []Project{
		{ID: 1, PathWithNamespace: "g/repo", SSHURL: "git@" + DefaultHost + ":g/repo.git", DefaultBranch: "--upload-pack=bad"},
		{ID: 1, PathWithNamespace: "g/repo", SSHURL: "--config=bad", DefaultBranch: "main"},
		{ID: 1, PathWithNamespace: "-g/repo", SSHURL: "git@" + DefaultHost + ":-g/repo.git", DefaultBranch: "main"},
	}
	for i, project := range unsafe {
		unsafeRoot := filepath.Join(t.TempDir(), fmt.Sprintf("unsafe-%d", i))
		var calls []call
		_, err := InitManaged(t.Context(), fakeRunner{calls: &calls}, Config{Host: DefaultHost, Root: unsafeRoot}, []Project{project})
		if err == nil || len(calls) != 0 {
			t.Fatalf("unsafe value reached Git: project=%+v err=%v calls=%+v", project, err, calls)
		}
		if _, statErr := os.Stat(unsafeRoot); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("unsafe value created a root: %v", statErr)
		}
	}
}

func TestManagedRedactsCredentialDiagnostics(t *testing.T) {
	secret := "super-secret-value"
	runner := fakeRunner{runs: map[string]Result{
		"git": {Code: 1, Stderr: []byte("fatal: https://oauth2:" + secret + "@" + DefaultHost + "/g/repo.git Authorization: Bearer " + secret + " token=" + secret + " glpat-abcd1234")},
	}}
	_, err := runManagedGit(t.Context(), runner, "probe remote", t.TempDir(), "fetch")
	if err == nil {
		t.Fatal("expected injected Git failure")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(strings.ToLower(err.Error()), "glpat-") {
		t.Fatalf("credential material leaked in diagnostic: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("diagnostic did not show redaction marker: %v", err)
	}

	credentialProject := managedProject(1, "g/repo")
	credentialProject.SSHURL = "https://oauth2:" + secret + "@" + DefaultHost + "/g/repo.git"
	_, err = InitManaged(t.Context(), fakeRunner{}, Config{Host: DefaultHost, Root: filepath.Join(t.TempDir(), "managed")}, []Project{credentialProject})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("credential-bearing URL leaked from validation: %v", err)
	}
}

type managedGitFixture struct {
	Project    Project
	Commit     string
	Base       string
	RemoteRoot string
	Work       string
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
	for _, dir := range []string{home, remoteRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "xdg"))
	t.Setenv("GIT_CONFIG_GLOBAL", configPath)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")

	filePrefix := "file://" + filepath.ToSlash(remoteRoot) + "/"
	runFixtureGit(t, "config", "--file", configPath, "url."+filePrefix+".insteadOf", "git@"+DefaultHost+":")
	runFixtureGit(t, "config", "--file", configPath, "protocol.file.allow", "always")

	fixture := managedGitFixture{Base: base, RemoteRoot: remoteRoot}
	created := addManagedFixtureProject(t, fixture, 101, "g/repo")
	fixture.Project = created.Project
	fixture.Commit = created.Commit
	fixture.Work = created.Work
	return fixture
}

func addManagedFixtureProject(t *testing.T, fixture managedGitFixture, id int64, projectPath string) managedGitFixture {
	t.Helper()
	remote := filepath.Join(fixture.RemoteRoot, filepath.FromSlash(projectPath)+".git")
	work := filepath.Join(fixture.Base, fmt.Sprintf("work-%d", id))
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatal(err)
	}
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
	return managedGitFixture{
		Project:    managedProject(id, projectPath),
		Commit:     commit,
		Base:       fixture.Base,
		RemoteRoot: fixture.RemoteRoot,
		Work:       work,
	}
}

func commitManagedFixture(t *testing.T, fixture *managedGitFixture, content string, force bool) string {
	t.Helper()
	if force {
		runFixtureGit(t, "-C", fixture.Work, "reset", "--hard", "HEAD~1")
	}
	if err := os.WriteFile(filepath.Join(fixture.Work, "README.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, "-C", fixture.Work, "add", "--", "README.md")
	runFixtureGit(t, "-C", fixture.Work,
		"-c", "user.name=Moedex Test",
		"-c", "user.email=moedex-test@localhost",
		"commit", "-m", "fixture update")
	pushArgs := []string{"-C", fixture.Work, "push"}
	if force {
		pushArgs = append(pushArgs, "--force")
	}
	pushArgs = append(pushArgs, "origin", "main")
	runFixtureGit(t, pushArgs...)
	fixture.Commit = strings.TrimSpace(runFixtureGit(t, "-C", fixture.Work, "rev-parse", "HEAD"))
	return fixture.Commit
}

type concurrentFetchRunner struct {
	Runner
	target   int32
	inFlight atomic.Int32
	peak     atomic.Int32
	release  chan struct{}
	once     sync.Once
}

type managedFaultRunner struct {
	Runner
	operation string
	mu        sync.Mutex
	fired     bool
}

func (r *managedFaultRunner) RunEnv(ctx context.Context, env []string, name string, args ...string) (Result, error) {
	r.mu.Lock()
	match := !r.fired && name == "git" && containsString(args, r.operation)
	if match {
		r.fired = true
	}
	r.mu.Unlock()
	if match {
		return Result{Code: 1, Stderr: []byte("injected " + r.operation + " failure")}, nil
	}
	return r.Runner.RunEnv(ctx, env, name, args...)
}

type managedRecordingRunner struct {
	Runner
	mu    sync.Mutex
	calls [][]string
}

func (r *managedRecordingRunner) RunEnv(ctx context.Context, env []string, name string, args ...string) (Result, error) {
	if name == "git" {
		r.mu.Lock()
		r.calls = append(r.calls, append([]string(nil), args...))
		r.mu.Unlock()
	}
	return r.Runner.RunEnv(ctx, env, name, args...)
}

func (r *managedRecordingRunner) snapshot() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]string, len(r.calls))
	for i := range r.calls {
		out[i] = append([]string(nil), r.calls[i]...)
	}
	return out
}

func newConcurrentFetchRunner(delegate Runner, target int32) *concurrentFetchRunner {
	return &concurrentFetchRunner{Runner: delegate, target: target, release: make(chan struct{})}
}

func (r *concurrentFetchRunner) RunEnv(ctx context.Context, env []string, name string, args ...string) (Result, error) {
	if name != "git" || !containsString(args, "fetch") {
		return r.Runner.RunEnv(ctx, env, name, args...)
	}
	current := r.inFlight.Add(1)
	defer r.inFlight.Add(-1)
	for {
		peak := r.peak.Load()
		if current <= peak || r.peak.CompareAndSwap(peak, current) {
			break
		}
	}
	if current >= r.target {
		r.once.Do(func() { close(r.release) })
	}
	select {
	case <-r.release:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	return r.Runner.RunEnv(ctx, env, name, args...)
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

func managedProject(id int64, projectPath string) Project {
	return Project{
		ID:                id,
		PathWithNamespace: projectPath,
		SSHURL:            fmt.Sprintf("git@%s:%s.git", DefaultHost, projectPath),
		DefaultBranch:     "main",
	}
}

func assertManagedActionKinds(t *testing.T, plan ManagedPlan, want map[int64]ManagedActionKind) {
	t.Helper()
	if len(plan.Actions) != len(want) {
		t.Fatalf("actions = %+v, want one action for each of %+v", plan.Actions, want)
	}
	for _, action := range plan.Actions {
		if wantKind, ok := want[action.ProjectID]; !ok || action.Kind != wantKind {
			t.Fatalf("action for project %d = %s, want %s; all=%+v", action.ProjectID, action.Kind, wantKind, plan.Actions)
		}
	}
}

func assertLockedCommit(t *testing.T, root string, id int64, commit string, status LockStatus) {
	t.Helper()
	lock, err := LoadLock(root, DefaultHost)
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range lock.Projects {
		if project.ID == id {
			if project.DefaultCommit != commit || project.Status != status {
				t.Fatalf("project %d lock = %+v, want commit=%s status=%s", id, project, commit, status)
			}
			return
		}
	}
	t.Fatalf("project %d missing from lock: %+v", id, lock)
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file %q changed:\n%s\nwant:\n%s", path, got, want)
	}
}

func assertRecoverableCommittedSnapshot(t *testing.T, root string) {
	t.Helper()
	lockData := []byte(runFixtureGit(t, "-C", root, "show", "HEAD:.moedex/corpus.lock.json"))
	var lock Lock
	if err := json.Unmarshal(lockData, &lock); err != nil {
		t.Fatalf("committed lock is not JSON: %v", err)
	}
	catalogData := []byte(runFixtureGit(t, "-C", root, "show", "HEAD:.moedex/corpus.json"))
	var catalog Catalog
	if err := json.Unmarshal(catalogData, &catalog); err != nil {
		t.Fatalf("committed catalog is not JSON: %v", err)
	}
	if err := catalog.Validate(); err != nil {
		t.Fatalf("committed catalog is invalid: %v", err)
	}
	if err := lock.Validate(catalog.Host); err != nil {
		t.Fatalf("committed lock is invalid: %v", err)
	}
	modules := runFixtureGit(t, "-C", root, "show", "HEAD:.gitmodules")
	for _, project := range lock.Projects {
		gitlink := runFixtureGit(t, "-C", root, "ls-tree", "HEAD", "--", project.PathWithNamespace)
		if !strings.Contains(gitlink, "160000 commit "+project.DefaultCommit) {
			t.Fatalf("committed gitlink disagrees with lock entry %+v: %q", project, gitlink)
		}
		if !strings.Contains(modules, `[submodule "`+managedSubmoduleName(project.ID)+`"]`) ||
			!strings.Contains(modules, "path = "+project.PathWithNamespace) {
			t.Fatalf("committed .gitmodules disagrees with lock entry %+v:\n%s", project, modules)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func stringIndex(values []string, want string) int {
	for i, value := range values {
		if value == want {
			return i
		}
	}
	return -1
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
