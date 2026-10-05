package corpus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Project is the subset of a GitLab project moedex-corpus needs to clone it and
// place it at the right path in the corpus tree.
type Project struct {
	// ID is GitLab's stable numeric project identifier. Namespace paths can
	// change, so managed-corpus reconciliation keys projects by this value.
	ID int64 `json:"id"`
	// PathWithNamespace is the full namespace path, e.g.
	// "services/Example.BillingApi". It doubles as the local relative path so
	// the mirror layout matches GitLab exactly.
	PathWithNamespace string `json:"path_with_namespace"`
	// SSHURL is the git clone URL (ssh_url_to_repo). Clones use SSH so they ride
	// the operator's existing key/auth — the same identity glab uses.
	SSHURL string `json:"ssh_url_to_repo"`
	// DefaultBranch is the branch to clone (for --single-branch). NOTE: GitLab
	// reports a *nominal* default branch (e.g. "main") even for a repo with zero
	// commits, where that ref does not actually exist on the remote — so a
	// non-empty value here does NOT mean the branch is clonable. Use EmptyRepo,
	// not DefaultBranch, to decide whether there is anything to clone.
	DefaultBranch string `json:"default_branch"`
	// EmptyRepo is true when the project has no commits (and thus no real branch).
	// Such repos contribute nothing to the corpus and must be skipped: a
	// `git clone --branch <nominal-default>` of one fails with "Remote branch …
	// not found in upstream origin". This field is only present in GitLab's full
	// project representation, so Enumerate must NOT request `simple=true`.
	EmptyRepo bool `json:"empty_repo"`
}

// TopLevelGroup returns the first path segment of the project's namespace — the
// group the allowlist matches on (e.g. "services" for
// "services/Example.BillingApi", "Libraries.Common" for a deeper
// "Libraries.Common/sub/Repo").
func (p Project) TopLevelGroup() string { return topLevelGroup(p.PathWithNamespace) }

// topLevelGroup returns the first '/'-separated segment of a namespace path.
func topLevelGroup(path string) string {
	if i := strings.IndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return path
}

// ParseProjects decodes the output of `glab api --paginate`, which concatenates
// one JSON array per page back-to-back (…}]​[{…) rather than emitting a single
// merged array. A streaming json.Decoder reads each array in turn, so this
// handles both the multi-page (concatenated) and single-array shapes. Entries
// missing a namespace path or SSH URL are skipped (defensive — every real
// project has both).
func ParseProjects(r io.Reader) ([]Project, error) {
	dec := json.NewDecoder(r)
	var all []Project
	for {
		var page []Project
		if err := dec.Decode(&page); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		for _, p := range page {
			if p.PathWithNamespace == "" || p.SSHURL == "" {
				continue
			}
			all = append(all, p)
		}
	}
	return all, nil
}

// FilterByGroups keeps only projects whose top-level group is in allow. An empty
// allow list means "no filter" — every project passes (callers that want the
// curated set must supply the allowlist). Input order is preserved.
func FilterByGroups(projects []Project, allow []string) []Project {
	if len(allow) == 0 {
		return projects
	}
	set := make(map[string]bool, len(allow))
	for _, g := range allow {
		set[g] = true
	}
	out := make([]Project, 0, len(projects))
	for _, p := range projects {
		if set[p.TopLevelGroup()] {
			out = append(out, p)
		}
	}
	return out
}

// Enumerate lists every accessible, non-archived project on cfg.Host via glab —
// access-filtered for free, since the API only returns what the operator's token
// can read (public + internal + private-where-member) — and returns the curated
// subset after applying the group allowlist. It is read-only.
//
// Curation is by group allowlist over ALL visible projects, NOT by membership.
// That choice is empirical: on the live corpus, all-visible + non-archived +
// allowlist defines the requested scope, whereas membership=true
// undershoots it by ~145 repos — almost all internal-visibility infra repos
// (Ansible roles/plays) the operator can read but isn't an explicit member of.
func Enumerate(ctx context.Context, r Runner, cfg Config) ([]Project, error) {
	// Full representation (no simple=true): we need the empty_repo flag, which the
	// simple representation omits. The extra fields are harmless and the payload is
	// fine at corpus scale (~500 projects, ~5 pages).
	res, err := r.Run(ctx, "glab", "api", "--hostname", cfg.Host, "--paginate",
		"projects?archived=false&per_page=100")
	if err != nil {
		return nil, fmt.Errorf("run glab api projects: %w", err)
	}
	if !res.Ok() {
		return nil, fmt.Errorf("glab api projects exited %d: %s", res.Code, redactDiagnostic(strings.TrimSpace(string(res.Stderr))))
	}
	projects, err := ParseProjects(bytes.NewReader(res.Stdout))
	if err != nil {
		return nil, fmt.Errorf("parse glab project list: %w", err)
	}
	return FilterByGroups(projects, cfg.Groups), nil
}
