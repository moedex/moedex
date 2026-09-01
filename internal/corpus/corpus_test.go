package corpus

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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
	callsMu *sync.Mutex // guards calls; required when the runner is used under concurrency (clone/sync fan-out)
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
		if f.callsMu != nil {
			f.callsMu.Lock()
		}
		*f.calls = append(*f.calls, call{cmd: cmd, env: env})
		if f.callsMu != nil {
			f.callsMu.Unlock()
		}
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

func TestDoctorDistinguishesAuthReachabilityAndGitTransport(t *testing.T) {
	projectsJSON := `[{"id":1,"path_with_namespace":"g/repo","ssh_url_to_repo":"git@` + DefaultHost + `:g/repo.git","default_branch":"main"}]`

	t.Run("authenticated but VPN unreachable", func(t *testing.T) {
		r := fakeRunner{
			paths: map[string]string{"glab": "/usr/bin/glab", "git": "/usr/bin/git"},
			respond: func(cmd string) (Result, bool) {
				switch {
				case strings.HasPrefix(cmd, "glab auth status"):
					return Result{}, true
				case strings.Contains(cmd, "glab api") && strings.HasSuffix(cmd, " /version"):
					return Result{Code: 1, Stderr: []byte("dial https://user:secret@" + DefaultHost + "/?private_token=glpat-secret: timeout")}, true
				}
				return Result{}, false
			},
		}
		rep := Doctor(t.Context(), r, Config{Host: DefaultHost, Root: t.TempDir()})
		auth := findDoctorCheck(t, rep, "authenticated to "+DefaultHost)
		reach := findDoctorCheck(t, rep, "GitLab host/VPN reachable")
		transport := findDoctorCheck(t, rep, "Git clone/fetch transport")
		if auth.Status != StatusOK || reach.Status != StatusFail || transport.Status != StatusSkipped {
			t.Fatalf("unexpected layered status: auth=%v reach=%v transport=%v", auth.Status, reach.Status, transport.Status)
		}
		for _, check := range rep.Checks {
			if strings.Contains(check.Detail, "secret") || strings.Contains(check.Detail, "user:") {
				t.Fatalf("credential leaked in doctor detail: %q", check.Detail)
			}
		}
	})

	t.Run("reachable API but Git transport fails", func(t *testing.T) {
		r := fakeRunner{
			paths: map[string]string{"glab": "/usr/bin/glab", "git": "/usr/bin/git"},
			respond: func(cmd string) (Result, bool) {
				switch {
				case strings.HasPrefix(cmd, "glab auth status"):
					return Result{}, true
				case strings.Contains(cmd, "glab api") && strings.HasSuffix(cmd, " /version"):
					return Result{Stdout: []byte(`{"version":"1"}`)}, true
				case strings.Contains(cmd, "glab api") && strings.Contains(cmd, "projects?"):
					return Result{Stdout: []byte(projectsJSON)}, true
				case strings.HasPrefix(cmd, "git ls-remote"):
					return Result{Code: 128, Stderr: []byte("Permission denied (publickey)")}, true
				}
				return Result{}, false
			},
		}
		rep := Doctor(t.Context(), r, Config{Host: DefaultHost, Root: t.TempDir()})
		if got := findDoctorCheck(t, rep, "GitLab host/VPN reachable"); got.Status != StatusOK {
			t.Fatalf("reachability = %v, want OK", got.Status)
		}
		if got := findDoctorCheck(t, rep, "Git clone/fetch transport"); got.Status != StatusFail || !strings.Contains(got.Detail, "Permission denied") {
			t.Fatalf("transport = %+v, want distinct failure", got)
		}
	})
}

func TestDoctorManagedAgreementAndRecoveryInstruction(t *testing.T) {
	fixture := newManagedGitFixture(t)
	root := filepath.Join(t.TempDir(), "managed")
	cfg := Config{Host: DefaultHost, Root: root, Groups: []string{"g"}, Concurrency: 1}
	if _, err := InitManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project}); err != nil {
		t.Fatalf("InitManaged: %v", err)
	}

	checks := doctorManagedRoot(t.Context(), ExecRunner{}, cfg)
	if got := findCheck(t, checks, "lock/module/gitlink agreement"); got.Status != StatusOK {
		t.Fatalf("managed agreement = %+v, want OK", got)
	}

	modules := filepath.Join(root, ".gitmodules")
	data, err := os.ReadFile(modules)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), fixture.Project.PathWithNamespace, "g/wrong", 1))
	if err := os.WriteFile(modules, data, 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := LoadLock(root, catalog.Host)
	if err != nil {
		t.Fatal(err)
	}
	got := doctorManagedAgreement(t.Context(), ExecRunner{}, root, lock)
	if got.Status != StatusFail || !strings.Contains(got.Fix, "do not repair by deleting") {
		t.Fatalf("disagreement = %+v, want error with non-destructive recovery", got)
	}
}

func TestDoctorManagedCleanlinessScopesUntrackedFiles(t *testing.T) {
	newRoot := func(t *testing.T) (string, Lock) {
		t.Helper()
		fixture := newManagedGitFixture(t)
		root := filepath.Join(t.TempDir(), "managed")
		cfg := Config{Host: DefaultHost, Root: root, Groups: []string{"g"}, Concurrency: 1}
		if _, err := InitManaged(t.Context(), ExecRunner{}, cfg, []Project{fixture.Project}); err != nil {
			t.Fatalf("InitManaged: %v", err)
		}
		lock, err := LoadLock(root, DefaultHost)
		if err != nil {
			t.Fatalf("LoadLock: %v", err)
		}
		return root, lock
	}

	t.Run("untracked superproject noise is ignored", func(t *testing.T) {
		root, lock := newRoot(t)
		if err := os.WriteFile(filepath.Join(root, ".DS_Store"), []byte("finder metadata"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := doctorManagedCleanliness(t.Context(), ExecRunner{}, root, lock); got.Status != StatusOK {
			t.Fatalf("cleanliness = %+v, want root-level OS noise ignored", got)
		}
	})

	t.Run("untracked managed metadata fails closed", func(t *testing.T) {
		root, lock := newRoot(t)
		if err := os.WriteFile(filepath.Join(root, ManagedDirName, "local-state"), []byte("local"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := doctorManagedCleanliness(t.Context(), ExecRunner{}, root, lock); got.Status != StatusFail {
			t.Fatalf("cleanliness = %+v, want untracked managed metadata failure", got)
		}
	})

	t.Run("untracked submodule work fails closed", func(t *testing.T) {
		root, lock := newRoot(t)
		dest := filepath.Join(root, filepath.FromSlash(lock.Projects[0].PathWithNamespace))
		if err := os.WriteFile(filepath.Join(dest, "local-state"), []byte("local"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := doctorManagedCleanliness(t.Context(), ExecRunner{}, root, lock); got.Status != StatusFail ||
			!strings.Contains(got.Detail, lock.Projects[0].PathWithNamespace) || !strings.Contains(got.Detail, "local-state") {
			t.Fatalf("cleanliness = %+v, want untracked submodule failure", got)
		}
	})

	t.Run("generated MSBuild output is classified precisely", func(t *testing.T) {
		root, lock := newRoot(t)
		dest := filepath.Join(root, filepath.FromSlash(lock.Projects[0].PathWithNamespace))
		generated := filepath.Join(dest, "src", "obj", "Debug", ".NETFramework,Version=v4.8.AssemblyAttributes.cs")
		if err := os.MkdirAll(filepath.Dir(generated), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(generated, []byte("generated"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := doctorManagedCleanliness(t.Context(), ExecRunner{}, root, lock)
		if got.Status != StatusFail || !strings.Contains(got.Detail, "all are untracked MSBuild intermediates") ||
			!strings.Contains(got.Detail, lock.Projects[0].PathWithNamespace) {
			t.Fatalf("cleanliness = %+v, want classified MSBuild failure", got)
		}
	})
}

func findDoctorCheck(t *testing.T, rep Report, name string) Check {
	t.Helper()
	return findCheck(t, rep.Checks, name)
}

func findCheck(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, check := range checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("doctor check %q not found in %+v", name, checks)
	return Check{}
}

// ---------------------------------------------------------------------------
// clone
// ---------------------------------------------------------------------------

func TestCloneArgs(t *testing.T) {
	cfg := Config{Host: DefaultHost, Root: "/corpus"}
	p := Project{
		PathWithNamespace: "Services.Payment/TC.BillingApi",
		SSHURL:            "git@" + DefaultHost + ":Services.Payment/TC.BillingApi.git",
		DefaultBranch:     "main",
	}
	dest, args, err := CloneArgs(cfg, p)
	if err != nil {
		t.Fatalf("CloneArgs: %v", err)
	}
	wantDest := filepath.Join("/corpus", "Services.Payment", "TC.BillingApi")
	if dest != wantDest {
		t.Fatalf("dest = %q, want %q", dest, wantDest)
	}
	want := []string{"clone", "--depth", "1", "--single-branch", "--branch", "main", "--", p.SSHURL, wantDest}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}

	// A project with no default branch — the -b flag must be omitted (git clone
	// -b "" would fail). (Note: empty repos are skipped earlier via EmptyRepo; this
	// guards the blank-DefaultBranch edge regardless.)
	_, args2, err := CloneArgs(cfg, Project{PathWithNamespace: "g/empty", SSHURL: "git@" + DefaultHost + ":g/empty.git"})
	if err != nil {
		t.Fatalf("CloneArgs: %v", err)
	}
	for _, a := range args2 {
		if a == "--branch" {
			t.Fatalf("empty-branch repo should omit --branch: %v", args2)
		}
	}
}

// TestCloneArgs_PositionalsGuarded covers the argument-injection finding: the
// clone URL and destination are always separated from option-parsing by "--",
// and a project whose ssh_url_to_repo does not point at the pinned host is
// rejected before any args are built, so the host check can never be bypassed
// by an injected option that also smuggles in a matching "@host:" substring.
func TestCloneArgs_PositionalsGuarded(t *testing.T) {
	cfg := Config{Host: DefaultHost, Root: "/corpus"}

	t.Run("dash-prefixed url is rejected outright", func(t *testing.T) {
		p := Project{PathWithNamespace: "g/evil", SSHURL: "--upload-pack=touch /tmp/pwned;@" + DefaultHost + ":x"}
		_, _, err := CloneArgs(cfg, p)
		if err == nil {
			t.Fatal("want error for flag-shaped ssh url, got nil")
		}
	})

	t.Run("url pointing at an unpinned host is rejected", func(t *testing.T) {
		p := Project{PathWithNamespace: "g/other", SSHURL: "git@evil.example.com:g/other.git", DefaultBranch: "main"}
		_, _, err := CloneArgs(cfg, p)
		if err == nil {
			t.Fatal("want error for ssh url on an unpinned host, got nil")
		}
	})

	t.Run("legitimate url still produces a -- separated argv", func(t *testing.T) {
		p := Project{PathWithNamespace: "g/ok", SSHURL: "git@" + DefaultHost + ":g/ok.git", DefaultBranch: "main"}
		_, args, err := CloneArgs(cfg, p)
		if err != nil {
			t.Fatalf("CloneArgs: %v", err)
		}
		idx := indexOf(args, "--")
		if idx < 0 || idx != len(args)-3 {
			t.Fatalf("want \"--\" immediately before the two positional operands, got %v", args)
		}
	})
}

// TestCloneOne_RejectsUntrustedProject ensures an invalid project never reaches
// git at all — cloneOne must fail fast rather than exec'ing a crafted argv.
func TestCloneOne_RejectsUntrustedProject(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Host: DefaultHost, Root: root, Concurrency: 1}
	evil := Project{PathWithNamespace: "g/evil", SSHURL: "git@evil.example.com:g/evil.git", DefaultBranch: "main"}

	var calls []call
	r := fakeRunner{calls: &calls, callsMu: &sync.Mutex{}}

	rep := CloneProjects(context.Background(), r, cfg, []Project{evil}, nil)
	if rep.Failed != 1 || rep.Cloned != 0 {
		t.Fatalf("counts: cloned=%d failed=%d, want 0/1", rep.Cloned, rep.Failed)
	}
	if len(calls) != 0 {
		t.Fatalf("git must never be invoked for an untrusted project, got calls: %v", calls)
	}
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
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
		calls:   &calls,
		callsMu: &sync.Mutex{},
		fail:    func(cmd string) bool { return strings.Contains(cmd, "g/bad.git") },
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

// TestTallyCloneReport_CountsSumToResults guards the basic invariant a report
// consumer relies on: every result lands in exactly one bucket, so the four
// counts always sum to len(Results).
func TestTallyCloneReport_CountsSumToResults(t *testing.T) {
	results := []CloneResult{
		{Outcome: Cloned},
		{Outcome: Skipped},
		{Outcome: Failed},
		{Outcome: Empty},
		{Outcome: Empty},
	}
	rep := tallyCloneReport(results)
	sum := rep.Cloned + rep.Skipped + rep.Failed + rep.Empty
	if sum != len(results) {
		t.Fatalf("counts sum to %d, want %d: %+v", sum, len(results), rep)
	}
}

// TestTallyCloneReport_UnknownOutcomeNeverDropped is the regression for F-046:
// the per-outcome switch in tallyCloneReport had no default case, so a
// CloneOutcome value none of the cases recognize (e.g. a future addition to the
// enum that this switch wasn't updated for) silently contributed to none of the
// counts — the tally would then under-count len(Results) with no indication why.
// An unrecognized outcome must still be accounted for somewhere in the tally.
func TestTallyCloneReport_UnknownOutcomeNeverDropped(t *testing.T) {
	const unknownOutcome = CloneOutcome(99)
	results := []CloneResult{
		{Project: Project{PathWithNamespace: "g/a"}, Outcome: Cloned},
		{Project: Project{PathWithNamespace: "g/b"}, Outcome: unknownOutcome},
	}
	rep := tallyCloneReport(results)
	sum := rep.Cloned + rep.Skipped + rep.Failed + rep.Empty
	if sum != len(results) {
		t.Fatalf("counts sum to %d, want %d (unknown outcome was silently dropped): %+v", sum, len(results), rep)
	}
}

// TestCloneProjects_EmptyReposSkipped covers both ways an empty (no-commit) repo
// is recognized: the GitLab empty_repo flag (skip before git), and — defensively —
// the git "Remote branch … not found" failure when the flag was absent/stale. Both
// must yield the Empty outcome (never Failed), so an empty repo can never make the
// pass exit non-zero.
func TestCloneProjects_EmptyReposSkipped(t *testing.T) {
	root := t.TempDir()

	flagged := Project{PathWithNamespace: "g/empty-flag", SSHURL: "git@h:g/empty-flag.git", DefaultBranch: "main", EmptyRepo: true}
	good := Project{PathWithNamespace: "g/good", SSHURL: "git@h:g/good.git", DefaultBranch: "main"}
	// Not flagged empty, but git fails the way an empty repo does (flag was stale).
	stale := Project{PathWithNamespace: "g/empty-stale", SSHURL: "git@h:g/empty-stale.git", DefaultBranch: "main"}

	var calls []call
	r := fakeRunner{
		calls:   &calls,
		callsMu: &sync.Mutex{},
		respond: func(cmd string) (Result, bool) {
			if strings.Contains(cmd, "g/empty-stale.git") {
				return Result{Code: 128, Stderr: []byte("fatal: Remote branch main not found in upstream origin")}, true
			}
			return Result{}, false
		},
	}
	cfg := Config{Root: root, Concurrency: 4}

	rep := CloneProjects(context.Background(), r, cfg, []Project{flagged, good, stale}, nil)

	if rep.Cloned != 1 || rep.Empty != 2 || rep.Failed != 0 {
		t.Fatalf("counts: cloned=%d empty=%d failed=%d (want 1/2/0)", rep.Cloned, rep.Empty, rep.Failed)
	}
	if rep.Results[0].Outcome != Empty || rep.Results[1].Outcome != Cloned || rep.Results[2].Outcome != Empty {
		t.Fatalf("outcomes = %v %v %v", rep.Results[0].Outcome, rep.Results[1].Outcome, rep.Results[2].Outcome)
	}
	// The flag-detected empty repo must NOT invoke git at all.
	for _, c := range calls {
		if strings.Contains(c.cmd, "g/empty-flag.git") {
			t.Fatalf("flagged empty repo should not run git: %s", c.cmd)
		}
	}
}

// TestWithinRoot pins the containment primitive that gates every destructive
// filesystem op (clone destination, prune RemoveAll) against a path that
// escapes cfg.Root — e.g. via a server-supplied path_with_namespace containing
// ".." segments.
func TestWithinRoot(t *testing.T) {
	cases := []struct {
		name, root, dest string
		want             bool
	}{
		{"direct child", "/corpus", "/corpus/g/repo", true},
		{"deep nested", "/corpus", "/corpus/g/sub/repo", true},
		{"root itself", "/corpus", "/corpus", false},
		{"root with trailing slash", "/corpus/", "/corpus/g/repo", true},
		{"sibling prefix collision", "/corpus", "/corpus-evil/repo", false},
		{"traversal escapes", "/corpus", "/corpus/../etc/cron.d/evil", false},
		{"traversal lands outside entirely", "/corpus", "/etc/cron.d/evil", false},
		{"unclean dest still contained", "/corpus", "/corpus/g/../g/repo", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := withinRoot(c.root, c.dest); got != c.want {
				t.Errorf("withinRoot(%q, %q) = %v, want %v", c.root, c.dest, got, c.want)
			}
		})
	}
}

// TestCloneOne_RejectsPathTraversal covers F-clone: a malicious
// path_with_namespace containing ".." must never reach `git clone` — the
// computed destination escapes cfg.Root, so it must be reported Failed up
// front instead.
func TestCloneOne_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	evil := Project{
		PathWithNamespace: "../../../../tmp/moedex-evil-clone",
		SSHURL:            "git@h:evil.git",
		DefaultBranch:     "main",
	}
	var calls []call
	r := fakeRunner{calls: &calls}
	cfg := Config{Root: root, Concurrency: 1}

	rep := CloneProjects(context.Background(), r, cfg, []Project{evil}, nil)

	if rep.Failed != 1 || rep.Cloned != 0 {
		t.Fatalf("counts: cloned=%d failed=%d (want 0/1)", rep.Cloned, rep.Failed)
	}
	if rep.Results[0].Outcome != Failed {
		t.Fatalf("outcome = %v, want Failed", rep.Results[0].Outcome)
	}
	for _, c := range calls {
		if strings.Contains(c.cmd, "clone") {
			t.Fatalf("git clone must never run for a path-traversal namespace: %s", c.cmd)
		}
	}
}

// TestUpdateOne_RejectsPathTraversal covers the fetch/reset side of the same
// gate: a project whose computed destination escapes cfg.Root must fail before
// any git command runs against it.
func TestUpdateOne_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	evil := Project{
		PathWithNamespace: "../../../../tmp/moedex-evil-update",
		SSHURL:            "git@h:evil.git",
		DefaultBranch:     "main",
	}
	var calls []call
	r := fakeRunner{calls: &calls}
	cfg := Config{Root: root}

	res := updateOne(context.Background(), r, cfg, evil)

	if res.Outcome != SyncFailed {
		t.Fatalf("outcome = %v, want SyncFailed", res.Outcome)
	}
	for _, c := range calls {
		if strings.Contains(c.cmd, "git") {
			t.Fatalf("git must never run for a path-traversal namespace: %s", c.cmd)
		}
	}
}

// TestUpdateOne_RejectsFlagShapedRef covers the other half of the argv-injection
// finding: DefaultBranch is fetched as a bare positional ref
// (`git fetch origin <ref>`) with no preceding flag name, so a server record
// whose default_branch begins with "-" must never reach git at all.
func TestUpdateOne_RejectsFlagShapedRef(t *testing.T) {
	root := filepath.Join(t.TempDir(), "g", "evil")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Root: filepath.Dir(filepath.Dir(root))}
	evil := Project{
		PathWithNamespace: "g/evil",
		SSHURL:            "git@" + DefaultHost + ":g/evil.git",
		DefaultBranch:     "--upload-pack=touch /tmp/pwned",
	}
	var calls []call
	r := fakeRunner{calls: &calls}

	res := updateOne(context.Background(), r, cfg, evil)

	if res.Outcome != SyncFailed {
		t.Fatalf("outcome = %v, want SyncFailed", res.Outcome)
	}
	if len(calls) != 0 {
		t.Fatalf("git must never run for a flag-shaped ref, got calls: %v", calls)
	}
}

// TestUpdateOne_FetchUsesPositionalGuard pins the exact fetch argv: "--" must
// separate the positional ref from option parsing.
func TestUpdateOne_FetchUsesPositionalGuard(t *testing.T) {
	root := filepath.Join(t.TempDir(), "g", "ok")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Root: filepath.Dir(filepath.Dir(root))}
	p := Project{
		PathWithNamespace: "g/ok",
		SSHURL:            "git@" + DefaultHost + ":g/ok.git",
		DefaultBranch:     "main",
	}
	var calls []call
	r := fakeRunner{calls: &calls}

	updateOne(context.Background(), r, cfg, p)

	var sawFetch bool
	for _, c := range calls {
		if strings.Contains(c.cmd, "fetch") {
			sawFetch = true
			if !strings.Contains(c.cmd, "origin -- main") {
				t.Fatalf("fetch must guard the positional ref with --, got: %s", c.cmd)
			}
		}
	}
	if !sawFetch {
		t.Fatal("fetch was never invoked")
	}
}

// TestPruneOne_RejectsPathTraversal covers the destructive prune path: even
// though LocalRepos cannot itself produce a ".."-bearing rel today, the
// RemoveAll must refuse to act if the computed destination would land outside
// cfg.Root, rather than trusting the join. Proven by escaping to a real
// directory outside root and asserting it survives.
func TestPruneOne_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	victim := t.TempDir()
	sentinel := filepath.Join(victim, "must-survive")
	if err := os.WriteFile(sentinel, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, victim)
	if err != nil {
		t.Fatal(err)
	}
	rel = filepath.ToSlash(rel)

	res := pruneOne(Config{Root: root}, rel)

	if res.Outcome != SyncFailed {
		t.Fatalf("outcome = %v, want SyncFailed", res.Outcome)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("path traversal prune deleted outside root: %v", err)
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

func TestReconcile_EmptyAllowlistNeverPrunes(t *testing.T) {
	// No groups enumerated (allow is empty): every local repo is out of scope by
	// definition, so none of them may be flagged Missing — empty allow must mean
	// "nothing is in scope," not "everything is in scope."
	local := []string{
		"Services.Payment/A",
		"old-svn-repos/legacy",
	}
	plan := Reconcile(nil, local, nil)
	if len(plan.Missing) != 0 {
		t.Fatalf("Missing = %v, want none (empty allowlist must prune nothing)", plan.Missing)
	}
}

func TestSyncProjects_EmptyGroupsNeverPrunes(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Root: root, Groups: nil, Concurrency: 2}
	if err := os.MkdirAll(filepath.Join(root, "g", "gone", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	rep, err := SyncProjects(context.Background(), fakeRunner{}, cfg, nil, true /*prune*/, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pruned != 0 {
		t.Fatalf("want 0 pruned with empty Groups even when prune=true; got pruned=%d missing=%d", rep.Pruned, rep.Missing)
	}
	if _, err := os.Stat(filepath.Join(root, "g", "gone")); err != nil {
		t.Errorf("repo must survive when Groups is empty, regardless of prune: %v", err)
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
	r := fakeRunner{} // moedex not on PATH
	_, err := Reindex(context.Background(), r, ReindexOptions{
		Root: "/corpus", CASDir: t.TempDir(), ShardDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "moedex") {
		t.Fatalf("want missing-binary error, got %v", err)
	}
}

func TestReindex_DefaultUsesUnifiedSemanticCommands(t *testing.T) {
	var calls []call
	r := fakeRunner{paths: map[string]string{"moedex": "/bin/moedex"}, calls: &calls}
	_, err := Reindex(context.Background(), r, ReindexOptions{
		Root: "/corpus", CASDir: t.TempDir(), ShardDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !strings.Contains(calls[0].cmd, "moedex index cas build") || !strings.Contains(calls[1].cmd, "moedex index cas export") {
		t.Fatalf("unified reindex calls = %v", calls)
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
