package ingest

import (
	"io/fs"
	"path/filepath"
	"sort"
)

// DiscoverRepos walks root and returns the directory of every git repo beneath
// it — one entry per ".git" directory or file found, taking that entry's parent as
// the repo. The returned slice is sorted for deterministic, reproducible
// ingestion order.
//
// The discovery is defined to count repos exactly as `find <root> -name .git`
// does: every path component named ".git" is one repo. We do not descend into a
// ".git" directory once found (a git object store contains no nested repos), so
// the walk stays cheap while still visiting the full working tree of every
// repo — which means a repo nested inside another repo's working tree is
// discovered too, just like find would. Directories that cannot be read are
// skipped rather than aborting the whole walk.
func DiscoverRepos(root string) ([]string, error) {
	var repos []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// Unreadable directory: skip it, don't fail the whole discovery.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == ".git" {
			repos = append(repos, filepath.Dir(p))
			if d.IsDir() {
				return fs.SkipDir // a git object store holds no nested repos
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(repos)
	return repos, nil
}

// CountGitEntries returns the number of ".git" entries (dirs or files) under
// root, matching `find <root> -name .git | wc -l`. It is the ground-truth count
// the discovery is checked against (AC-B1): a repo whose .git is a file (a git
// worktree or submodule gitlink) is counted here and by DiscoverRepos. In a
// plain mirror the two agree; managed roots use DiscoverSources so the
// superproject marker itself is never treated as an indexing source.
func CountGitEntries(root string) (int, error) {
	n := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == ".git" {
			n++
			if d.IsDir() {
				return fs.SkipDir
			}
		}
		return nil
	})
	return n, err
}
