package corpus

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// LockStatus describes whether a project advanced during the most recent sync.
type LockStatus string

const (
	LockStatusCurrent        LockStatus = "current"
	LockStatusCarriedForward LockStatus = "carried_forward"
)

// LockedProject is one exact, stable-ID project snapshot.
type LockedProject struct {
	ID                int64      `json:"id"`
	PathWithNamespace string     `json:"path_with_namespace"`
	CloneURL          string     `json:"clone_url"`
	DefaultBranch     string     `json:"default_branch"`
	DefaultCommit     string     `json:"default_commit"`
	Status            LockStatus `json:"status"`
}

// Lock is the deterministic acquisition-to-indexing handoff. Projects are
// serialized in ascending GitLab project-ID order.
type Lock struct {
	Version             int             `json:"version"`
	EnumerationComplete bool            `json:"enumeration_complete"`
	Projects            []LockedProject `json:"projects"`
}

// NewLock constructs and validates a canonical lock for host.
func NewLock(host string, enumerationComplete bool, projects []LockedProject) (Lock, error) {
	l := Lock{
		Version:             ManagedSchemaVersion,
		EnumerationComplete: enumerationComplete,
		Projects:            append([]LockedProject(nil), projects...),
	}
	canonicalizeLock(&l)
	if err := l.Validate(host); err != nil {
		return Lock{}, err
	}
	return l, nil
}

// Validate checks schema, stable identity, paths, URLs, commits, and ordering.
func (l Lock) Validate(host string) error {
	if l.Version != ManagedSchemaVersion {
		return fmt.Errorf("unsupported managed corpus lock version %d", l.Version)
	}
	if err := validatePinnedHost(host); err != nil {
		return err
	}
	seenIDs := make(map[int64]struct{}, len(l.Projects))
	seenPaths := make(map[string]int64, len(l.Projects))
	var previousID int64
	for i, project := range l.Projects {
		if project.ID <= 0 {
			return fmt.Errorf("managed corpus project ID must be positive: %d", project.ID)
		}
		if _, ok := seenIDs[project.ID]; ok {
			return fmt.Errorf("duplicate managed corpus project ID %d", project.ID)
		}
		if i > 0 && project.ID < previousID {
			return fmt.Errorf("managed corpus projects are not strictly sorted by ID at %d", project.ID)
		}
		previousID = project.ID
		seenIDs[project.ID] = struct{}{}
		if err := validateManagedPath(project.PathWithNamespace); err != nil {
			return fmt.Errorf("project %d: %w", project.ID, err)
		}
		if other, ok := seenPaths[project.PathWithNamespace]; ok {
			return fmt.Errorf("duplicate managed corpus path %q for projects %d and %d", project.PathWithNamespace, other, project.ID)
		}
		seenPaths[project.PathWithNamespace] = project.ID
		if err := validateCloneURL(host, project.CloneURL); err != nil {
			return fmt.Errorf("project %d: %w", project.ID, err)
		}
		if project.DefaultBranch == "" {
			return fmt.Errorf("project %d has no default branch", project.ID)
		}
		if err := validRef(project.DefaultBranch); err != nil {
			return fmt.Errorf("project %d: %w", project.ID, err)
		}
		if !validGitObjectID(project.DefaultCommit) {
			return fmt.Errorf("project %d has malformed or absent default commit %q", project.ID, project.DefaultCommit)
		}
		switch project.Status {
		case LockStatusCurrent, LockStatusCarriedForward:
		default:
			return fmt.Errorf("project %d has invalid lock status %q", project.ID, project.Status)
		}
	}
	for i := range l.Projects {
		for j := i + 1; j < len(l.Projects); j++ {
			left := l.Projects[i].PathWithNamespace
			right := l.Projects[j].PathWithNamespace
			if managedPathsOverlap(left, right) {
				return fmt.Errorf("managed corpus paths overlap: %q and %q", left, right)
			}
		}
	}
	return nil
}

// LockPath returns the acquisition snapshot path beneath root.
func LockPath(root string) string {
	return filepath.Join(root, ManagedDirName, LockFileName)
}

// LoadLock strictly decodes and validates root's acquisition snapshot.
func LoadLock(root, host string) (Lock, error) {
	var l Lock
	if err := decodeStrictJSON(LockPath(root), &l); err != nil {
		return Lock{}, fmt.Errorf("load managed corpus lock: %w", err)
	}
	if err := l.Validate(host); err != nil {
		return Lock{}, fmt.Errorf("validate managed corpus lock: %w", err)
	}
	return l, nil
}

// WriteLock writes a canonical, fsync-durable acquisition snapshot.
func WriteLock(root, host string, l Lock) error {
	canonicalizeLock(&l)
	if err := l.Validate(host); err != nil {
		return fmt.Errorf("validate managed corpus lock: %w", err)
	}
	if err := writeCanonicalJSON(LockPath(root), l, nil); err != nil {
		return fmt.Errorf("write managed corpus lock: %w", err)
	}
	return nil
}

func canonicalizeLock(l *Lock) {
	l.Projects = append([]LockedProject(nil), l.Projects...)
	if l.Projects == nil {
		l.Projects = make([]LockedProject, 0)
	}
	for i := range l.Projects {
		l.Projects[i].DefaultCommit = strings.ToLower(l.Projects[i].DefaultCommit)
	}
	sort.Slice(l.Projects, func(i, j int) bool { return l.Projects[i].ID < l.Projects[j].ID })
}

func validateManagedPath(value string) error {
	if value == "" || value != strings.TrimSpace(value) || path.IsAbs(value) ||
		value == "." || value == ".." || path.Clean(value) != value ||
		strings.Contains(value, "\\") || strings.ContainsFunc(value, unicode.IsControl) {
		return fmt.Errorf("unsafe managed corpus path %q", value)
	}
	parts := strings.Split(value, "/")
	if len(parts) < 2 || parts[0] == ManagedDirName || parts[0] == ".git" {
		return fmt.Errorf("unsafe managed corpus path %q", value)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("unsafe managed corpus path %q", value)
		}
	}
	return nil
}

func validateCloneURL(host, raw string) error {
	if raw == "" || raw != strings.TrimSpace(raw) || strings.HasPrefix(raw, "-") ||
		strings.ContainsFunc(raw, unicode.IsControl) {
		return fmt.Errorf("invalid clone URL %q", raw)
	}

	// SCP-like SSH syntax: git@host:namespace/project.git.
	if !strings.Contains(raw, "://") {
		at := strings.IndexByte(raw, '@')
		colon := strings.IndexByte(raw, ':')
		if at <= 0 || colon <= at+1 || colon == len(raw)-1 || strings.Contains(raw[:at], ":") {
			return fmt.Errorf("invalid clone URL %q", raw)
		}
		if !strings.EqualFold(raw[at+1:colon], host) {
			return fmt.Errorf("clone URL host %q does not match pinned host %q", raw[at+1:colon], host)
		}
		return nil
	}

	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.Path == "" {
		return fmt.Errorf("invalid clone URL %q", raw)
	}
	if u.Scheme != "ssh" && u.Scheme != "https" {
		return fmt.Errorf("unsupported clone URL scheme %q", u.Scheme)
	}
	if !strings.EqualFold(u.Hostname(), host) {
		return fmt.Errorf("clone URL host %q does not match pinned host %q", u.Hostname(), host)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("clone URL must not contain query or fragment data")
	}
	if u.Scheme == "https" && u.User != nil {
		return fmt.Errorf("clone URL must not contain credentials")
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			return fmt.Errorf("clone URL must not contain credentials")
		}
	}
	return nil
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

func managedPathsOverlap(left, right string) bool {
	return strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}
