package ingest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitRepo creates a temp dir, runs `git init`, writes the given files, and
// `git add`s them so they appear in `git ls-files -s`. files maps a relative
// path to its byte content. It returns the repo dir.
func gitRepo(t *testing.T, files map[string][]byte) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		// Keep git from depending on the user's global config/identity.
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	for rel, content := range files {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	return dir
}

// byRel indexes a slice of Files by RelPath for convenient assertions.
func byRel(files []File) map[string]File {
	m := make(map[string]File, len(files))
	for _, f := range files {
		m[f.RelPath] = f
	}
	return m
}

func TestRepoReturnsTextFile(t *testing.T) {
	content := []byte("hello\nworld\n")
	dir := gitRepo(t, map[string][]byte{"main.go": content})

	files, err := Repo("myrepo", dir)
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	m := byRel(files)
	f, ok := m["main.go"]
	if !ok {
		t.Fatalf("main.go not returned; got %v", files)
	}
	if string(f.Content) != string(content) {
		t.Errorf("Content = %q, want %q", f.Content, content)
	}
	if f.Repo != "myrepo" {
		t.Errorf("Repo label = %q, want %q", f.Repo, "myrepo")
	}
	if f.AbsPath != filepath.Join(dir, "main.go") {
		t.Errorf("AbsPath = %q, want %q", f.AbsPath, filepath.Join(dir, "main.go"))
	}
	// git blob SHA-1 is 40 lowercase hex chars.
	if len(f.SHA) != 40 {
		t.Errorf("SHA = %q, want 40 hex chars", f.SHA)
	}
	for _, c := range f.SHA {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("SHA = %q contains non-hex char %q", f.SHA, c)
			break
		}
	}
}

func TestRepoSkipsBinary(t *testing.T) {
	dir := gitRepo(t, map[string][]byte{
		"text.txt":   []byte("plain text"),
		"binary.dat": {0x00, 0x01, 0x02, 'a', 'b'}, // contains a NUL byte
	})

	files, err := Repo("r", dir)
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	m := byRel(files)
	if _, ok := m["binary.dat"]; ok {
		t.Errorf("binary.dat should be skipped (contains NUL), got it in results")
	}
	if _, ok := m["text.txt"]; !ok {
		t.Errorf("text.txt should be included")
	}
}

func TestRepoStripsBOM(t *testing.T) {
	bom := []byte{0xEF, 0xBB, 0xBF}
	body := []byte("package main\n")
	dir := gitRepo(t, map[string][]byte{"bom.go": append(append([]byte{}, bom...), body...)})

	files, err := Repo("r", dir)
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	m := byRel(files)
	f, ok := m["bom.go"]
	if !ok {
		t.Fatalf("bom.go not returned")
	}
	if string(f.Content) != string(body) {
		t.Errorf("Content = %q, want BOM stripped to %q", f.Content, body)
	}
}

func TestRepoSkipsFileMissingFromWorktree(t *testing.T) {
	dir := gitRepo(t, map[string][]byte{
		"keep.txt": []byte("kept"),
		"gone.txt": []byte("deleted from worktree"),
	})
	// Remove from the working tree but leave it tracked in the index.
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}

	files, err := Repo("r", dir)
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	m := byRel(files)
	if _, ok := m["gone.txt"]; ok {
		t.Errorf("gone.txt should be skipped (tracked but absent from worktree)")
	}
	if _, ok := m["keep.txt"]; !ok {
		t.Errorf("keep.txt should still be present")
	}
}

func TestRepoErrorsOnNonGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir() // not a git repo
	if _, err := Repo("r", dir); err == nil {
		t.Fatal("expected error for non-git directory, got nil")
	}
}

func TestRepoDedupsContentBySHANo(t *testing.T) {
	// Two paths with identical content share a git blob SHA. Repo returns one
	// File per tracked path (dedup is the index's job, not ingest's), but both
	// must carry the same SHA.
	same := []byte("identical contents\n")
	dir := gitRepo(t, map[string][]byte{
		"a.txt": same,
		"b.txt": same,
	})

	files, err := Repo("r", dir)
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	m := byRel(files)
	a, okA := m["a.txt"]
	b, okB := m["b.txt"]
	if !okA || !okB {
		t.Fatalf("expected both a.txt and b.txt, got %v", files)
	}
	if a.SHA != b.SHA {
		t.Errorf("identical content should share a SHA: a=%q b=%q", a.SHA, b.SHA)
	}
}
