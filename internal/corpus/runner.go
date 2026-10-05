// Package corpus owns moedex's corpus acquisition and freshness: the setup
// experience that, on a fresh machine, checks for the glab CLI, helps the
// operator authenticate to configured's internal GitLab ONLY, and clones every
// project their access allows into the live corpus that the engine then indexes
// and keeps fresh.
//
// This package is deliberately separate from the retrieval engine. The engine
// (internal/index, internal/search, the serving spine) is pure-Go and
// zero-dependency by identity; corpus acquisition is inherently not — it shells
// out to glab, git, and moedex-index, and talks to a network. Keeping that here,
// behind the Runner seam, preserves the engine's posture: nothing under
// internal/* except this package imports an external tool, and the engine never
// imports this package.
//
// Everything that can be is pure and unit-testable: project enumeration parsing,
// the group allowlist filter, and the doctor decision tree all take a Runner, so
// tests exercise them with a fake instead of a real glab/git/network. Only
// ExecRunner actually launches subprocesses.
//
// The whole package is pinned to a single configured host per operation — it never
// authenticates to or clones from anywhere else.
package corpus

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
)

var credentialRedactors = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`),
	regexp.MustCompile(`(?i)(authorization:\s*(?:bearer|basic)\s+)[^\s]+`),
	regexp.MustCompile(`(?i)((?:private[-_]?token|access[-_]?token|token)=)[^&\s]+`),
	regexp.MustCompile(`(?i)glpat-[a-z0-9_-]+`),
}

// Runner abstracts the external commands the corpus tool drives — glab, git, and
// moedex-index — so the orchestration logic can be unit-tested without a network,
// a GitLab account, or those binaries installed. ExecRunner is the production
// implementation; tests use a fake.
type Runner interface {
	// LookPath reports the absolute path of an executable on PATH, or an error if
	// it is not found (mirrors os/exec.LookPath).
	LookPath(name string) (string, error)

	// Run executes name with args and returns the captured Result. A non-zero
	// process exit is reported in Result.Code with a NIL error — that is a normal
	// outcome callers inspect (e.g. `glab auth status` exits non-zero when not
	// logged in). A non-nil error means the process could not be run to
	// completion (binary missing, context cancelled, I/O failure), not merely that
	// it failed.
	Run(ctx context.Context, name string, args ...string) (Result, error)

	// RunEnv is Run with extra environment variables ("KEY=value") layered on top
	// of the parent process environment. Used by the clone/sync paths to make git
	// non-interactive and skip LFS smudging. Run is equivalent to RunEnv with a
	// nil env.
	RunEnv(ctx context.Context, env []string, name string, args ...string) (Result, error)
}

// Result is the outcome of a Runner.Run call.
type Result struct {
	Stdout []byte
	Stderr []byte
	Code   int // process exit code; 0 on success
}

// Ok reports whether the process exited 0.
func (r Result) Ok() bool { return r.Code == 0 }

// ExecRunner is the production Runner backed by os/exec.
type ExecRunner struct{}

// LookPath implements Runner.
func (ExecRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

// Run implements Runner (no extra environment).
func (e ExecRunner) Run(ctx context.Context, name string, args ...string) (Result, error) {
	return e.RunEnv(ctx, nil, name, args...)
}

// RunEnv implements Runner. It captures stdout and stderr separately and
// translates a clean non-zero exit into Result.Code (nil error), reserving the
// error return for failures that prevented the process from running to
// completion. Extra env entries are appended to os.Environ.
func (ExecRunner) RunEnv(ctx context.Context, env []string, name string, args ...string) (Result, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.Code = ee.ExitCode()
			return res, nil // ran to completion, just exited non-zero
		}
		return res, err // could not run
	}
	return res, nil
}

// redactDiagnostic removes common credential forms from external-command
// diagnostics before they are returned to an operator or persisted in logs.
func redactDiagnostic(value string) string {
	redacted := value
	for i, pattern := range credentialRedactors {
		replacement := "[REDACTED]"
		if i < 3 {
			replacement = "${1}[REDACTED]"
		}
		redacted = pattern.ReplaceAllString(redacted, replacement)
	}
	return redacted
}
