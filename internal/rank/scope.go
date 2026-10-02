package rank

import (
	"context"

	"moedex/internal/index"
	"moedex/internal/sourcescope"
)

func (r *Ranker) buildScopeCatalog() {
	r.scopeByRepo = make(map[string][]uint64)
	for id := uint64(0); id < uint64(r.ix.NumBlobs()); id++ {
		blob := r.ix.Blob(id)
		if blob == nil {
			continue
		}
		r.scopeAll = append(r.scopeAll, id)
		seen := make(map[string]bool)
		for _, file := range blob.Files {
			if !seen[file.Repo] {
				r.scopeByRepo[file.Repo] = append(r.scopeByRepo[file.Repo], id)
				seen[file.Repo] = true
			}
		}
	}
}

// scopedView borrows the immutable scoring indexes. It copies only matching
// location records and path-entry slice headers, never content or token indexes.
// Repo-scoped requests visit that repository's blob catalog; scopes without a
// repo currently scan the corpus's location metadata, not source bytes.
func (r *Ranker) scopedView(ctx context.Context, scope sourcescope.Scope) (*Ranker, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	view := *r
	view.scopeFiles = make(map[uint64][]index.FileRef)
	view.pathInfo = make(map[uint64][]coverageEntry)
	view.symInfo = make(map[uint64][]coverageEntry)
	candidates := r.scopeAll
	if scope.Repo != "" {
		candidates = r.scopeByRepo[scope.Repo]
	}
	for i, id := range candidates {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		blob := r.ix.Blob(id)
		if blob == nil {
			continue
		}
		matched := 0
		for pos, file := range blob.Files {
			if pos%256 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			if scope.Match(file) {
				matched++
			}
		}
		if matched == 0 {
			continue
		}
		if matched == len(blob.Files) {
			// The common single-location case borrows both immutable slices.
			view.scopeFiles[id] = blob.Files
			if entries := r.pathInfo[id]; len(entries) > 0 {
				view.pathInfo[id] = entries
			}
		} else {
			files := make([]index.FileRef, 0, matched)
			entries := make([]coverageEntry, 0, matched)
			for pos, file := range blob.Files {
				if !scope.Match(file) {
					continue
				}
				files = append(files, file)
				if pos < len(r.pathInfo[id]) {
					entries = append(entries, r.pathInfo[id][pos])
				}
			}
			view.scopeFiles[id] = files
			if len(entries) > 0 {
				view.pathInfo[id] = entries
			}
		}
		if entries := r.symInfo[id]; len(entries) > 0 {
			view.symInfo[id] = entries
		}
	}
	return &view, ctx.Err()
}
