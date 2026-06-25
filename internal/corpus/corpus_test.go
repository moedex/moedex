package corpus

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeRunner is a programmable Runner for tests. paths maps an executable name to
// the path LookPath returns (absent name → not found). runs maps a command-line
// prefix to the Result returned; the first registered prefix that matches the
// joined "name args..." wins, so callers can register "glab auth status" and
// ignore trailing flags. Optional: calls records every invocation (for asserting
// args/env), and fail forces a non-zero exit for any command it matches (for
// simulating clone failures).
type fakeRunner struct {
	paths   map[string]string
	runs    map[string]Result
	calls   *[]call
	fail    func(cmd string) bool
	respond func(cmd string) (Result, bool) // full control; checked before runs
}

type call struct {
	cmd string
	env []string
}

func (f fakeRunner) LookPath(name string) (string, error) {
	if p, ok := f.paths[name]; ok && p != "" {
		return p, nil
	}
	return "", errNotFound
}

func (f fakeRunner) Run(ctx context.Context, name string, args ...string) (Result, error) {
	return f.RunEnv(ctx, nil, name, args...)
}

func (f fakeRunner) RunEnv(_ context.Context, env []string, name string, args ...string) (Result, error) {
	cmd := strings.TrimSpace(name + " " + strings.Join(args, " "))
	if f.calls != nil {
		*f.calls = append(*f.calls, call{cmd: cmd, env: env})
	}
	if f.fail != nil && f.fail(cmd) {
		return Result{Code: 1, Stderr: []byte("simulated clone failure")}, nil
	}
	if f.respond != nil {
		if res, ok := f.respond(cmd); ok {
			return res, nil
		}
	}
	for prefix, res := range f.runs {
		if strings.HasPrefix(cmd, prefix) {
			return res, nil
		}
	}
	return Result{}, nil // unregistered: exit 0, empty output
}

var errNotFound = &lookupError{}

type lookupError struct{}

func (*lookupError) Error() string { return "executable not found" }

// ---------------------------------------------------------------------------

func TestParseProjects_ConcatenatedArrays(t *testing.T) {
	// glab --paginate emits one array per page, back-to-back with no separator.
	in := `[{"path_with_namespace":"Services.Payment/A","ssh_url_to_repo":"git@h:Services.Payment/A.git","default_branch":"main"}]` +
		`[{"path_with_namespace":"Libraries.Common/B","ssh_url_to_repo":"git@h:Libraries.Common/B.git","default_branch":"master"},` +
		`{"path_with_namespace":"","ssh_url_to_repo":"git@h:junk.git"}]`
	got, err := ParseProjects(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseProjects: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 projects (empty-path entry skipped), got %d: %+v", len(got), got)
	}
	if got[0].PathWithNamespace != "Services.Payment/A" || got[1].DefaultBranch != "master" {
		t.Fatalf("parsed fields wrong: %+v", got)
	}
}

func TestParseProjects_SingleArray(t *testing.T) {
	in := `[{"path_with_namespace":"X/Y","ssh_url_to_repo":"git@h:X/Y.git"}]`
	got, err := ParseProjects(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseProjects: %v", err)
	}
	if len(got) != 1 || got[0].PathWithNamespace != "X/Y" {
		t.Fatalf("want single project X/Y, got %+v", got)
	}
}

func TestTopLevelGroup(t *testing.T) {
	cases := map[string]string{
		"Services.Payment/TC.BillingApi": "Services.Payment",
		"Libraries.Common/sub/deep/Repo": "Libraries.Common",
		"NoSlash":                        "NoSlash",
	}
	for in, want := range cases {
		if got := (Project{PathWithNamespace: in}).TopLevelGroup(); got != want {
			t.Errorf("TopLevelGroup(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFilterByGroups(t *testing.T) {
	projects := []Project{
		{PathWithNamespace: "Services.Payment/A"},
		{PathWithNamespace: "Zak/scratch"},
		{PathWithNamespace: "Libraries.Common/B"},
		{PathWithNamespace: "ai/experiment"},
	}
	got := FilterByGroups(projects, []string{"Services.Payment", "Libraries.Common"})
	if len(got) != 2 {
		t.Fatalf("want 2 kept, got %d: %+v", len(got), got)
	}
	if got[0].PathWithNamespace != "Services.Payment/A" || got[1].PathWithNamespace != "Libraries.Common/B" {
		t.Fatalf("filtered set wrong (order should be preserved): %+v", got)
	}
	// Empty allowlist = no filter.
	if all := FilterByGroups(projects, nil); len(all) != len(projects) {
		t.Fatalf("empty allow should pass all %d, got %d", len(projects), len(all))
	}
}

func TestParseGroups(t *testing.T) {
	in := "# header comment\n\n  Services.Payment  \nLibraries.Common\n# another\nServices.Payment\nansible\n"
	got := ParseGroups(strings.NewReader(in))
	want := []string{"Libraries.Common", "Services.Payment", "ansible"} // sorted, de-duped, trimmed
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// doctor decision tree
// ---------------------------------------------------------------------------

func TestDoctor_AllGood(t *testing.T) {
	projectsJSON := `[{"path_with_namespace":"Services.Payment/A","ssh_url_to_repo":"git@h:Services.Payment/A.git","default_branch":"main"}]`
	r := fakeRunner{
		paths: map[string]string{"glab": "/usr/bin/glab", "git": "/usr/bin/git"},
		runs: map[string]Result{
			"glab --version":      {Stdout: []byte("glab 1.100.0 (deadbeef)\n")},
			"glab auth status":    {Code: 0},
			"glab api --hostname": {Stdout: []byte(projectsJSON)},
		},
	}
	cfg := Config{Host: DefaultHost, Root: "/corpus", Groups: []string{"Services.Payment"}}
	rep := Doctor(context.Background(), r, cfg)
	if !rep.OK() {
		t.Fatalf("expected OK report, got %+v", rep)
	}
	if !rep.ProjectedKnown || rep.Projected != 1 {
		t.Fatalf("want projected=1 known, got known=%v n=%d", rep.ProjectedKnown, rep.Projected)
	}
	for _, c := range rep.Checks {
		if c.Status != StatusOK {
			t.Errorf("check %q not OK: %+v", c.Name, c)
		}
	}
}

func TestDoctor_GlabMissing(t *testing.T) {
	r := fakeRunner{paths: map[string]string{"git": "/usr/bin/git"}} // no glab
	rep := Doctor(context.Background(), r, Config{Host: DefaultHost})
	if rep.OK() {
		t.Fatal("expected failure when glab missing")
	}
	if rep.ProjectedKnown {
		t.Fatal("must not enumerate without glab")
	}
	var glab, auth Check
	for _, c := range rep.Checks {
		if strings.HasPrefix(c.Name, "glab CLI") {
			glab = c
		}
		if strings.HasPrefix(c.Name, "authenticated") {
			auth = c
		}
	}
	if glab.Status != StatusFail || !strings.Contains(glab.Fix, "brew install glab") {
		t.Errorf("glab check should fail with a brew hint: %+v", glab)
	}
	if auth.Status != StatusSkipped {
		t.Errorf("auth check should be skipped when glab missing: %+v", auth)
	}
}

func TestDoctor_NotAuthed(t *testing.T) {
	r := fakeRunner{
		paths: map[string]string{"glab": "/usr/bin/glab", "git": "/usr/bin/git"},
		runs: map[string]Result{
			"glab auth status": {Code: 1, Stderr: []byte("not logged in")},
		},
	}
	rep := Doctor(context.Background(), r, Config{Host: DefaultHost})
	if rep.OK() {
		t.Fatal("expected failure when not authed")
	}
	if rep.ProjectedKnown {
		t.Fatal("must not enumerate when not authed")
	}
	var auth Check
	for _, c := range rep.Checks {
		if strings.HasPrefix(c.Name, "authenticated") {
			auth = c
		}
	}
	if auth.Status != StatusFail {
		t.Fatalf("auth check should fail: %+v", auth)
	}
	if !strings.Contains(auth.Fix, "glab auth login --hostname "+DefaultHost) {
		t.Errorf("auth fix should guide host-scoped login: %q", auth.Fix)
	}
}

// ---------------------------------------------------------------------------
// clone
// ---------------------------------------------------------------------------

func TestCloneArgs(t *testing.T) {
	cfg := Config{Root: "/corpus"}
	p := Project{
		PathWithNamespace: "Services.Payment/TC.BillingApi",
		SSHURL:            "git@h:Services.Payment/TC.BillingApi.git",
		DefaultBranch:     "main",
	}
	dest, args := CloneArgs(cfg, p)
	wantDest := filepath.Join("/corpus", "Services.Payment", "TC.BillingApi")
	if dest != wantDest {
		t.Fatalf("dest = %q, want %q", dest, wantDest)
	}
	want := []string{"clone", "--depth", "1", "--single-branch", "--branch", "main", p.SSHURL, wantDest}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}

	// An empty repo advertises no default branch — the -b flag must be omitted
	// (git clone -b "" would fail).
	_, args2 := CloneArgs(cfg, Project{PathWithNamespace: "g/empty", SSHURL: "git@h:g/empty.git"})
	for _, a := range args2 {
		if a == "--branch" {
			t.Fatalf("empty-branch repo should omit --branch: %v", args2)
		}
	}
}

func TestCloneProjects(t *testing.T) {
	root := t.TempDir()

	present := Project{PathWithNamespace: "g/present", SSHURL: "git@h:g/present.git", DefaultBranch: "main"}
	good := Project{PathWithNamespace: "g/good", SSHURL: "git@h:g/good.git", DefaultBranch: "main"}
	bad := Project{PathWithNamespace: "g/bad", SSHURL: "git@h:g/bad.git", DefaultBranch: "main"}

	// Pre-create the "present" repo so it is Skipped, not re-cloned.
	if err := os.MkdirAll(filepath.Join(root, "g", "present", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	var calls []call
	r := fakeRunner{
		calls: &calls,
		fail:  func(cmd string) bool { return strings.Contains(cmd, "g/bad.git") },
	}
	cfg := Config{Root: root, Concurrency: 4}

	rep := CloneProjects(context.Background(), r, cfg, []Project{present, good, bad}, nil)

	if rep.Cloned != 1 || rep.Skipped != 1 || rep.Failed != 1 {
		t.Fatalf("counts: cloned=%d skipped=%d failed=%d", rep.Cloned, rep.Skipped, rep.Failed)
	}
	// Results preserve input order regardless of completion order.
	if rep.Results[0].Outcome != Skipped || rep.Results[1].Outcome != Cloned || rep.Results[2].Outcome != Failed {
		t.Fatalf("order not preserved: %+v", rep.Results)
	}
	// A skipped repo must never invoke git.
	for _, c := range calls {
		if strings.Contains(c.cmd, "g/present.git") {
			t.Fatalf("skipped repo should not run git: %s", c.cmd)
		}
	}
	// Clone invocations carry the LFS-skip env.
	var sawGood bool
	for _, c := range calls {
		if strings.Contains(c.cmd, "g/good.git") {
			sawGood = true
			if !envHas(c.env, "GIT_LFS_SKIP_SMUDGE=1") {
				t.Fatalf("clone env missing LFS skip: %v", c.env)
			}
		}
	}
	if !sawGood {
		t.Fatal("good repo was never cloned")
	}
	// The failure is captured with a detail for the report.
	fails := rep.Failures()
	if len(fails) != 1 || fails[0].Project.PathWithNamespace != "g/bad" || fails[0].Detail == "" {
		t.Fatalf("failure not captured with detail: %+v", fails)
	}
}

func TestDefaultGroupsEmbedded(t *testing.T) {
	g := DefaultGroups()
	if len(g) < 50 {
		t.Fatalf("built-in allowlist looks too small (%d) — embed broken?", len(g))
	}
	// Spot-check a couple of known TurnCommerce groups and sortedness.
	set := map[string]bool{}
	for _, x := range g {
		set[x] = true
	}
	for _, want := range []string{"Services.Payment", "Libraries.Common", "Products.NameBright"} {
		if !set[want] {
			t.Errorf("built-in allowlist missing %q", want)
		}
	}
}

func envHas(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// sync: reconcile + orchestration
// ---------------------------------------------------------------------------

func TestReconcile(t *testing.T) {
	allow := []string{"Services.Payment", "Libraries.Common"}
	projects := []Project{
		{PathWithNamespace: "Services.Payment/A"},   // present locally → update
		{PathWithNamespace: "Services.Payment/NEW"}, // not local → clone
		{PathWithNamespace: "Libraries.Common/B"},   // present locally → update
	}
	local := []string{
		"Services.Payment/A",
		"Libraries.Common/B",
		"Services.Payment/GONE", // in scope, not enumerated → missing
		"old-svn-repos/legacy",  // OUT of scope (group not allowed) → ignored, never pruned
	}
	plan := Reconcile(projects, local, allow)

	if got := pathsOf(plan.ToClone); !eq(got, []string{"Services.Payment/NEW"}) {
		t.Errorf("ToClone = %v", got)
	}
	if got := pathsOf(plan.ToUpdate); !eq(got, []string{"Libraries.Common/B", "Services.Payment/A"}) {
		t.Errorf("ToUpdate = %v", got)
	}
	if !eq(plan.Missing, []string{"Services.Payment/GONE"}) {
		t.Errorf("Missing = %v (out-of-scope repo must NOT be flagged)", plan.Missing)
	}
}

func TestSyncProjects(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Root: root, Groups: []string{"g"}, Concurrency: 4}

	current := Project{PathWithNamespace: "g/current", SSHURL: "git@h:g/current.git", DefaultBranch: "main"}
	changed := Project{PathWithNamespace: "g/changed", SSHURL: "git@h:g/changed.git", DefaultBranch: "main"}
	fresh := Project{PathWithNamespace: "g/fresh", SSHURL: "git@h:g/fresh.git", DefaultBranch: "main"}

	// current + changed exist locally; fresh does not. gone exists locally but is
	// not enumerated → missing (and pruned, since we pass prune=true).
	for _, rel := range []string{"g/current", "g/changed", "g/gone"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel), ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	r := fakeRunner{
		respond: func(cmd string) (Result, bool) {
			switch {
			// g/current: local HEAD == FETCH_HEAD → no change.
			case strings.Contains(cmd, "g/current") && strings.Contains(cmd, "rev-parse HEAD"):
				return Result{Stdout: []byte("aaaa\n")}, true
			case strings.Contains(cmd, "g/current") && strings.Contains(cmd, "rev-parse FETCH_HEAD"):
				return Result{Stdout: []byte("aaaa\n")}, true
			// g/changed: local HEAD != FETCH_HEAD → reset → updated.
			case strings.Contains(cmd, "g/changed") && strings.Contains(cmd, "rev-parse HEAD"):
				return Result{Stdout: []byte("aaaa\n")}, true
			case strings.Contains(cmd, "g/changed") && strings.Contains(cmd, "rev-parse FETCH_HEAD"):
				return Result{Stdout: []byte("bbbb\n")}, true
			}
			return Result{}, false // everything else (fetch/reset/clone) succeeds
		},
	}

	projects := []Project{current, changed, fresh}
	rep, err := SyncProjects(context.Background(), r, cfg, projects, true /*prune*/, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Cloned != 1 || rep.Updated != 1 || rep.Current != 1 || rep.Pruned != 1 || rep.Failed != 0 {
		t.Fatalf("counts: cloned=%d updated=%d current=%d pruned=%d missing=%d failed=%d",
			rep.Cloned, rep.Updated, rep.Current, rep.Pruned, rep.Missing, rep.Failed)
	}
	// The pruned repo's directory must be gone.
	if _, err := os.Stat(filepath.Join(root, "g", "gone")); !os.IsNotExist(err) {
		t.Errorf("g/gone should have been pruned")
	}
	// Repos that should remain.
	for _, rel := range []string{"g/current", "g/changed"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s should still exist: %v", rel, err)
		}
	}
}

func TestSyncProjects_MissingKeptWithoutPrune(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Root: root, Groups: []string{"g"}, Concurrency: 2}
	if err := os.MkdirAll(filepath.Join(root, "g", "gone", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	rep, err := SyncProjects(context.Background(), fakeRunner{}, cfg, nil, false /*no prune*/, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Missing != 1 || rep.Pruned != 0 {
		t.Fatalf("want 1 missing, 0 pruned; got missing=%d pruned=%d", rep.Missing, rep.Pruned)
	}
	if _, err := os.Stat(filepath.Join(root, "g", "gone")); err != nil {
		t.Errorf("missing repo must be KEPT without -prune: %v", err)
	}
}

// ---------------------------------------------------------------------------
// reindex
// ---------------------------------------------------------------------------

func TestReindex_FreshBuild(t *testing.T) {
	casDir := t.TempDir()   // empty → no blobmanifest.json → cas-build
	shardDir := t.TempDir() // export target
	var calls []call
	r := fakeRunner{
		paths: map[string]string{"moedex-index": "/bin/moedex-index"},
		calls: &calls,
	}
	rep, err := Reindex(context.Background(), r, ReindexOptions{
		Root: "/corpus", CASDir: casDir, ShardDir: shardDir,
		IndexBin: "moedex-index", Reload: []string{"echo", "reloaded"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Built {
		t.Error("fresh CAS should report Built=true")
	}
	if !rep.Reloaded {
		t.Error("reload command should have run")
	}
	// Sequence: cas-build → cas-export → reload.
	if len(calls) != 3 ||
		!strings.Contains(calls[0].cmd, "cas-build") ||
		!strings.Contains(calls[1].cmd, "cas-export") ||
		!strings.Contains(calls[1].cmd, "-deduped") ||
		!strings.Contains(calls[2].cmd, "echo reloaded") {
		t.Fatalf("unexpected call sequence:\n%v", calls)
	}
}

func TestReindex_DeltaRefresh(t *testing.T) {
	casDir := t.TempDir()
	// Mark the CAS as already initialized.
	if err := os.WriteFile(filepath.Join(casDir, casManifestName), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls []call
	r := fakeRunner{paths: map[string]string{"moedex-index": "/bin/moedex-index"}, calls: &calls}
	rep, err := Reindex(context.Background(), r, ReindexOptions{
		Root: "/corpus", CASDir: casDir, ShardDir: t.TempDir(), IndexBin: "moedex-index",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Built {
		t.Error("initialized CAS should delta-refresh, not build")
	}
	if rep.Reloaded {
		t.Error("no reload command was given")
	}
	if len(calls) != 2 || !strings.Contains(calls[0].cmd, "cas-refresh") || !strings.Contains(calls[1].cmd, "cas-export") {
		t.Fatalf("want cas-refresh then cas-export, got:\n%v", calls)
	}
}

func TestReindex_MissingBinary(t *testing.T) {
	r := fakeRunner{} // moedex-index not on PATH
	_, err := Reindex(context.Background(), r, ReindexOptions{
		Root: "/corpus", CASDir: t.TempDir(), ShardDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "moedex-index") {
		t.Fatalf("want missing-binary error, got %v", err)
	}
}

func TestReindex_ExportFailureStops(t *testing.T) {
	r := fakeRunner{
		paths: map[string]string{"moedex-index": "/bin/moedex-index"},
		fail:  func(cmd string) bool { return strings.Contains(cmd, "cas-export") },
	}
	rep, err := Reindex(context.Background(), r, ReindexOptions{
		Root: "/corpus", CASDir: t.TempDir(), ShardDir: t.TempDir(), IndexBin: "moedex-index",
		Reload: []string{"echo", "should-not-run"},
	})
	if err == nil || !strings.Contains(err.Error(), "cas-export") {
		t.Fatalf("want cas-export failure, got %v", err)
	}
	if rep.Reloaded {
		t.Error("reload must not run after a failed export")
	}
}

func pathsOf(ps []Project) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.PathWithNamespace
	}
	return out
}

func eq(a, b []string) bool {
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
