package ingest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
