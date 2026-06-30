package corpus

import (
	"context"
	"strings"
)

// CheckStatus is the outcome of one preflight check.
type CheckStatus int

const (
	// StatusOK means the check passed.
	StatusOK CheckStatus = iota
	// StatusFail means the check failed and Check.Fix tells the operator how to fix it.
	StatusFail
	// StatusSkipped means the check could not run because a prerequisite check failed.
	StatusSkipped
)

// Check is one preflight result.
type Check struct {
	Name   string      // what was checked
	Status CheckStatus // outcome
	Detail string      // what we found (path, version, reason)
	Fix    string      // remediation hint, set only when Status == StatusFail
}

// Report is the full doctor result: the per-check outcomes plus, when glab could
// reach the host, the projected number of repos a clone/sync would touch.
type Report struct {
	Host           string
	Root           string
	Checks         []Check
	Projected      int  // curated project count, valid only when ProjectedKnown
	ProjectedKnown bool // true when enumeration ran (glab present + authed)
}

// OK reports whether every check that ran passed (skipped checks don't count
// against it; a failure does).
func (r Report) OK() bool {
	for _, c := range r.Checks {
		if c.Status == StatusFail {
			return false
		}
	}
	return true
}

// Doctor runs the setup preflight against cfg.Host using r, and returns a
// structured report. It never mutates anything and never handles tokens itself —
// authentication is delegated entirely to glab; this only inspects and guides.
//
// The checks, in order:
//  1. glab CLI installed (LookPath)
//  2. authenticated to cfg.Host ONLY (`glab auth status --hostname HOST`)
//  3. git installed (LookPath)
//
// When glab is present and authed, it also enumerates the curated set so the
// operator sees how many repos the mirror will hold (Report.Projected).
func Doctor(ctx context.Context, r Runner, cfg Config) Report {
	rep := Report{Host: cfg.Host, Root: cfg.Root}

	// 1. glab installed.
	glabOK := false
	if path, err := r.LookPath("glab"); err != nil {
		rep.Checks = append(rep.Checks, Check{
			Name:   "glab CLI installed",
			Status: StatusFail,
			Detail: "glab not found on PATH",
			Fix:    "install it — macOS: brew install glab  (others: https://gitlab.com/gitlab-org/cli#installation)",
		})
	} else {
		detail := path
		if res, err := r.Run(ctx, "glab", "--version"); err == nil {
			if v := firstLine(res.Stdout); v != "" {
				detail = path + "  (" + v + ")"
			}
		}
		rep.Checks = append(rep.Checks, Check{Name: "glab CLI installed", Status: StatusOK, Detail: detail})
		glabOK = true
	}

	// 2. authenticated to the host (and only the host).
	authOK := false
	switch {
	case !glabOK:
		rep.Checks = append(rep.Checks, Check{
			Name:   "authenticated to " + cfg.Host,
			Status: StatusSkipped,
			Detail: "skipped — install glab first",
		})
	default:
		res, err := r.Run(ctx, "glab", "auth", "status", "--hostname", cfg.Host)
		if err == nil && res.Ok() {
			rep.Checks = append(rep.Checks, Check{
				Name:   "authenticated to " + cfg.Host,
				Status: StatusOK,
				Detail: "logged in",
			})
			authOK = true
		} else {
			rep.Checks = append(rep.Checks, Check{
				Name:   "authenticated to " + cfg.Host,
				Status: StatusFail,
				Detail: "not logged in to " + cfg.Host,
				Fix:    "glab auth login --hostname " + cfg.Host + "   (run it here now with:  ! glab auth login --hostname " + cfg.Host + " )",
			})
		}
	}

	// 3. git installed (needed for the clones themselves).
	if path, err := r.LookPath("git"); err != nil {
		rep.Checks = append(rep.Checks, Check{
			Name:   "git installed",
			Status: StatusFail,
			Detail: "git not found on PATH",
			Fix:    "install git — macOS: xcode-select --install  (others: your package manager)",
		})
	} else {
		rep.Checks = append(rep.Checks, Check{Name: "git installed", Status: StatusOK, Detail: path})
	}

	// Projected clone count — best-effort, only when we can actually reach the host.
	if glabOK && authOK {
		if projects, err := Enumerate(ctx, r, cfg); err == nil {
			rep.Projected = len(projects)
			rep.ProjectedKnown = true
		}
	}
	return rep
}

// firstLine returns the first non-empty line of b, trimmed.
func firstLine(b []byte) string { return nonEmptyLine(b, true) }

// nonEmptyLine scans the lines of b (split on "\n") for the first one that is
// non-empty after trimming, walking forward from the start if forward is true
// or backward from the end if false. It is the shared engine behind firstLine
// and lastLine, which differ only in scan direction.
func nonEmptyLine(b []byte, forward bool) string {
	lines := strings.Split(string(b), "\n")
	if !forward {
		for i := len(lines) - 1; i >= 0; i-- {
			if s := strings.TrimSpace(lines[i]); s != "" {
				return s
			}
		}
		return ""
	}
	for _, ln := range lines {
		if s := strings.TrimSpace(ln); s != "" {
			return s
		}
	}
	return ""
}
