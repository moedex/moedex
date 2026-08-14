package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunRepoDiscoveryMatchesEngineSemantics guards F-057 and the managed-corpus
// discovery contract: scale must count repos exactly as ingest.DiscoverRepos
// does, including a worktree/submodule whose .git marker is a regular gitfile.
func TestRunRepoDiscoveryMatchesEngineSemantics(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("MOEDEX_SELECTIVE", "")
	t.Setenv("MOEDEX_MMAP", "")

	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	gitInitRepo(t, realDir, "f.go", "package real\n")

	// A linked worktree uses a regular .git file that points at the primary
	// repository's metadata, matching the shape of a checked-out submodule.
	gitfileDir := filepath.Join(root, "gitfile-worktree")
	cmd := exec.Command("git", "-C", realDir, "worktree", "add", "-q", "--detach", gitfileDir, "HEAD")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}

	out := captureStdout(t, func() {
		if err := run(root, ""); err != nil {
			t.Fatalf("run: %v", err)
		}
	})

	want := "found 2 git repos under " + root
	if !strings.Contains(out, want) {
		t.Fatalf("output does not count both directory and gitfile repositories:\n%s\nwant line containing %q", out, want)
	}
}

// TestRunReturnsErrorOnSampleQueryFailure guards F-058: a malformed sample
// regex must surface as a returned (and exit-worthy) error, not a silent
// success.
func TestRunReturnsErrorOnSampleQueryFailure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("MOEDEX_SELECTIVE", "")
	t.Setenv("MOEDEX_MMAP", "")

	root := t.TempDir()
	gitInitRepo(t, filepath.Join(root, "real"), "f.go", "package real\n")

	var err error
	captureStdout(t, func() {
		err = run(root, "(") // unmatched paren: invalid regex
	})
	if err == nil {
		t.Fatal("run returned nil error for an invalid sample regex; query failures must not be silent")
	}
}

// TestVersionRequested is the regression for F-082: scale (unlike moedex-serve,
// moedex-index, moedex-corpus) had no way to print its build identity, so a
// PARITY-REPORT.md-style scale run couldn't be tied back to the binary that
// produced it.
func TestVersionRequested(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"no args", nil, false},
		{"-version", []string{"-version"}, true},
		{"--version", []string{"--version"}, true},
		{"a root path", []string{"/some/root"}, false},
		{"root then sample regex", []string{"/some/root", "foo"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := versionRequested(tc.args); got != tc.want {
				t.Errorf("versionRequested(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func gitInitRepo(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@moedex.local",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@moedex.local",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("add", "-A")
	run("commit", "-q", "-m", "init")
}

// captureStdout redirects os.Stdout for the duration of fn and returns what
// was written. Tests in this package don't run in parallel, so the global
// redirect is safe.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}
