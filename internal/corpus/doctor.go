package corpus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
// Authentication, host reachability, and Git transport are intentionally
// separate checks: a valid glab token does not imply that the VPN is connected,
// and a reachable API does not prove that SSH/HTTPS clone credentials work.
func Doctor(ctx context.Context, r Runner, cfg Config) Report {
	rep := Report{Host: cfg.Host, Root: cfg.Root}
	rep.Checks = append(rep.Checks, doctorManagedRoot(ctx, r, cfg)...)

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

	// 3. GitLab API / VPN reachability, distinct from stored authentication.
	reachable := false
	switch {
	case !authOK:
		rep.Checks = append(rep.Checks, Check{
			Name:   "GitLab host/VPN reachable",
			Status: StatusSkipped,
			Detail: "skipped — authenticate glab first",
		})
	default:
		res, err := r.Run(ctx, "glab", "api", "--hostname", cfg.Host, "/version")
		if err == nil && res.Ok() {
			rep.Checks = append(rep.Checks, Check{Name: "GitLab host/VPN reachable", Status: StatusOK, Detail: "API reachable"})
			reachable = true
		} else {
			detail := commandFailureDetail(err, res, "host unreachable")
			rep.Checks = append(rep.Checks, Check{
				Name:   "GitLab host/VPN reachable",
				Status: StatusFail,
				Detail: detail,
				Fix:    "connect the required network, then re-run `moedex-corpus doctor`",
			})
		}
	}

	// 4. git installed (needed for the clone/fetch transport probe).
	gitOK := false
	if path, err := r.LookPath("git"); err != nil {
		rep.Checks = append(rep.Checks, Check{
			Name:   "git installed",
			Status: StatusFail,
			Detail: "git not found on PATH",
			Fix:    "install git — macOS: xcode-select --install  (others: your package manager)",
		})
	} else {
		rep.Checks = append(rep.Checks, Check{Name: "git installed", Status: StatusOK, Detail: path})
		gitOK = true
	}

	// Enumerate once after the control-plane checks, then use a real visible
	// project's configured protocol for the data-plane probe.
	var projects []Project
	if glabOK && authOK && reachable {
		var err error
		projects, err = Enumerate(ctx, r, cfg)
		if err == nil {
			rep.Projected = len(projects)
			rep.ProjectedKnown = true
		} else {
			rep.Checks = append(rep.Checks, Check{
				Name:   "curated project enumeration",
				Status: StatusFail,
				Detail: redactDiagnostic(err.Error()),
				Fix:    "verify GitLab API access to the curated groups",
			})
		}
	}

	switch {
	case !gitOK:
		rep.Checks = append(rep.Checks, Check{Name: "Git clone/fetch transport", Status: StatusSkipped, Detail: "skipped — install git first"})
	case !reachable:
		rep.Checks = append(rep.Checks, Check{Name: "Git clone/fetch transport", Status: StatusSkipped, Detail: "skipped — GitLab host is not reachable"})
	case !rep.ProjectedKnown:
		rep.Checks = append(rep.Checks, Check{Name: "Git clone/fetch transport", Status: StatusSkipped, Detail: "skipped — project enumeration failed"})
	default:
		project, ok := firstTransportProject(projects)
		if !ok {
			rep.Checks = append(rep.Checks, Check{Name: "Git clone/fetch transport", Status: StatusSkipped, Detail: "skipped — no non-empty project is in scope"})
			break
		}
		ref := "refs/heads/" + project.DefaultBranch
		res, err := r.RunEnv(ctx, gitEnv, "git", "ls-remote", "--exit-code", "--", project.SSHURL, ref)
		if err == nil && res.Ok() {
			rep.Checks = append(rep.Checks, Check{Name: "Git clone/fetch transport", Status: StatusOK, Detail: "readable using the configured Git protocol"})
		} else {
			rep.Checks = append(rep.Checks, Check{
				Name:   "Git clone/fetch transport",
				Status: StatusFail,
				Detail: commandFailureDetail(err, res, "transport probe failed"),
				Fix:    "verify SSH keys or HTTPS Git credentials; test `git ls-remote <validated-project-url> <default-ref>`",
			})
		}
	}
	return rep
}

func firstTransportProject(projects []Project) (Project, bool) {
	for _, project := range projects {
		if !project.EmptyRepo && project.DefaultBranch != "" {
			return project, true
		}
	}
	return Project{}, false
}

func commandFailureDetail(err error, res Result, fallback string) string {
	if err != nil {
		return redactDiagnostic(err.Error())
	}
	if detail := redactDiagnostic(lastLine(res.Stderr)); detail != "" {
		return detail
	}
	if res.Code != 0 {
		return fmt.Sprintf("%s (exit %d)", fallback, res.Code)
	}
	return fallback
}

func doctorManagedRoot(ctx context.Context, r Runner, cfg Config) []Check {
	managed, err := IsManagedRoot(cfg.Root)
	if err != nil {
		return []Check{{
			Name:   "managed ownership marker",
			Status: StatusFail,
			Detail: redactDiagnostic(err.Error()),
			Fix:    "restore a regular .moedex/corpus.json from the last committed snapshot; do not adopt this root implicitly",
		}}
	}
	if !managed {
		return nil
	}

	checks := make([]Check, 0, 5)
	catalog, err := LoadCatalog(cfg.Root)
	if err != nil {
		return append(checks, Check{
			Name:   "managed ownership marker",
			Status: StatusFail,
			Detail: redactDiagnostic(err.Error()),
			Fix:    "restore .moedex/corpus.json from the last committed snapshot; do not edit or adopt in place",
		})
	}
	if cfg.Host != "" && catalog.Host != cfg.Host {
		checks = append(checks, Check{
			Name:   "managed ownership marker",
			Status: StatusFail,
			Detail: fmt.Sprintf("marker host %q does not match pinned host %q", catalog.Host, cfg.Host),
			Fix:    "create a new sibling managed corpus for the pinned host",
		})
		return checks
	}
	checks = append(checks, Check{Name: "managed ownership marker", Status: StatusOK, Detail: "schema and pinned host valid"})

	res, runErr := r.RunEnv(ctx, gitEnv, "git", "-C", cfg.Root, "rev-parse", "--is-inside-work-tree")
	if runErr != nil || !res.Ok() || strings.TrimSpace(string(res.Stdout)) != "true" {
		checks = append(checks, Check{
			Name:   "managed Git superproject",
			Status: StatusFail,
			Detail: commandFailureDetail(runErr, res, "root is not a Git work tree"),
			Fix:    "restore the last committed superproject or initialize a new sibling root",
		})
		return checks
	}
	checks = append(checks, Check{Name: "managed Git superproject", Status: StatusOK, Detail: "work tree valid"})

	lock, err := LoadLock(cfg.Root, catalog.Host)
	if err != nil {
		checks = append(checks, Check{
			Name:   "managed acquisition lock",
			Status: StatusFail,
			Detail: redactDiagnostic(err.Error()),
			Fix:    "restore .moedex/corpus.lock.json from HEAD; do not fall back to filesystem discovery",
		})
		return checks
	}
	checks = append(checks, Check{Name: "managed acquisition lock", Status: StatusOK, Detail: fmt.Sprintf("%d locked project(s)", len(lock.Projects))})

	checks = append(checks, doctorManagedCleanliness(ctx, r, cfg.Root, lock))
	checks = append(checks, doctorManagedAgreement(ctx, r, cfg.Root, lock))
	return checks
}

func doctorManagedCleanliness(ctx context.Context, r Runner, root string, lock Lock) Check {
	failInspect := func(detail string) Check {
		return Check{Name: "managed recoverable cleanliness", Status: StatusFail, Detail: detail, Fix: "inspect the superproject without mutating it, then restore or commit an intentional snapshot"}
	}
	failDirty := func(detail string) Check {
		return Check{
			Name:   "managed recoverable cleanliness",
			Status: StatusFail,
			Detail: detail,
			Fix:    "preserve any local work, then restore the last committed managed snapshot before syncing",
		}
	}

	state, err := probeManagedStage(ctx, r, root)
	if err != nil {
		return failInspect(redactDiagnostic(err.Error()))
	}
	if state == managedDirty {
		return failDirty("managed superproject metadata or tracked gitlinks have local changes")
	}

	// Untracked files are not benign inside an indexed submodule: preserve them
	// as local work and stop before sync can advance. Unlike probeManagedStage
	// above, this is unconditional per project — a submodule's own worktree is
	// never part of what stageManagedSync stages, so any output here is real,
	// unstaged local work regardless of whether the superproject index itself
	// is clean or holds a resumable stage.
	var dirty []managedDirtyProject
	for _, project := range lock.Projects {
		dest := filepath.Join(root, filepath.FromSlash(project.PathWithNamespace))
		res, err := r.RunEnv(ctx, gitEnv, "git", "-C", dest, "status", "--porcelain", "--untracked-files=all")
		if err != nil || !res.Ok() {
			return failInspect(commandFailureDetail(err, res, "could not inspect a managed submodule worktree"))
		}
		if len(res.Stdout) != 0 {
			dirty = append(dirty, summarizeManagedDirtyProject(project, res.Stdout))
		}
	}
	if len(dirty) > 0 {
		return failDirty(formatManagedDirtyProjects(dirty))
	}

	if state == managedStagedResumable {
		// A prior sync's `git add` completed but it never reached `git commit`
		// (signal, deadline, OOM, reboot). That is safe and self-healing — the
		// next sync commits it — so it must not block ExecStartPre in the
		// shipped systemd unit the way a genuinely dirty tree does.
		return Check{
			Name:   "managed recoverable cleanliness",
			Status: StatusOK,
			Detail: "a staged snapshot from an interrupted sync is pending — the next sync commits it automatically",
		}
	}
	return Check{Name: "managed recoverable cleanliness", Status: StatusOK, Detail: "tracked snapshot and managed submodule worktrees are clean"}
}

type managedDirtyProject struct {
	project   LockedProject
	paths     []string
	generated bool
}

func summarizeManagedDirtyProject(project LockedProject, porcelain []byte) managedDirtyProject {
	paths := make([]string, 0, 1)
	generated := true
	for _, line := range strings.Split(strings.TrimSpace(string(porcelain)), "\n") {
		if line == "" {
			continue
		}
		path := line
		if len(line) > 3 {
			path = strings.TrimSpace(line[3:])
		}
		paths = append(paths, path)
		if len(line) < 2 || line[:2] != "??" || !isKnownMSBuildIntermediate(path) {
			generated = false
		}
	}
	return managedDirtyProject{project: project, paths: paths, generated: generated && len(paths) > 0}
}

func isKnownMSBuildIntermediate(path string) bool {
	normalized := filepath.ToSlash(path)
	if !strings.HasPrefix(normalized, "obj/") && !strings.Contains(normalized, "/obj/") {
		return false
	}
	for _, suffix := range []string{
		".AssemblyAttributes.cs",
		".AssemblyInfo.cs",
		".AssemblyInfoInputs.cache",
		".GeneratedMSBuildEditorConfig.editorconfig",
		".GlobalUsings.g.cs",
		".AssemblyReference.cache",
	} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

func formatManagedDirtyProjects(dirty []managedDirtyProject) string {
	pathCount := 0
	generatedCount := 0
	for _, project := range dirty {
		pathCount += len(project.paths)
		if project.generated {
			generatedCount++
		}
	}
	detail := fmt.Sprintf("%d managed submodule worktree(s) have local changes across %d path(s)", len(dirty), pathCount)
	if generatedCount == len(dirty) {
		detail += "; all are untracked MSBuild intermediates"
	} else if generatedCount > 0 {
		detail += fmt.Sprintf("; %d contain only untracked MSBuild intermediates", generatedCount)
	}

	const maxProjects = 6
	parts := make([]string, 0, min(len(dirty), maxProjects))
	for i, project := range dirty {
		if i == maxProjects {
			break
		}
		firstPath := "unknown path"
		if len(project.paths) > 0 {
			firstPath = project.paths[0]
		}
		kind := "local change"
		if project.generated {
			kind = "MSBuild output"
		}
		parts = append(parts, fmt.Sprintf("%s [%s: %s]", project.project.PathWithNamespace, kind, firstPath))
	}
	if len(parts) > 0 {
		detail += ": " + strings.Join(parts, "; ")
	}
	if len(dirty) > maxProjects {
		detail += fmt.Sprintf("; and %d more", len(dirty)-maxProjects)
	}
	return detail
}

func doctorManagedAgreement(ctx context.Context, r Runner, root string, lock Lock) Check {
	fix := "restore .gitmodules, gitlinks, lock, and submodules from the same committed snapshot; do not repair by deleting projects"
	expectedConfig := make(map[string]string, len(lock.Projects)*3)
	for _, project := range lock.Projects {
		section := "submodule." + managedSubmoduleName(project.ID)
		expectedConfig[section+".path"] = project.PathWithNamespace
		expectedConfig[section+".url"] = project.CloneURL
		expectedConfig[section+".branch"] = project.DefaultBranch
	}

	actualConfig := map[string]string{}
	if len(lock.Projects) > 0 {
		res, err := r.RunEnv(ctx, gitEnv, "git", "-C", root, "config", "-f", ".gitmodules", "--get-regexp", `^submodule\..*\.(path|url|branch)$`)
		if err != nil || !res.Ok() {
			return Check{Name: "lock/module/gitlink agreement", Status: StatusFail, Detail: commandFailureDetail(err, res, "cannot read .gitmodules"), Fix: fix}
		}
		for _, line := range strings.Split(strings.TrimSpace(string(res.Stdout)), "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), " ")
			if !ok {
				key, value, ok = strings.Cut(strings.TrimSpace(line), "\t")
			}
			if !ok || key == "" {
				return Check{Name: "lock/module/gitlink agreement", Status: StatusFail, Detail: "malformed .gitmodules configuration", Fix: fix}
			}
			actualConfig[key] = strings.TrimSpace(value)
		}
	}
	if !equalStringMap(expectedConfig, actualConfig) {
		return Check{Name: "lock/module/gitlink agreement", Status: StatusFail, Detail: ".gitmodules does not exactly match the managed lock", Fix: fix}
	}

	res, err := r.RunEnv(ctx, gitEnv, "git", "-C", root, "ls-tree", "-rz", "HEAD")
	if err != nil || !res.Ok() {
		return Check{Name: "lock/module/gitlink agreement", Status: StatusFail, Detail: commandFailureDetail(err, res, "cannot inspect committed gitlinks"), Fix: fix}
	}
	gitlinks := parseGitlinks(res.Stdout)
	if len(gitlinks) != len(lock.Projects) {
		return Check{Name: "lock/module/gitlink agreement", Status: StatusFail, Detail: "committed gitlink count does not match the managed lock", Fix: fix}
	}
	for _, project := range lock.Projects {
		if gitlinks[project.PathWithNamespace] != project.DefaultCommit {
			return Check{Name: "lock/module/gitlink agreement", Status: StatusFail, Detail: fmt.Sprintf("project %d gitlink does not match its locked commit", project.ID), Fix: fix}
		}
		dest := filepath.Join(root, filepath.FromSlash(project.PathWithNamespace))
		if _, statErr := os.Lstat(dest); errors.Is(statErr, os.ErrNotExist) {
			return Check{Name: "lock/module/gitlink agreement", Status: StatusFail, Detail: fmt.Sprintf("project %d submodule is not materialized", project.ID), Fix: "run `git submodule update --init` only after confirming the committed lock and .gitmodules agree"}
		} else if statErr != nil {
			return Check{Name: "lock/module/gitlink agreement", Status: StatusFail, Detail: fmt.Sprintf("project %d submodule cannot be inspected", project.ID), Fix: fix}
		}
		marker, statErr := os.Lstat(filepath.Join(dest, ".git"))
		if statErr != nil || !(marker.IsDir() || marker.Mode().IsRegular()) {
			return Check{Name: "lock/module/gitlink agreement", Status: StatusFail, Detail: fmt.Sprintf("project %d has no Git repository marker", project.ID), Fix: fix}
		}
		head, runErr := r.RunEnv(ctx, gitEnv, "git", "-C", dest, "rev-parse", "--verify", "HEAD")
		if runErr != nil || !head.Ok() || !strings.EqualFold(strings.TrimSpace(string(head.Stdout)), project.DefaultCommit) {
			return Check{Name: "lock/module/gitlink agreement", Status: StatusFail, Detail: fmt.Sprintf("project %d work tree HEAD does not match its locked commit", project.ID), Fix: fix}
		}
	}
	return Check{Name: "lock/module/gitlink agreement", Status: StatusOK, Detail: fmt.Sprintf("%d submodule snapshot(s) agree", len(lock.Projects))}
}

func equalStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func parseGitlinks(data []byte) map[string]string {
	out := map[string]string{}
	for _, record := range strings.Split(string(data), "\x00") {
		metadata, path, ok := strings.Cut(record, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(metadata)
		if len(fields) == 3 && fields[0] == "160000" && fields[1] == "commit" {
			out[path] = strings.ToLower(fields[2])
		}
	}
	return out
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
