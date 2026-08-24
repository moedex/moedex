package sourcehash

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDigestTracksWorktreeContentModesAndDeletions(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "fixture@example.com")
	git(t, dir, "config", "user.name", "Fixture")
	tracked := filepath.Join(dir, "tracked.go")
	writeFile(t, tracked, []byte("package fixture\n"), 0o644)
	git(t, dir, "add", "tracked.go")
	git(t, dir, "commit", "-qm", "fixture")

	base := digest(t, dir)
	if got := digest(t, dir); got != base {
		t.Fatalf("unchanged digest=%q want %q", got, base)
	}

	writeFile(t, tracked, []byte("package changed\n"), 0o644)
	changed := digest(t, dir)
	if changed == base {
		t.Fatal("content change did not change digest")
	}
	git(t, dir, "add", "tracked.go")
	if got := digest(t, dir); got != changed {
		t.Fatalf("staging changed source identity: got %q want %q", got, changed)
	}

	if err := os.Chmod(tracked, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := digest(t, dir); got == changed {
		t.Fatal("executable mode change did not change digest")
	}

	untracked := filepath.Join(dir, "new.go")
	writeFile(t, untracked, []byte("package fixture\n"), 0o644)
	withUntracked := digest(t, dir)
	if err := os.Remove(untracked); err != nil {
		t.Fatal(err)
	}
	if got := digest(t, dir); got == withUntracked {
		t.Fatal("untracked content did not change digest")
	}

	if err := os.Remove(tracked); err != nil {
		t.Fatal(err)
	}
	if got := digest(t, dir); got == base {
		t.Fatal("tracked deletion did not change digest")
	}
}

func TestCheckClean(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "fixture@example.com")
	git(t, dir, "config", "user.name", "Fixture")
	writeFile(t, filepath.Join(dir, "tracked.go"), []byte("package fixture\n"), 0o644)
	git(t, dir, "add", "tracked.go")
	git(t, dir, "commit", "-qm", "fixture")
	if err := CheckClean(context.Background(), dir); err != nil {
		t.Fatalf("clean worktree rejected: %v", err)
	}
	writeFile(t, filepath.Join(dir, "untracked.go"), []byte("package fixture\n"), 0o644)
	if err := CheckClean(context.Background(), dir); err == nil {
		t.Fatal("dirty worktree accepted")
	}
}

func digest(t *testing.T, dir string) string {
	t.Helper()
	got, err := Digest(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func writeFile(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}
