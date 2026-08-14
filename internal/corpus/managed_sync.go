package corpus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
)

const managedSyncCommitMessage = "chore: sync Moedex managed corpus"

// ManagedActionKind is a pure reconciliation decision for one stable project ID.
type ManagedActionKind string

const (
	ManagedActionAdd          ManagedActionKind = "add"
	ManagedActionUpdate       ManagedActionKind = "update"
	ManagedActionMove         ManagedActionKind = "move"
	ManagedActionMissing      ManagedActionKind = "missing"
	ManagedActionPrune        ManagedActionKind = "prune"
	ManagedActionCarryForward ManagedActionKind = "carry_forward"
	ManagedActionConflict     ManagedActionKind = "conflict"
)

// ManagedAction retains both sides of a reconciliation decision. Previous is
// set for existing lock entries; Project is set for current enumeration entries.
type ManagedAction struct {
	Kind      ManagedActionKind
	ProjectID int64
	Previous  *LockedProject
	Project   *Project
	Reason    string
}

// ManagedPlan is an ordered, side-effect-free stable-ID reconciliation.
type ManagedPlan struct {
	Actions             []ManagedAction
	EnumerationComplete bool
	PruneRequested      bool
}

// ActionsOfKind returns a copy of the actions in one decision bucket.
func (p ManagedPlan) ActionsOfKind(kind ManagedActionKind) []ManagedAction {
	var out []ManagedAction
	for _, action := range p.Actions {
		if action.Kind == kind {
			out = append(out, action)
		}
	}
	return out
}

// ManagedSyncOptions makes prune authority explicit. The zero value is safe:
// an incomplete enumeration can update projects it did see, but cannot prune.
type ManagedSyncOptions struct {
	EnumerationComplete bool
	Prune               bool
}

type managedSyncHooks struct {
	beforeLockRename func(string) error
}

// ManagedFailure is one non-fatal project outcome included in an aggregate
// ManagedSyncError after independent successes have been committed.
type ManagedFailure struct {
	ProjectID int64
	Path      string
	Action    ManagedActionKind
	Err       error
}

// ManagedSyncError reports a partial managed sync. Its message deliberately
// excludes command arguments and remote URLs.
type ManagedSyncError struct {
	Root                  string
	Failures              []ManagedFailure
	IncompleteEnumeration bool
}

func (e *ManagedSyncError) Error() string {
	parts := make([]string, 0, 2)
	if e.IncompleteEnumeration {
		parts = append(parts, "enumeration was incomplete")
	}
	if len(e.Failures) > 0 {
		parts = append(parts, fmt.Sprintf("%d project operation(s) failed or conflicted", len(e.Failures)))
	}
	if len(parts) == 0 {
		parts = append(parts, "managed sync was partial")
	}
	return fmt.Sprintf("managed corpus sync at %q was partial: %s", e.Root, strings.Join(parts, "; "))
}

// ManagedSyncResult describes both the pure plan and the actions actually
// applied after fetch/dirty-state outcomes were known.
type ManagedSyncResult struct {
	Plan               ManagedPlan
	Applied            []ManagedAction
	Lock               Lock
	Changed            bool
	SuperprojectCommit string
}

// ReconcileManaged creates a deterministic stable-ID plan. Invalid or
// ambiguous incoming records become conflicts rather than destructive actions.
func ReconcileManaged(host string, previous Lock, projects []Project, opts ManagedSyncOptions) ManagedPlan {
	plan := ManagedPlan{
		EnumerationComplete: opts.EnumerationComplete,
		PruneRequested:      opts.Prune,
	}
	previousByID := make(map[int64]LockedProject, len(previous.Projects))
	previousPathOwner := make(map[string]int64, len(previous.Projects))
	for _, entry := range previous.Projects {
		previousByID[entry.ID] = entry
		previousPathOwner[entry.PathWithNamespace] = entry.ID
	}

	ordered := append([]Project(nil), projects...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].ID != ordered[j].ID {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].PathWithNamespace < ordered[j].PathWithNamespace
	})
	idCounts := make(map[int64]int, len(ordered))
	pathCounts := make(map[string]int, len(ordered))
	for _, project := range ordered {
		idCounts[project.ID]++
		pathCounts[project.PathWithNamespace]++
	}
	seenIDs := make(map[int64]struct{}, len(ordered))
	for i := range ordered {
		project := ordered[i]
		if _, seen := seenIDs[project.ID]; seen {
			continue
		}
		seenIDs[project.ID] = struct{}{}
		previousEntry, hadPrevious := previousByID[project.ID]
		action := ManagedAction{ProjectID: project.ID, Project: projectPtr(project)}
		if hadPrevious {
			action.Previous = lockedProjectPtr(previousEntry)
		}

		switch {
		case project.ID <= 0:
			action.Kind, action.Reason = ManagedActionConflict, "project ID must be positive"
		case idCounts[project.ID] > 1:
			action.Kind, action.Reason = ManagedActionConflict, "duplicate project ID in enumeration"
		case pathCounts[project.PathWithNamespace] > 1:
			action.Kind, action.Reason = ManagedActionConflict, "duplicate namespace path in enumeration"
		case incomingPathOverlaps(ordered, project):
			action.Kind, action.Reason = ManagedActionConflict, "namespace path overlaps another enumerated project"
		case validateManagedPath(project.PathWithNamespace) != nil:
			action.Kind, action.Reason = ManagedActionConflict, "unsafe namespace path"
		case validateCloneURL(host, project.SSHURL) != nil:
			action.Kind, action.Reason = ManagedActionConflict, "untrusted clone URL"
		case !project.EmptyRepo && project.DefaultBranch == "":
			action.Kind, action.Reason = ManagedActionConflict, "project has no default branch"
		case validRef(project.DefaultBranch) != nil:
			action.Kind, action.Reason = ManagedActionConflict, "unsafe default branch"
		case project.EmptyRepo && hadPrevious:
			action.Kind, action.Reason = ManagedActionCarryForward, "enumerated project has no commit"
		case project.EmptyRepo:
			action.Kind, action.Reason = ManagedActionCarryForward, "new empty project has no snapshot"
		case previousPathOwner[project.PathWithNamespace] != 0 && previousPathOwner[project.PathWithNamespace] != project.ID:
			action.Kind, action.Reason = ManagedActionConflict, "namespace path is owned by another project ID"
		case previousPathOverlaps(previous.Projects, project.ID, project.PathWithNamespace):
			action.Kind, action.Reason = ManagedActionConflict, "namespace path overlaps an existing project"
		case hadPrevious && previousEntry.PathWithNamespace != project.PathWithNamespace:
			action.Kind = ManagedActionMove
		case hadPrevious:
			action.Kind = ManagedActionUpdate
		default:
			action.Kind = ManagedActionAdd
		}
		plan.Actions = append(plan.Actions, action)
	}

	for _, entry := range previous.Projects {
		if _, present := seenIDs[entry.ID]; present {
			continue
		}
		action := ManagedAction{ProjectID: entry.ID, Previous: lockedProjectPtr(entry)}
		switch {
		case opts.EnumerationComplete && opts.Prune:
			action.Kind = ManagedActionPrune
		case opts.EnumerationComplete:
			action.Kind = ManagedActionMissing
		default:
			action.Kind = ManagedActionCarryForward
			action.Reason = "enumeration incomplete"
		}
		plan.Actions = append(plan.Actions, action)
	}

	sort.SliceStable(plan.Actions, func(i, j int) bool {
		if plan.Actions[i].ProjectID != plan.Actions[j].ProjectID {
			return plan.Actions[i].ProjectID < plan.Actions[j].ProjectID
		}
		return managedActionOrder(plan.Actions[i].Kind) < managedActionOrder(plan.Actions[j].Kind)
	})
	return plan
}

// SyncManaged reconciles and commits one safe superproject snapshot. Remote
// probes/fetches run concurrently across projects; all worktree, .gitmodules,
// index, lock, and commit mutations are serialized below that boundary.
func SyncManaged(ctx context.Context, r Runner, cfg Config, projects []Project, opts ManagedSyncOptions) (ManagedSyncResult, error) {
	return syncManaged(ctx, r, cfg, projects, opts, managedSyncHooks{})
}

func syncManaged(ctx context.Context, r Runner, cfg Config, projects []Project, opts ManagedSyncOptions, hooks managedSyncHooks) (ManagedSyncResult, error) {
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return ManagedSyncResult{}, fmt.Errorf("resolve managed corpus root: %w", err)
	}
	cfg.Root = root
	catalog, err := LoadCatalog(root)
	if err != nil {
		return ManagedSyncResult{}, err
	}
	if cfg.Host == "" {
		cfg.Host = catalog.Host
	}
	if cfg.Host != catalog.Host {
		return ManagedSyncResult{}, fmt.Errorf("managed corpus host %q does not match catalog host %q", cfg.Host, catalog.Host)
	}
	previous, err := LoadLock(root, catalog.Host)
	if err != nil {
		return ManagedSyncResult{}, err
	}
	if err := ensureManagedIndexClean(ctx, r, root); err != nil {
		return ManagedSyncResult{}, err
	}

	plan := ReconcileManaged(catalog.Host, previous, projects, opts)
	fetches := fetchManagedProjects(ctx, r, cfg, plan)
	finalByID := make(map[int64]LockedProject, len(previous.Projects)+len(projects))
	for _, entry := range previous.Projects {
		finalByID[entry.ID] = entry
	}
	applied := make([]ManagedAction, 0, len(plan.Actions))
	var failures []ManagedFailure
	mutated := false

	for _, action := range plan.Actions {
		actual := action
		switch action.Kind {
		case ManagedActionConflict:
			carryPrevious(finalByID, action.Previous)
			failures = append(failures, managedFailure(action, errors.New(action.Reason)))

		case ManagedActionCarryForward:
			carryPrevious(finalByID, action.Previous)
			if action.Project != nil && action.Previous != nil {
				failures = append(failures, managedFailure(action, errors.New(action.Reason)))
			}

		case ManagedActionMissing:
			carryPrevious(finalByID, action.Previous)

		case ManagedActionPrune:
			if err := ensureManagedActionClean(ctx, r, root, action); err != nil {
				actual.Kind, actual.Reason = ManagedActionConflict, err.Error()
				carryPrevious(finalByID, action.Previous)
				failures = append(failures, managedFailure(actual, err))
				break
			}
			if err := pruneManagedProject(ctx, r, root, action); err != nil {
				return ManagedSyncResult{Plan: plan, Applied: applied}, recoverableSyncError(root, "pruning project", err)
			}
			delete(finalByID, action.ProjectID)
			mutated = true

		case ManagedActionAdd, ManagedActionUpdate, ManagedActionMove:
			fetched := fetches[action.ProjectID]
			if fetched.Err != nil {
				actual.Kind, actual.Reason = ManagedActionCarryForward, "remote fetch failed"
				carryPrevious(finalByID, action.Previous)
				failures = append(failures, managedFailure(actual, fetched.Err))
				break
			}
			if action.Kind == ManagedActionAdd {
				if err := ensureManagedAddTargetAbsent(cfg, *action.Project); err != nil {
					actual.Kind, actual.Reason = ManagedActionConflict, err.Error()
					failures = append(failures, managedFailure(actual, err))
					break
				}
			}
			if action.Kind != ManagedActionAdd {
				if err := ensureManagedActionClean(ctx, r, root, action); err != nil {
					actual.Kind, actual.Reason = ManagedActionConflict, err.Error()
					carryPrevious(finalByID, action.Previous)
					failures = append(failures, managedFailure(actual, err))
					break
				}
			}
			entry, changed, err := applyManagedProject(ctx, r, cfg, action, fetched)
			if err != nil {
				return ManagedSyncResult{Plan: plan, Applied: applied}, recoverableSyncError(root, "applying project mutation", err)
			}
			finalByID[action.ProjectID] = entry
			mutated = mutated || changed
		}
		applied = append(applied, actual)
	}

	entries := make([]LockedProject, 0, len(finalByID))
	for _, entry := range finalByID {
		entries = append(entries, entry)
	}
	next, err := NewLock(catalog.Host, opts.EnumerationComplete, entries)
	if err != nil {
		return ManagedSyncResult{Plan: plan, Applied: applied}, recoverableSyncError(root, "building the next lock", err)
	}
	semanticChange := !reflect.DeepEqual(previous, next)
	if semanticChange {
		if err := writeLock(root, catalog.Host, next, hooks.beforeLockRename); err != nil {
			return ManagedSyncResult{Plan: plan, Applied: applied}, recoverableSyncError(root, "writing the next lock", err)
		}
		mutated = true
	}

	result := ManagedSyncResult{Plan: plan, Applied: applied, Lock: next, Changed: mutated}
	if mutated {
		if err := stageManagedSync(ctx, r, root, applied); err != nil {
			return result, recoverableSyncError(root, "staging the next snapshot", err)
		}
		if _, err := runManagedGit(ctx, r, "commit managed sync", root,
			"-c", "user.name=Moedex",
			"-c", "user.email=moedex@localhost",
			"commit", "-m", managedSyncCommitMessage); err != nil {
			return result, recoverableSyncError(root, "committing the next snapshot", err)
		}
		res, err := runManagedGit(ctx, r, "resolve managed sync commit", root,
			"rev-parse", "--verify", "HEAD")
		if err != nil {
			return result, recoverableSyncError(root, "resolving the next snapshot", err)
		}
		result.SuperprojectCommit = strings.TrimSpace(string(res.Stdout))
	}

	if len(failures) > 0 || !opts.EnumerationComplete {
		return result, &ManagedSyncError{
			Root:                  root,
			Failures:              failures,
			IncompleteEnumeration: !opts.EnumerationComplete,
		}
	}
	return result, nil
}

type managedFetch struct {
	Commit string
	Err    error
}

func fetchManagedProjects(ctx context.Context, r Runner, cfg Config, plan ManagedPlan) map[int64]managedFetch {
	actions := make([]ManagedAction, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		if action.Kind == ManagedActionAdd || action.Kind == ManagedActionUpdate || action.Kind == ManagedActionMove {
			actions = append(actions, action)
		}
	}
	results := make([]managedFetch, len(actions))
	conc := cfg.Concurrency
	if conc < 1 {
		conc = 1
	}
	if conc > len(actions) && len(actions) > 0 {
		conc = len(actions)
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < conc; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				results[index] = fetchManagedProject(ctx, r, cfg, actions[index])
			}
		}()
	}
	for i := range actions {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	out := make(map[int64]managedFetch, len(actions))
	for i, action := range actions {
		out[action.ProjectID] = results[i]
	}
	return out
}

func fetchManagedProject(ctx context.Context, r Runner, cfg Config, action ManagedAction) managedFetch {
	project := action.Project
	if project == nil {
		return managedFetch{Err: errors.New("managed action has no enumerated project")}
	}
	if action.Kind == ManagedActionAdd {
		ref := "refs/heads/" + project.DefaultBranch
		res, err := r.RunEnv(ctx, gitEnv, "git", "ls-remote", "--exit-code", "--", project.SSHURL, ref)
		if err != nil {
			return managedFetch{Err: err}
		}
		if !res.Ok() {
			return managedFetch{Err: fmt.Errorf("remote probe exited %d", res.Code)}
		}
		fields := strings.Fields(string(res.Stdout))
		if len(fields) < 2 || !validGitObjectID(fields[0]) {
			return managedFetch{Err: errors.New("remote probe returned no valid default commit")}
		}
		return managedFetch{Commit: strings.ToLower(fields[0])}
	}

	dest := filepath.Join(cfg.Root, filepath.FromSlash(action.Previous.PathWithNamespace))
	res, err := r.RunEnv(ctx, gitEnv, "git", "-C", dest, "fetch", "--no-tags", project.SSHURL, "--", project.DefaultBranch)
	if err != nil {
		return managedFetch{Err: err}
	}
	if !res.Ok() {
		return managedFetch{Err: fmt.Errorf("fetch exited %d", res.Code)}
	}
	res, err = runManagedGit(ctx, r, "resolve fetched project commit", dest,
		"rev-parse", "--verify", "FETCH_HEAD")
	if err != nil {
		return managedFetch{Err: err}
	}
	commit := strings.TrimSpace(string(res.Stdout))
	if !validGitObjectID(commit) {
		return managedFetch{Err: errors.New("fetch returned no valid default commit")}
	}
	return managedFetch{Commit: strings.ToLower(commit)}
}

func applyManagedProject(ctx context.Context, r Runner, cfg Config, action ManagedAction, fetched managedFetch) (LockedProject, bool, error) {
	project := *action.Project
	switch action.Kind {
	case ManagedActionAdd:
		dest := cfg.Dest(project)
		if _, err := os.Lstat(dest); err == nil {
			return LockedProject{}, false, fmt.Errorf("add target %q already exists", project.PathWithNamespace)
		} else if !errors.Is(err, os.ErrNotExist) {
			return LockedProject{}, false, err
		}
		if _, err := runManagedGit(ctx, r, "add project submodule", cfg.Root,
			"submodule", "add",
			"--name", managedSubmoduleName(project.ID),
			"--branch", project.DefaultBranch,
			"--", project.SSHURL, gitPathArg(project.PathWithNamespace)); err != nil {
			return LockedProject{}, false, err
		}
		res, err := runManagedGit(ctx, r, "resolve added project commit", dest,
			"rev-parse", "--verify", "HEAD")
		if err != nil {
			return LockedProject{}, false, err
		}
		fetched.Commit = strings.TrimSpace(string(res.Stdout))

	case ManagedActionMove:
		from := action.Previous.PathWithNamespace
		dest := cfg.Dest(project)
		if _, err := os.Lstat(dest); err == nil {
			return LockedProject{}, false, fmt.Errorf("move target %q already exists", project.PathWithNamespace)
		} else if !errors.Is(err, os.ErrNotExist) {
			return LockedProject{}, false, err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return LockedProject{}, false, fmt.Errorf("create move destination parent: %w", err)
		}
		if _, err := runManagedGit(ctx, r, "move project submodule", cfg.Root,
			"mv", "--", gitPathArg(from), gitPathArg(project.PathWithNamespace)); err != nil {
			return LockedProject{}, false, err
		}
		if err := setManagedSubmoduleMetadata(ctx, r, cfg.Root, project); err != nil {
			return LockedProject{}, false, err
		}
		if _, err := runManagedGit(ctx, r, "reset moved project", cfg.Dest(project),
			"reset", "--hard", "FETCH_HEAD"); err != nil {
			return LockedProject{}, false, err
		}

	case ManagedActionUpdate:
		dest := cfg.Dest(project)
		metadataChanged := action.Previous.CloneURL != project.SSHURL || action.Previous.DefaultBranch != project.DefaultBranch
		if metadataChanged {
			if err := setManagedSubmoduleMetadata(ctx, r, cfg.Root, project); err != nil {
				return LockedProject{}, false, err
			}
		}
		if action.Previous.DefaultCommit != fetched.Commit {
			if _, err := runManagedGit(ctx, r, "reset updated project", dest,
				"reset", "--hard", "FETCH_HEAD"); err != nil {
				return LockedProject{}, false, err
			}
		}
		entry := lockedProjectFromProject(project, fetched.Commit)
		return entry, metadataChanged || action.Previous.DefaultCommit != fetched.Commit || action.Previous.Status != LockStatusCurrent, nil
	}

	if !validGitObjectID(fetched.Commit) {
		return LockedProject{}, false, errors.New("project mutation produced an invalid commit")
	}
	return lockedProjectFromProject(project, fetched.Commit), true, nil
}

func setManagedSubmoduleMetadata(ctx context.Context, r Runner, root string, project Project) error {
	section := "submodule." + managedSubmoduleName(project.ID)
	values := [][2]string{
		{section + ".path", project.PathWithNamespace},
		{section + ".url", project.SSHURL},
		{section + ".branch", project.DefaultBranch},
	}
	for _, value := range values {
		if _, err := runManagedGit(ctx, r, "update submodule metadata", root,
			"config", "-f", ".gitmodules", "--replace-all", value[0], value[1]); err != nil {
			return err
		}
	}
	if _, err := runManagedGit(ctx, r, "update submodule origin", filepath.Join(root, filepath.FromSlash(project.PathWithNamespace)),
		"remote", "set-url", "origin", project.SSHURL); err != nil {
		return err
	}
	return nil
}

func ensureManagedIndexClean(ctx context.Context, r Runner, root string) error {
	res, err := runManagedGit(ctx, r, "inspect managed superproject index", root,
		"diff", "--cached", "--name-only", "-z")
	if err != nil {
		return err
	}
	if len(res.Stdout) != 0 {
		return fmt.Errorf("managed corpus at %q has pre-existing staged changes", root)
	}
	res, err = runManagedGit(ctx, r, "inspect managed metadata", root,
		"status", "--porcelain", "--untracked-files=all", "--", ".gitmodules", ManagedDirName)
	if err != nil {
		return err
	}
	if len(res.Stdout) != 0 {
		return fmt.Errorf("managed corpus at %q has modified ownership metadata", root)
	}
	return nil
}

func ensureManagedActionClean(ctx context.Context, r Runner, root string, action ManagedAction) error {
	if action.Previous == nil {
		return nil
	}
	path := action.Previous.PathWithNamespace
	dest := filepath.Join(root, filepath.FromSlash(path))
	res, err := runManagedGit(ctx, r, "inspect project worktree", dest,
		"status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if len(res.Stdout) != 0 {
		return fmt.Errorf("project %d at %q has local modifications or untracked files", action.ProjectID, path)
	}
	res, err = runManagedGit(ctx, r, "inspect superproject path", root,
		"status", "--porcelain", "--untracked-files=all", "--", gitPathArg(path))
	if err != nil {
		return err
	}
	if len(res.Stdout) != 0 {
		return fmt.Errorf("superproject path %q has local modifications", path)
	}
	return nil
}

func ensureManagedAddTargetAbsent(cfg Config, project Project) error {
	dest := cfg.Dest(project)
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("add target %q already exists", project.PathWithNamespace)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func pruneManagedProject(ctx context.Context, r Runner, root string, action ManagedAction) error {
	path := action.Previous.PathWithNamespace
	if _, err := runManagedGit(ctx, r, "deinitialize project submodule", root,
		"submodule", "deinit", "-f", "--", gitPathArg(path)); err != nil {
		return err
	}
	if _, err := runManagedGit(ctx, r, "remove project gitlink", root,
		"rm", "-f", "--", gitPathArg(path)); err != nil {
		return err
	}
	return nil
}

func stageManagedSync(ctx context.Context, r Runner, root string, actions []ManagedAction) error {
	paths := map[string]struct{}{
		filepath.ToSlash(filepath.Join(ManagedDirName, LockFileName)): {},
	}
	metadataChanged := false
	for _, action := range actions {
		switch action.Kind {
		case ManagedActionAdd, ManagedActionMove:
			metadataChanged = true
			if action.Project != nil {
				paths[action.Project.PathWithNamespace] = struct{}{}
			}
		case ManagedActionUpdate:
			if action.Project != nil {
				paths[action.Project.PathWithNamespace] = struct{}{}
			}
			if action.Previous != nil && action.Project != nil &&
				(action.Previous.CloneURL != action.Project.SSHURL || action.Previous.DefaultBranch != action.Project.DefaultBranch) {
				metadataChanged = true
			}
		case ManagedActionPrune:
			metadataChanged = true
		}
	}
	if metadataChanged {
		paths[".gitmodules"] = struct{}{}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, gitPathArg(path))
	}
	sort.Strings(ordered)
	args := append([]string{"add", "--"}, ordered...)
	_, err := runManagedGit(ctx, r, "stage managed sync snapshot", root, args...)
	return err
}

func carryPrevious(final map[int64]LockedProject, previous *LockedProject) {
	if previous == nil {
		return
	}
	entry := *previous
	entry.Status = LockStatusCarriedForward
	final[entry.ID] = entry
}

func managedFailure(action ManagedAction, err error) ManagedFailure {
	path := ""
	if action.Project != nil {
		path = action.Project.PathWithNamespace
	} else if action.Previous != nil {
		path = action.Previous.PathWithNamespace
	}
	return ManagedFailure{ProjectID: action.ProjectID, Path: path, Action: action.Kind, Err: err}
}

func lockedProjectFromProject(project Project, commit string) LockedProject {
	return LockedProject{
		ID:                project.ID,
		PathWithNamespace: project.PathWithNamespace,
		CloneURL:          project.SSHURL,
		DefaultBranch:     project.DefaultBranch,
		DefaultCommit:     strings.ToLower(commit),
		Status:            LockStatusCurrent,
	}
}

func projectPtr(project Project) *Project {
	copy := project
	return &copy
}

func lockedProjectPtr(project LockedProject) *LockedProject {
	copy := project
	return &copy
}

func managedActionOrder(kind ManagedActionKind) int {
	switch kind {
	case ManagedActionConflict:
		return 0
	case ManagedActionCarryForward:
		return 1
	case ManagedActionMissing:
		return 2
	case ManagedActionPrune:
		return 3
	case ManagedActionMove:
		return 4
	case ManagedActionUpdate:
		return 5
	case ManagedActionAdd:
		return 6
	default:
		return 7
	}
}

func incomingPathOverlaps(projects []Project, project Project) bool {
	for _, other := range projects {
		if other.ID == project.ID && other.PathWithNamespace == project.PathWithNamespace {
			continue
		}
		if managedPathsOverlap(project.PathWithNamespace, other.PathWithNamespace) {
			return true
		}
	}
	return false
}

func previousPathOverlaps(previous []LockedProject, projectID int64, projectPath string) bool {
	for _, other := range previous {
		if other.ID == projectID {
			continue
		}
		if managedPathsOverlap(projectPath, other.PathWithNamespace) {
			return true
		}
	}
	return false
}

func recoverableSyncError(root, action string, err error) error {
	return fmt.Errorf("managed corpus sync interrupted at %q while %s; the last committed snapshot remains authoritative: %w", root, action, err)
}
