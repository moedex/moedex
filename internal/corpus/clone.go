package corpus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// CloneOutcome categorizes what happened to one project during a clone pass.
type CloneOutcome int

const (
	// Cloned means git clone ran and succeeded.
	Cloned CloneOutcome = iota
	// Skipped means a clone already existed at the destination (idempotent re-run).
	Skipped
	// Failed means git clone could not complete; CloneResult.Err says why.
	Failed
	// Empty means the project has no commits (no real branch): nothing to clone
	// and nothing to index. It is NOT a failure — empty repos are expected on the
	// server and must not make a clone/sync pass exit non-zero.
	Empty
)

// CloneResult is the outcome for a single project.
type CloneResult struct {
	Project Project
	Outcome CloneOutcome
	Err     error  // set when Failed
	Detail  string // short stderr tail when Failed, for the operator report
}

// CloneReport aggregates a whole clone pass.
type CloneReport struct {
	Results []CloneResult
	Cloned  int
	Skipped int
	Failed  int
	Empty   int
}

// Failures returns just the failed results, for the end-of-run summary.
func (r CloneReport) Failures() []CloneResult {
	var out []CloneResult
	for _, res := range r.Results {
		if res.Outcome == Failed {
			out = append(out, res)
		}
	}
	return out
}

// gitEnv makes git non-interactive and cheap for bulk mirroring (used by both
// clone and sync's fetch):
//   - GIT_LFS_SKIP_SMUDGE=1   — don't download LFS blobs (binary; the indexer skips them anyway)
//   - GIT_TERMINAL_PROMPT=0   — never block on a credential prompt; fail fast instead
//   - GIT_SSH_COMMAND=...     — BatchMode (no password prompt) + accept-new (auto-trust the
//     host key on first contact with the internal GitLab, so a fresh machine doesn't hang
//     on the interactive "authenticity of host" question)
var gitEnv = []string{
	"GIT_LFS_SKIP_SMUDGE=1",
	"GIT_TERMINAL_PROMPT=0",
	"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new",
}

// Dest returns the local path a project clones to: the corpus root joined with
// the project's full namespace path, so the mirror layout matches GitLab exactly
// (and the engine's per-repo discovery sees the same structure). PathWithNamespace
// is server-supplied data — filepath.Join silently resolves any ".." it contains,
// so callers that act on the result (clone, fetch/reset, prune) MUST gate it
// through withinRoot before touching the filesystem; Dest itself stays a pure join
// so its output remains simple to unit test.
func (cfg Config) Dest(p Project) string {
	return filepath.Join(cfg.Root, filepath.FromSlash(p.PathWithNamespace))
}

// errUnsafeDest is returned when a computed destination would land outside
// cfg.Root — e.g. a path_with_namespace containing ".." segments, or an empty
// one that collapses onto cfg.Root itself.
var errUnsafeDest = errors.New("destination escapes corpus root")

// withinRoot reports whether dest is strictly contained under root once both are
// cleaned (dest itself equaling root is rejected too — Dest/prune destinations
// are always a sub-path, never the root). This is the actual containment gate:
// GitLab's naming rules forbid ".." in a namespace path today, but nothing in
// this package enforces that independently, so the join must never be trusted.
func withinRoot(root, dest string) bool {
	root = filepath.Clean(root)
	dest = filepath.Clean(dest)
	return dest != root && strings.HasPrefix(dest, root+string(filepath.Separator))
}

// errUntrustedProject is returned when a project's server-supplied fields fail
// validateProject — an ssh_url_to_repo that doesn't point at the pinned host, or
// a value shaped like a command-line flag rather than data.
var errUntrustedProject = errors.New("untrusted project")

// validateProject rejects a Project whose server-supplied fields could be
// mistaken for git options rather than the data they're meant to be — defense
// in depth alongside the "--" separator in CloneArgs and updateOne's fetch, and
// on top of the fact that GitLab (not an attacker) normally generates these
// fields server-side:
//   - SSHURL must not look like a flag (a real ssh_url_to_repo never starts
//     with "-") and must name the pinned host, so a clone can never be
//     redirected to an arbitrary remote.
//   - DefaultBranch, used as a positional ref in updateOne's `git fetch`, must
//     not look like a flag either.
func validateProject(cfg Config, p Project) error {
	if strings.HasPrefix(p.SSHURL, "-") {
		return fmt.Errorf("%w: ssh url %q looks like a flag", errUntrustedProject, p.SSHURL)
	}
	if cfg.Host != "" {
		if host := sshURLHost(p.SSHURL); host != cfg.Host {
			return fmt.Errorf("%w: ssh url %q does not point at the pinned host %q", errUntrustedProject, p.SSHURL, cfg.Host)
		}
	}
	return validRef(p.DefaultBranch)
}

// validRef rejects a branch/ref that looks like a flag rather than a name — it
// guards updateOne's `git fetch origin <ref>`, where ref is a bare positional
// argument with no preceding flag name to force it to be read as data.
func validRef(ref string) error {
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("%w: ref %q looks like a flag", errUntrustedProject, ref)
	}
	return nil
}

// sshURLHost extracts the host from an SSH git URL, accepting both SCP-like
// syntax ("git@host:path") and full ssh:// syntax ("ssh://git@host[:port]/path").
// It returns "" if no "user@host" form is found.
func sshURLHost(sshURL string) string {
	sshURL = strings.TrimPrefix(sshURL, "ssh://")
	_, rest, ok := strings.Cut(sshURL, "@")
	if !ok {
		return ""
	}
	if end := strings.IndexAny(rest, ":/"); end >= 0 {
		return rest[:end]
	}
	return rest
}

// CloneArgs builds the git argument vector for a shallow, single-branch clone of
// p into its destination under cfg.Root. The branch flag is included only when
// the project advertises a default branch (an empty repo has none, and `-b ""`
// would fail). The clone URL and destination are separated from option parsing
// by "--", and p is validated first (see validateProject) — so the returned argv
// can never have an attacker-controlled positional operand mistaken for a flag.
// It is otherwise pure, so the exact command is unit-tested.
func CloneArgs(cfg Config, p Project) (dest string, args []string, err error) {
	dest = cfg.Dest(p)
	if err := validateProject(cfg, p); err != nil {
		return dest, nil, err
	}
	args = []string{"clone", "--depth", "1", "--single-branch"}
	if p.DefaultBranch != "" {
		args = append(args, "--branch", p.DefaultBranch)
	}
	args = append(args, "--", p.SSHURL, dest)
	return dest, args, nil
}

// alreadyCloned reports whether dest looks like a git working tree already (a
// .git entry, dir or gitlink file). Clone is idempotent: an existing repo is
// left untouched and reported Skipped (freshening it is sync's job, Phase 3).
func alreadyCloned(dest string) bool {
	_, err := os.Stat(filepath.Join(dest, ".git"))
	return err == nil
}

// HasClone reports whether project p is already present under cfg.Root. Exposed
// so callers (e.g. a clone dry-run) can preview new-vs-present without running git.
func (cfg Config) HasClone(p Project) bool {
	return alreadyCloned(cfg.Dest(p))
}

// cloneOne clones a single project, returning its result. It never returns an
// error itself — failures are captured in the result so the fan-out continues.
func cloneOne(ctx context.Context, r Runner, cfg Config, p Project) CloneResult {
	dest, args, err := CloneArgs(cfg, p)
	if err != nil {
		return CloneResult{Project: p, Outcome: Failed, Err: err}
	}
	if !withinRoot(cfg.Root, dest) {
		return CloneResult{Project: p, Outcome: Failed, Err: errUnsafeDest, Detail: dest}
	}
	if alreadyCloned(dest) {
		return CloneResult{Project: p, Outcome: Skipped}
	}
	// A repo with no commits has no real branch to clone (despite GitLab still
	// advertising a nominal DefaultBranch). Skip it up front — there is nothing to
	// index, and attempting the clone would only fail.
	if p.EmptyRepo {
		return CloneResult{Project: p, Outcome: Empty}
	}
	res, err := r.RunEnv(ctx, gitEnv, "git", args...)
	if err != nil {
		return CloneResult{Project: p, Outcome: Failed, Err: err}
	}
	if !res.Ok() {
		// Defense-in-depth: if the repo turned out to be empty after all (the
		// empty_repo flag was stale, or it was emptied between enumerate and
		// clone), git reports the missing branch. Treat that as Empty, not Failed,
		// so it never trips the non-zero exit.
		if isEmptyRepoGitErr(res.Stderr) {
			return CloneResult{Project: p, Outcome: Empty}
		}
		return CloneResult{
			Project: p, Outcome: Failed,
			Err:    errGitExit(res.Code),
			Detail: lastLine(res.Stderr),
		}
	}
	return CloneResult{Project: p, Outcome: Cloned}
}

// isEmptyRepoGitErr reports whether a git clone failure is the "no commits yet"
// signature: `git clone --branch <b>` of a repo with no real branch fails with
// "Remote branch <b> not found in upstream origin".
func isEmptyRepoGitErr(stderr []byte) bool {
	return bytes.Contains(stderr, []byte("Remote branch")) &&
		bytes.Contains(stderr, []byte("not found in upstream origin"))
}

// errGitExit describes a git process that ran but exited non-zero.
func errGitExit(code int) error { return fmt.Errorf("git exited %d", code) }

// CloneProjects clones every project into cfg.Root with up to cfg.Concurrency
// workers running at once — Moe reaching out with many tentacles. It is
// continue-on-error: one repo's failure never aborts the pass. progress, if
// non-nil, is called once per finished project (from worker goroutines, so it
// must be safe for concurrent use) for live output. Results preserve the input
// order regardless of completion order.
func CloneProjects(ctx context.Context, r Runner, cfg Config, projects []Project, progress func(CloneResult)) CloneReport {
	conc := cfg.Concurrency
	if conc < 1 {
		conc = 1
	}
	if conc > len(projects) && len(projects) > 0 {
		conc = len(projects)
	}

	results := make([]CloneResult, len(projects))
	type job struct {
		idx int
		p   Project
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	for w := 0; w < conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				res := cloneOne(ctx, r, cfg, j.p)
				results[j.idx] = res
				if progress != nil {
					progress(res)
				}
			}
		}()
	}
	for i, p := range projects {
		jobs <- job{i, p}
	}
	close(jobs)
	wg.Wait()

	return tallyCloneReport(results)
}

// tallyCloneReport builds a CloneReport's per-outcome counts from results.
// Every result lands in exactly one bucket: an outcome this switch doesn't
// recognize (e.g. a future CloneOutcome this function wasn't updated for) is
// conservatively counted as Failed rather than silently contributing to none
// of the counts, so the tally always sums to len(results) and a forgotten case
// surfaces as a visible failure instead of an inexplicable under-count.
func tallyCloneReport(results []CloneResult) CloneReport {
	rep := CloneReport{Results: results}
	for _, res := range results {
		switch res.Outcome {
		case Cloned:
			rep.Cloned++
		case Skipped:
			rep.Skipped++
		case Failed:
			rep.Failed++
		case Empty:
			rep.Empty++
		default:
			rep.Failed++
		}
	}
	return rep
}

// lastLine returns the last non-empty line of b, trimmed — the most useful part
// of a git failure for a one-line report.
func lastLine(b []byte) string { return nonEmptyLine(b, false) }
