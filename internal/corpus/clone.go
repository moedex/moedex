package corpus

import (
	"context"
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
// (and the engine's per-repo discovery sees the same structure).
func (cfg Config) Dest(p Project) string {
	return filepath.Join(cfg.Root, filepath.FromSlash(p.PathWithNamespace))
}

// CloneArgs builds the git argument vector for a shallow, single-branch clone of
// p into its destination under cfg.Root. The branch flag is included only when
// the project advertises a default branch (an empty repo has none, and `-b ""`
// would fail). It is pure, so the exact command is unit-tested.
func CloneArgs(cfg Config, p Project) (dest string, args []string) {
	dest = cfg.Dest(p)
	args = []string{"clone", "--depth", "1", "--single-branch"}
	if p.DefaultBranch != "" {
		args = append(args, "--branch", p.DefaultBranch)
	}
	args = append(args, p.SSHURL, dest)
	return dest, args
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
	dest, args := CloneArgs(cfg, p)
	if alreadyCloned(dest) {
		return CloneResult{Project: p, Outcome: Skipped}
	}
	res, err := r.RunEnv(ctx, gitEnv, "git", args...)
	if err != nil {
		return CloneResult{Project: p, Outcome: Failed, Err: err}
	}
	if !res.Ok() {
		return CloneResult{
			Project: p, Outcome: Failed,
			Err:    errGitExit(res.Code),
			Detail: lastLine(res.Stderr),
		}
	}
	return CloneResult{Project: p, Outcome: Cloned}
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

	rep := CloneReport{Results: results}
	for _, res := range results {
		switch res.Outcome {
		case Cloned:
			rep.Cloned++
		case Skipped:
			rep.Skipped++
		case Failed:
			rep.Failed++
		}
	}
	return rep
}

// lastLine returns the last non-empty line of b, trimmed — the most useful part
// of a git failure for a one-line report.
func lastLine(b []byte) string {
	lines := strings.Split(string(b), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return ""
}
