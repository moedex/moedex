package corpus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const managedInitCommitMessage = "chore: initialize Moedex managed corpus"

// ManagedInitResult describes the first locally committed corpus snapshot.
type ManagedInitResult struct {
	Root               string
	Lock               Lock
	SuperprojectCommit string
}

// InitManaged creates a new Moedex-owned Git superproject. It never adopts or
// rewrites a non-empty destination. All server-controlled project data is
// validated before root is created; after the marker exists, failures leave the
// exact marked root in place for inspection or explicit abandonment.
func InitManaged(ctx context.Context, r Runner, cfg Config, projects []Project) (ManagedInitResult, error) {
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return ManagedInitResult{}, fmt.Errorf("resolve managed corpus root: %w", err)
	}
	cfg.Root = root

	catalog, err := NewCatalog(cfg)
	if err != nil {
		return ManagedInitResult{}, err
	}
	ordered, err := validateManagedProjects(cfg, projects)
	if err != nil {
		return ManagedInitResult{}, err
	}
	if err := requireEmptyDestination(root); err != nil {
		return ManagedInitResult{}, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return ManagedInitResult{}, fmt.Errorf("create managed corpus root %q: %w", root, err)
	}

	marked := false
	fail := func(action string, cause error) (ManagedInitResult, error) {
		if marked {
			return ManagedInitResult{}, fmt.Errorf("managed corpus initialization interrupted at %q while %s: %w", root, action, cause)
		}
		return ManagedInitResult{}, fmt.Errorf("initialize managed corpus at %q while %s: %w", root, action, cause)
	}

	if _, err := runManagedGit(ctx, r, "initialize superproject", root, "init"); err != nil {
		return fail("initializing the Git superproject", err)
	}

	// Serialize everything from here on (marker, submodule adds, lock write,
	// commit) against any other sync/init invocation racing this same root.
	// Acquired only once ".git" exists, since the lock lives inside it —
	// see managed_lock.go. Two concurrent "git init" calls against a fresh,
	// still-empty root are themselves harmless/idempotent.
	release, err := acquireManagedLock(root)
	if err != nil {
		return fail("locking the managed corpus root", err)
	}
	defer func() { _ = release() }()

	if err := WriteCatalog(root, catalog); err != nil {
		return fail("writing the ownership marker", err)
	}
	marked = true

	locked := make([]LockedProject, 0, len(ordered))
	stagePaths := []string{filepath.ToSlash(filepath.Join(ManagedDirName, CatalogFileName))}
	for _, project := range ordered {
		if project.EmptyRepo {
			continue
		}
		name := managedSubmoduleName(project.ID)
		if _, err := runManagedGit(ctx, r, "add project submodule", root,
			"submodule", "add",
			"--name", name,
			"--branch", project.DefaultBranch,
			"--", project.SSHURL, gitPathArg(project.PathWithNamespace)); err != nil {
			return fail("adding submodule "+name, err)
		}
		dest := cfg.Dest(project)
		res, err := runManagedGit(ctx, r, "resolve project default commit", dest,
			"rev-parse", "--verify", "HEAD")
		if err != nil {
			return fail("resolving submodule "+name+" commit", err)
		}
		commit := strings.TrimSpace(string(res.Stdout))
		entry := LockedProject{
			ID:                project.ID,
			PathWithNamespace: project.PathWithNamespace,
			CloneURL:          project.SSHURL,
			DefaultBranch:     project.DefaultBranch,
			DefaultCommit:     commit,
			Status:            LockStatusCurrent,
		}
		if !validGitObjectID(commit) {
			return fail("validating submodule "+name+" commit", fmt.Errorf("git returned malformed object ID %q", commit))
		}
		locked = append(locked, entry)
		stagePaths = append(stagePaths, gitPathArg(project.PathWithNamespace))
	}

	lock, err := NewLock(cfg.Host, true, locked)
	if err != nil {
		return fail("building the acquisition lock", err)
	}
	if err := WriteLock(root, cfg.Host, lock); err != nil {
		return fail("writing the acquisition lock", err)
	}
	stagePaths = append(stagePaths, filepath.ToSlash(filepath.Join(ManagedDirName, LockFileName)))
	if len(locked) > 0 {
		stagePaths = append(stagePaths, ".gitmodules")
	}
	sort.Strings(stagePaths)
	addArgs := append([]string{"add", "--"}, stagePaths...)
	if _, err := runManagedGit(ctx, r, "stage managed snapshot", root, addArgs...); err != nil {
		return fail("staging the managed snapshot", err)
	}
	if _, err := runManagedGit(ctx, r, "commit managed snapshot", root,
		"-c", "user.name=Moedex",
		"-c", "user.email=moedex@localhost",
		"commit", "-m", managedInitCommitMessage); err != nil {
		return fail("committing the managed snapshot", err)
	}
	res, err := runManagedGit(ctx, r, "resolve superproject commit", root,
		"rev-parse", "--verify", "HEAD")
	if err != nil {
		return fail("resolving the committed snapshot", err)
	}
	commit := strings.TrimSpace(string(res.Stdout))
	if !validGitObjectID(commit) {
		return fail("validating the committed snapshot", fmt.Errorf("git returned malformed object ID %q", commit))
	}
	return ManagedInitResult{Root: root, Lock: lock, SuperprojectCommit: commit}, nil
}

func validateManagedProjects(cfg Config, projects []Project) ([]Project, error) {
	ordered := append([]Project(nil), projects...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	seenIDs := make(map[int64]struct{}, len(ordered))
	seenPaths := make(map[string]int64, len(ordered))
	for _, project := range ordered {
		if project.ID <= 0 {
			return nil, fmt.Errorf("managed corpus project ID must be positive: %d", project.ID)
		}
		if _, ok := seenIDs[project.ID]; ok {
			return nil, fmt.Errorf("duplicate managed corpus project ID %d", project.ID)
		}
		seenIDs[project.ID] = struct{}{}
		if err := validateManagedPath(project.PathWithNamespace); err != nil {
			return nil, fmt.Errorf("project %d: %w", project.ID, err)
		}
		if other, ok := seenPaths[project.PathWithNamespace]; ok {
			return nil, fmt.Errorf("duplicate managed corpus path %q for projects %d and %d", project.PathWithNamespace, other, project.ID)
		}
		seenPaths[project.PathWithNamespace] = project.ID
		if err := validateCloneURL(cfg.Host, project.SSHURL); err != nil {
			return nil, fmt.Errorf("project %d: %w", project.ID, err)
		}
		if !project.EmptyRepo && project.DefaultBranch == "" {
			return nil, fmt.Errorf("project %d has no default branch", project.ID)
		}
		if err := validRef(project.DefaultBranch); err != nil {
			return nil, fmt.Errorf("project %d: %w", project.ID, err)
		}
	}
	for i := range ordered {
		for j := i + 1; j < len(ordered); j++ {
			left := ordered[i].PathWithNamespace
			right := ordered[j].PathWithNamespace
			if managedPathsOverlap(left, right) {
				return nil, fmt.Errorf("managed corpus paths overlap: %q and %q", left, right)
			}
		}
	}
	return ordered, nil
}

func requireEmptyDestination(root string) error {
	if root == "" {
		return errors.New("managed corpus root is empty")
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect managed corpus destination %q: %w", root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed corpus destination %q is a symbolic link", root)
	}
	if !info.IsDir() {
		return fmt.Errorf("managed corpus destination %q is not a directory", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("inspect managed corpus destination %q: %w", root, err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("refusing non-empty or unmarked managed corpus destination %q", root)
	}
	return nil
}

func managedSubmoduleName(id int64) string {
	return "project-" + strconv.FormatInt(id, 10)
}

// gitPathArg keeps a flag-shaped relative path from being reinterpreted by
// porcelain commands (or their internal child commands) even after "--".
func gitPathArg(path string) string {
	if strings.HasPrefix(path, "-") {
		return "./" + path
	}
	return path
}

func runManagedGit(ctx context.Context, r Runner, action, worktree string, args ...string) (Result, error) {
	gitArgs := make([]string, 0, len(args)+2)
	gitArgs = append(gitArgs, "-C", worktree)
	gitArgs = append(gitArgs, args...)
	res, err := r.RunEnv(ctx, gitEnv, "git", gitArgs...)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %s", action, redactDiagnostic(err.Error()))
	}
	if !res.Ok() {
		detail := redactDiagnostic(lastLine(res.Stderr))
		if detail == "" {
			detail = "no diagnostic output"
		}
		return res, fmt.Errorf("%s: git exited %d: %s", action, res.Code, detail)
	}
	return res, nil
}
