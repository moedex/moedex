package corpus

import (
	"context"
	"strings"
	"testing"
)

// fakeRunner is a programmable Runner for tests. paths maps an executable name to
// the path LookPath returns (absent name → not found). runs maps a command-line
// prefix to the Result Run returns; the first registered prefix that matches the
// joined "name args..." wins, so callers can register "glab auth status" and
// ignore trailing flags.
type fakeRunner struct {
	paths map[string]string
	runs  map[string]Result
}

func (f fakeRunner) LookPath(name string) (string, error) {
	if p, ok := f.paths[name]; ok && p != "" {
		return p, nil
	}
	return "", errNotFound
}

func (f fakeRunner) Run(_ context.Context, name string, args ...string) (Result, error) {
	cmd := strings.TrimSpace(name + " " + strings.Join(args, " "))
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
