package corpus

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"moedex/internal/ingest"
)

// SyncOutcome categorizes what happened to one repo during a sync pass.
type SyncOutcome int

const (
	// SyncCloned: a newly-created server repo was cloned.
	SyncCloned SyncOutcome = iota
	// SyncUpdated: an existing repo advanced to a new commit.
	SyncUpdated
	// SyncCurrent: an existing repo was already at the remote tip (no change).
	SyncCurrent
	// SyncPruned: a local repo no longer on the server was removed (only with prune).
	SyncPruned
	// SyncMissing: a local repo is gone/inaccessible on the server; reported, NOT removed.
	SyncMissing
	// SyncFailed: a git operation (clone/fetch/reset) or a prune removal failed.
	SyncFailed
)

// SyncResult is the outcome for a single repo.
type SyncResult struct {
	Path    string // PathWithNamespace (clone/update) or local rel path (missing/pruned)
	Outcome SyncOutcome
	Err     error
	Detail  string
}

// SyncReport aggregates a whole sync pass.
type SyncReport struct {
	Results []SyncResult
	Cloned  int
	Updated int
	Current int
	Pruned  int
	Missing int
	Failed  int
}

// Failures returns just the failed results, for the end-of-run summary.
func (r SyncReport) Failures() []SyncResult {
	var out []SyncResult
	for _, res := range r.Results {
		if res.Outcome == SyncFailed {
			out = append(out, res)
		}
	}
	return out
}

// MissingRepos returns the repos present locally but gone on the server (only
// populated when prune was off), for the operator to review.
func (r SyncReport) MissingRepos() []SyncResult {
	var out []SyncResult
	for _, res := range r.Results {
		if res.Outcome == SyncMissing {
			out = append(out, res)
		}
	}
	return out
}

// SyncPlan is the reconciliation of the enumerated curated set against what is on
// disk: what to clone (new on the server), what to update (present both), and
// what is missing (on disk, in scope, but gone from the server).
type SyncPlan struct {
	ToClone  []Project // enumerated but absent locally
	ToUpdate []Project // enumerated and present locally
	Missing  []string  // local rel paths, in an allowed group, no longer enumerated
}

// Reconcile diffs the enumerated curated projects against the local repo set
// (rel paths under the corpus root, slash-separated). It is pure and the heart of
// sync's safety: a repo is flagged Missing only when its top-level group is in
// allow — so narrowing the allowlist leaves out-of-scope local repos untouched
// rather than marking them for prune. Result slices are sorted for determinism.
func Reconcile(projects []Project, localRels, allow []string) SyncPlan {
	enum := make(map[string]Project, len(projects))
	for _, p := range projects {
		enum[p.PathWithNamespace] = p
	}
	local := make(map[string]bool, len(localRels))
	for _, l := range localRels {
		local[l] = true
	}
	allowSet := make(map[string]bool, len(allow))
	for _, g := range allow {
		allowSet[g] = true
	}

	var plan SyncPlan
	for _, p := range projects {
		if local[p.PathWithNamespace] {
			plan.ToUpdate = append(plan.ToUpdate, p)
		} else {
			plan.ToClone = append(plan.ToClone, p)
		}
	}
	for _, l := range localRels {
		if _, ok := enum[l]; ok {
			continue // present on the server → handled as an update
		}
		if len(allowSet) > 0 && !allowSet[topLevelGroup(l)] {
			continue // out of curated scope → leave it alone, never prune
		}
		plan.Missing = append(plan.Missing, l)
	}
	sort.Slice(plan.ToClone, func(i, j int) bool { return plan.ToClone[i].PathWithNamespace < plan.ToClone[j].PathWithNamespace })
	sort.Slice(plan.ToUpdate, func(i, j int) bool { return plan.ToUpdate[i].PathWithNamespace < plan.ToUpdate[j].PathWithNamespace })
	sort.Strings(plan.Missing)
	return plan
}

// LocalRepos lists every git repo under root as a slash-separated path relative
// to root (matching a project's PathWithNamespace). A non-existent root yields an
// empty list (a fresh machine — everything is a clone), not an error.
func LocalRepos(root string) ([]string, error) {
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	dirs, err := ingest.DiscoverRepos(root)
	if err != nil {
		return nil, err
	}
	rels := make([]string, 0, len(dirs))
	for _, d := range dirs {
		rel, err := filepath.Rel(root, d)
		if err != nil {
			continue
		}
		rels = append(rels, filepath.ToSlash(rel))
	}
	return rels, nil
}

// PlanSync discovers the local repos under cfg.Root and reconciles them against
// the enumerated projects. It is the dry-run preview and the input to SyncProjects.
func PlanSync(cfg Config, projects []Project) (SyncPlan, error) {
	localRels, err := LocalRepos(cfg.Root)
	if err != nil {
		return SyncPlan{}, err
	}
	return Reconcile(projects, localRels, cfg.Groups), nil
}

// SyncProjects brings the corpus tree level with the server: clones new repos,
// fast-forwards existing ones (fetch + hard reset, shallow-safe), and — only when
// prune is true — removes local repos that are gone on the server. Clones and
// updates run concurrently (Moe's tentacles); prune runs sequentially afterward
// (deletion never races the fetch pool). progress, if non-nil, is called once per
// finished repo from worker goroutines, so it must be safe for concurrent use.
func SyncProjects(ctx context.Context, r Runner, cfg Config, projects []Project, prune bool, progress func(SyncResult)) (SyncReport, error) {
	plan, err := PlanSync(cfg, projects)
	if err != nil {
		return SyncReport{}, err
	}

	jobs := make([]func() SyncResult, 0, len(plan.ToClone)+len(plan.ToUpdate))
	for _, p := range plan.ToClone {
		p := p
		jobs = append(jobs, func() SyncResult { return cloneAsSync(ctx, r, cfg, p) })
	}
	for _, p := range plan.ToUpdate {
		p := p
		jobs = append(jobs, func() SyncResult { return updateOne(ctx, r, cfg, p) })
	}
	results := runParallel(cfg.Concurrency, jobs, progress)

	// Missing repos last, sequentially.
	for _, rel := range plan.Missing {
		res := SyncResult{Path: rel, Outcome: SyncMissing}
		if prune {
			if err := os.RemoveAll(filepath.Join(cfg.Root, filepath.FromSlash(rel))); err != nil {
				res.Outcome, res.Err = SyncFailed, err
			} else {
				res.Outcome = SyncPruned
			}
		}
		results = append(results, res)
		if progress != nil {
			progress(res)
		}
	}

	rep := SyncReport{Results: results}
	for _, res := range results {
		switch res.Outcome {
		case SyncCloned:
			rep.Cloned++
		case SyncUpdated:
			rep.Updated++
		case SyncCurrent:
			rep.Current++
		case SyncPruned:
			rep.Pruned++
		case SyncMissing:
			rep.Missing++
		case SyncFailed:
			rep.Failed++
		}
	}
	return rep, nil
}

// cloneAsSync clones a new repo and maps the result into the sync vocabulary.
func cloneAsSync(ctx context.Context, r Runner, cfg Config, p Project) SyncResult {
	res := cloneOne(ctx, r, cfg, p)
	switch res.Outcome {
	case Cloned:
		return SyncResult{Path: p.PathWithNamespace, Outcome: SyncCloned}
	case Skipped:
		// Raced into existence between reconcile and now; treat as current.
		return SyncResult{Path: p.PathWithNamespace, Outcome: SyncCurrent}
	default:
		return SyncResult{Path: p.PathWithNamespace, Outcome: SyncFailed, Err: res.Err, Detail: res.Detail}
	}
}

// updateOne fast-forwards an existing repo to the server tip. It fetches the
// default branch at depth 1 and, if the local tip differs from the fetched tip,
// hard-resets to it (the mirror is read-only, so reset is the robust choice — it
// survives force-pushes). An unchanged repo is reported SyncCurrent without a reset.
func updateOne(ctx context.Context, r Runner, cfg Config, p Project) SyncResult {
	dest := cfg.Dest(p)
	ref := p.DefaultBranch
	if ref == "" {
		ref = "HEAD"
	}
	res, err := r.RunEnv(ctx, gitEnv, "git", "-C", dest, "fetch", "--depth", "1", "origin", ref)
	if err != nil {
		return SyncResult{Path: p.PathWithNamespace, Outcome: SyncFailed, Err: err}
	}
	if !res.Ok() {
		return SyncResult{Path: p.PathWithNamespace, Outcome: SyncFailed, Err: errGitExit(res.Code), Detail: lastLine(res.Stderr)}
	}
	local := revParse(ctx, r, dest, "HEAD")
	remote := revParse(ctx, r, dest, "FETCH_HEAD")
	if local != "" && local == remote {
		return SyncResult{Path: p.PathWithNamespace, Outcome: SyncCurrent}
	}
	res, err = r.RunEnv(ctx, gitEnv, "git", "-C", dest, "reset", "--hard", "FETCH_HEAD")
	if err != nil {
		return SyncResult{Path: p.PathWithNamespace, Outcome: SyncFailed, Err: err}
	}
	if !res.Ok() {
		return SyncResult{Path: p.PathWithNamespace, Outcome: SyncFailed, Err: errGitExit(res.Code), Detail: lastLine(res.Stderr)}
	}
	return SyncResult{Path: p.PathWithNamespace, Outcome: SyncUpdated}
}

// revParse returns the commit ref points at in the repo at dir, or "" on error.
func revParse(ctx context.Context, r Runner, dir, ref string) string {
	res, err := r.Run(ctx, "git", "-C", dir, "rev-parse", ref)
	if err != nil || !res.Ok() {
		return ""
	}
	return strings.TrimSpace(string(res.Stdout))
}

// runParallel runs jobs with up to conc workers, returning results in input order
// and invoking progress (if non-nil) once per finished job.
func runParallel(conc int, jobs []func() SyncResult, progress func(SyncResult)) []SyncResult {
	if conc < 1 {
		conc = 1
	}
	if conc > len(jobs) && len(jobs) > 0 {
		conc = len(jobs)
	}
	results := make([]SyncResult, len(jobs))
	idx := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				res := jobs[i]()
				results[i] = res
				if progress != nil {
					progress(res)
				}
			}
		}()
	}
	for i := range jobs {
		idx <- i
	}
	close(idx)
	wg.Wait()
	return results
}
