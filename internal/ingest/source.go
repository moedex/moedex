package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"moedex/internal/corpus/catalog"
)

// RepoSource is the acquisition-to-indexing boundary for one repository.
// Namespace is the label stored in file references; managed sources retain
// stable GitLab identity and the exact default commit from the corpus lock.
type RepoSource struct {
	ProjectID    int64
	Namespace    string
	Dir          string
	LockedCommit string
	Managed      bool
}

// VerifySource closes the discovery-to-ingest race for managed roots. Legacy
// sources intentionally remain live-working-tree compatible.
func VerifySource(source RepoSource) error {
	if !source.Managed {
		return nil
	}
	head, err := Head(source.Dir)
	if err != nil {
		return fmt.Errorf("read managed source project %d HEAD: %w", source.ProjectID, err)
	}
	if !strings.EqualFold(head, source.LockedCommit) {
		return fmt.Errorf("managed source project %d at %q HEAD %s does not match locked default commit %s", source.ProjectID, source.Namespace, shortCommit(head), shortCommit(source.LockedCommit))
	}
	return nil
}

// DiscoverSources returns the deterministic indexing source set for root. A
// marked root is lock-driven and fails closed on any marker, lock, materialized
// submodule, or HEAD mismatch. An unmarked root preserves recursive legacy
// discovery and basename labels.
func DiscoverSources(root string) ([]RepoSource, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve corpus root: %w", err)
	}
	managed, err := catalog.IsManagedRoot(absRoot)
	if err != nil {
		return nil, err
	}
	if managed {
		return discoverManagedSources(absRoot)
	}

	repos, err := DiscoverRepos(absRoot)
	if err != nil {
		return nil, err
	}
	sources := make([]RepoSource, 0, len(repos))
	for _, dir := range repos {
		head, err := Head(dir)
		if err != nil {
			head = ""
		}
		sources = append(sources, RepoSource{
			Namespace:    filepath.Base(dir),
			Dir:          dir,
			LockedCommit: head,
		})
	}
	return sources, nil
}

// DiscoverSourceDirs adapts the typed source seam to legacy freshness APIs.
// Managed integrity validation still occurs before any directory is returned.
func DiscoverSourceDirs(root string) ([]string, error) {
	sources, err := DiscoverSources(root)
	if err != nil {
		return nil, err
	}
	dirs := make([]string, len(sources))
	for i, source := range sources {
		dirs[i] = source.Dir
	}
	return dirs, nil
}

func discoverManagedSources(root string) ([]RepoSource, error) {
	cat, err := catalog.LoadCatalog(root)
	if err != nil {
		return nil, err
	}
	// Local ingestion uses the validated acquisition marker's host. The lock
	// must match that host; the example default is not an indexing allowlist.
	lock, err := catalog.LoadLock(root, cat.Host)
	if err != nil {
		return nil, err
	}
	sources := make([]RepoSource, 0, len(lock.Projects))
	for _, project := range lock.Projects {
		dir := filepath.Join(root, filepath.FromSlash(project.PathWithNamespace))
		info, err := os.Stat(dir)
		if err != nil {
			return nil, fmt.Errorf("managed source project %d at %q is not materialized: %w", project.ID, project.PathWithNamespace, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("managed source project %d at %q is not a directory", project.ID, project.PathWithNamespace)
		}
		marker, err := os.Lstat(filepath.Join(dir, ".git"))
		if err != nil || !(marker.IsDir() || marker.Mode().IsRegular()) {
			return nil, fmt.Errorf("managed source project %d at %q has no Git repository marker", project.ID, project.PathWithNamespace)
		}
		head, err := Head(dir)
		if err != nil {
			return nil, fmt.Errorf("read managed source project %d HEAD: %w", project.ID, err)
		}
		if !strings.EqualFold(head, project.DefaultCommit) {
			return nil, fmt.Errorf("managed source project %d at %q HEAD %s does not match locked default commit %s", project.ID, project.PathWithNamespace, shortCommit(head), shortCommit(project.DefaultCommit))
		}
		sources = append(sources, RepoSource{
			ProjectID:    project.ID,
			Namespace:    project.PathWithNamespace,
			Dir:          dir,
			LockedCommit: strings.ToLower(project.DefaultCommit),
			Managed:      true,
		})
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].ProjectID < sources[j].ProjectID })
	return sources, nil
}

func shortCommit(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
