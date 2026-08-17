package ingest

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const (
	// AIPrivacyFileName is the repository-root policy defined by TurnCommerce AI
	// Governance section 10. Reading this file is the one permitted bootstrap
	// read before repository content is processed.
	AIPrivacyFileName = ".ai-privacy.yml"
	// DefaultAIPrivacyLevel applies when the policy is absent or omits its global
	// level. Level 3 permits supported enterprise/no-training AI platforms.
	DefaultAIPrivacyLevel = 3
	restrictedAILevel     = 1
)

type privacyPolicy struct {
	globalLevel int
	overrides   []privacyOverride
}

type privacyOverride struct {
	path  string
	level int
}

type privacyPendingOverride struct {
	path  *string
	level *int
}

// PrivacyPolicyError identifies a policy that could not be read or validated.
// Primary build and refresh paths use this type to fail closed instead of
// treating the repository as an ordinary transient ingest skip.
type PrivacyPolicyError struct {
	Path string
	Err  error
}

func (e *PrivacyPolicyError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("invalid %s: %v", e.Path, e.Err)
	}
	return fmt.Sprintf("invalid %s: %v", AIPrivacyFileName, e.Err)
}

func (e *PrivacyPolicyError) Unwrap() error { return e.Err }

// IsPrivacyPolicyError reports whether err represents a fail-closed privacy
// policy failure.
func IsPrivacyPolicyError(err error) bool {
	var target *PrivacyPolicyError
	return errors.As(err, &target)
}

func loadPrivacyPolicy(repoDir string) (privacyPolicy, error) {
	policy := privacyPolicy{globalLevel: DefaultAIPrivacyLevel}
	policyPath := filepath.Join(repoDir, AIPrivacyFileName)
	info, err := os.Lstat(policyPath)
	if errors.Is(err, os.ErrNotExist) {
		return policy, nil
	}
	if err != nil {
		return privacyPolicy{}, privacyError(policyPath, fmt.Errorf("inspect policy: %w", err))
	}
	if !info.Mode().IsRegular() {
		return privacyPolicy{}, privacyError(policyPath, errors.New("policy must be a regular file, not a symlink or special file"))
	}
	data, err := os.ReadFile(policyPath)
	if err != nil {
		return privacyPolicy{}, privacyError(policyPath, fmt.Errorf("read policy: %w", err))
	}

	parsed, err := parsePrivacyPolicy(data)
	if err != nil {
		return privacyPolicy{}, privacyError(policyPath, fmt.Errorf("parse policy: %w", err))
	}
	return parsed, nil
}

func privacyError(policyPath string, err error) error {
	return &PrivacyPolicyError{Path: policyPath, Err: err}
}

// AIPrivacyFingerprint validates a repository's policy and returns a stable
// fingerprint of its effective rules. Missing and empty policies intentionally
// fingerprint as the documented level-3 default. Freshness manifests persist
// this value so an uncommitted policy change cannot be hidden by an unchanged
// git HEAD.
func AIPrivacyFingerprint(repoDir string) (string, error) {
	policy, err := loadPrivacyPolicy(repoDir)
	if err != nil {
		return "", err
	}
	return privacyPolicyFingerprint(policy), nil
}

// LSPWorkspaceAllowed reports whether an external language server may safely
// open repoDir. Unlike ingestion, an LSP can scan the entire workspace after a
// single allowed-file query, so any level-1 path makes the whole working tree
// ineligible unless a separately sanitized workspace is supplied. Policy parse
// and validation failures are returned so callers fail closed before launch.
func LSPWorkspaceAllowed(repoDir string) (bool, error) {
	policy, err := loadPrivacyPolicy(repoDir)
	if err != nil {
		return false, err
	}
	if policy.globalLevel == restrictedAILevel {
		return false, nil
	}
	for _, override := range policy.overrides {
		if override.level == restrictedAILevel {
			return false, nil
		}
	}
	return true, nil
}

func privacyPolicyFingerprint(policy privacyPolicy) string {
	overrides := append([]privacyOverride(nil), policy.overrides...)
	sort.Slice(overrides, func(i, j int) bool {
		if overrides[i].path == overrides[j].path {
			return overrides[i].level < overrides[j].level
		}
		return overrides[i].path < overrides[j].path
	})
	h := sha256.New()
	fmt.Fprintf(h, "moedex-ai-privacy-v1\nglobal:%d\n", policy.globalLevel)
	for _, override := range overrides {
		fmt.Fprintf(h, "%s:%d\n", override.path, override.level)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func validPrivacyLevel(level int) bool { return level >= 1 && level <= 4 }

// parsePrivacyPolicy accepts only the documented .ai-privacy.yml subset. It is
// intentionally dependency-free and strict: unsupported YAML features or
// unknown fields fail closed rather than being interpreted permissively.
func parsePrivacyPolicy(data []byte) (privacyPolicy, error) {
	policy := privacyPolicy{globalLevel: DefaultAIPrivacyLevel}
	var (
		current     *privacyPendingOverride
		inOverrides bool
		sawGlobal   bool
	)
	finishCurrent := func() error {
		if current == nil {
			return nil
		}
		if current.path == nil || current.level == nil {
			return errors.New("each privacy_levels entry requires path and privacy_level")
		}
		normalized, err := normalizePrivacyPath(*current.path)
		if err != nil {
			return err
		}
		policy.overrides = append(policy.overrides, privacyOverride{path: normalized, level: *current.level})
		current = nil
		return nil
	}

	lines := strings.Split(strings.TrimPrefix(string(data), "\ufeff"), "\n")
	for i, sourceLine := range lines {
		line, err := stripPrivacyComment(strings.TrimSuffix(sourceLine, "\r"))
		if err != nil {
			return privacyPolicy{}, fmt.Errorf("line %d: %w", i+1, err)
		}
		line = strings.TrimRight(line, " ")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.ContainsRune(line, '\t') {
			return privacyPolicy{}, fmt.Errorf("tabs are not allowed (line %d)", i+1)
		}

		if strings.HasPrefix(line, "global_privacy_level:") && !strings.HasPrefix(line, " ") {
			if sawGlobal || inOverrides {
				return privacyPolicy{}, errors.New("global_privacy_level must appear once before privacy_levels")
			}
			value := strings.TrimSpace(strings.TrimPrefix(line, "global_privacy_level:"))
			level, err := parsePrivacyLevel(value, "global_privacy_level")
			if err != nil {
				return privacyPolicy{}, err
			}
			policy.globalLevel = level
			sawGlobal = true
			continue
		}
		if line == "privacy_levels:" {
			if inOverrides {
				return privacyPolicy{}, errors.New("privacy_levels must appear at most once")
			}
			inOverrides = true
			continue
		}
		if inOverrides && strings.HasPrefix(line, "  - ") {
			if err := finishCurrent(); err != nil {
				return privacyPolicy{}, err
			}
			current = &privacyPendingOverride{}
			if err := assignPrivacyField(current, strings.TrimPrefix(line, "  - ")); err != nil {
				return privacyPolicy{}, fmt.Errorf("line %d: %w", i+1, err)
			}
			continue
		}
		if inOverrides && current != nil && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     ") {
			if err := assignPrivacyField(current, strings.TrimPrefix(line, "    ")); err != nil {
				return privacyPolicy{}, fmt.Errorf("line %d: %w", i+1, err)
			}
			continue
		}
		return privacyPolicy{}, fmt.Errorf("unsupported %s syntax on line %d", AIPrivacyFileName, i+1)
	}
	if err := finishCurrent(); err != nil {
		return privacyPolicy{}, err
	}
	return validatePrivacyPolicy(policy)
}

func assignPrivacyField(current *privacyPendingOverride, field string) error {
	key, raw, ok := strings.Cut(field, ":")
	if !ok || strings.TrimSpace(raw) == "" {
		return errors.New("privacy_levels fields require a value")
	}
	value, err := parsePrivacyScalar(raw)
	if err != nil {
		return err
	}
	switch strings.TrimSpace(key) {
	case "path":
		if current.path != nil {
			return errors.New("duplicate path in privacy_levels entry")
		}
		current.path = &value
	case "privacy_level":
		if current.level != nil {
			return errors.New("duplicate privacy_level in privacy_levels entry")
		}
		level, err := parsePrivacyLevel(value, "privacy_level")
		if err != nil {
			return err
		}
		current.level = &level
	default:
		return fmt.Errorf("unknown privacy_levels field %q", strings.TrimSpace(key))
	}
	return nil
}

func stripPrivacyComment(line string) (string, error) {
	var single, double bool
	for i, r := range line {
		switch {
		case r == '\'' && !double:
			single = !single
		case r == '"' && !single && (i == 0 || line[i-1] != '\\'):
			double = !double
		case r == '#' && !single && !double:
			return line[:i], nil
		}
	}
	if single || double {
		return "", errors.New("unterminated quoted value")
	}
	return line, nil
}

func parsePrivacyScalar(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "\"") {
		var decoded string
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			return "", errors.New("invalid double-quoted value")
		}
		return decoded, nil
	}
	if strings.HasPrefix(value, "'") {
		if len(value) < 2 || !strings.HasSuffix(value, "'") {
			return "", errors.New("invalid single-quoted value")
		}
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'"), nil
	}
	return value, nil
}

func parsePrivacyLevel(raw, label string) (int, error) {
	value, err := parsePrivacyScalar(raw)
	if err != nil {
		return 0, err
	}
	if len(value) != 1 || value[0] < '1' || value[0] > '4' {
		return 0, fmt.Errorf("%s must be an integer from 1 to 4", label)
	}
	return int(value[0] - '0'), nil
}

func validatePrivacyPolicy(policy privacyPolicy) (privacyPolicy, error) {
	if !validPrivacyLevel(policy.globalLevel) {
		return privacyPolicy{}, fmt.Errorf("global_privacy_level must be an integer from 1 to 4, got %d", policy.globalLevel)
	}
	seen := make(map[string]struct{}, len(policy.overrides))
	for _, item := range policy.overrides {
		if !validPrivacyLevel(item.level) {
			return privacyPolicy{}, fmt.Errorf("%s must use an integer from 1 to 4", item.path)
		}
		if _, ok := seen[item.path]; ok {
			return privacyPolicy{}, fmt.Errorf("duplicate privacy path %q", item.path)
		}
		seen[item.path] = struct{}{}
		if item.level > policy.globalLevel {
			return privacyPolicy{}, fmt.Errorf("%s cannot be less restrictive than global_privacy_level", item.path)
		}
	}
	for i, child := range policy.overrides {
		for j, parent := range policy.overrides {
			if i == j || !privacyPathContains(parent.path, child.path) {
				continue
			}
			if child.level > parent.level {
				return privacyPolicy{}, fmt.Errorf("%s cannot be less restrictive than ancestor %s", child.path, parent.path)
			}
		}
	}
	return policy, nil
}

func normalizePrivacyPath(value string) (string, error) {
	if value == "" || !strings.HasPrefix(value, "/") || strings.Contains(value, "\\") ||
		strings.ContainsRune(value, 0) || strings.ContainsFunc(value, unicode.IsControl) {
		return "", errors.New("override path must be repository-relative and begin with /")
	}
	folder := strings.HasSuffix(value, "/")
	trimmed := strings.TrimSuffix(value, "/")
	if trimmed == "" || trimmed == "/" || path.Clean(trimmed) != trimmed {
		return "", fmt.Errorf("unsafe override path %q", value)
	}
	for _, segment := range strings.Split(strings.TrimPrefix(trimmed, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("unsafe override path %q", value)
		}
	}
	if folder {
		return trimmed + "/", nil
	}
	return trimmed, nil
}

func privacyPathContains(parent, child string) bool {
	if strings.HasSuffix(parent, "/") {
		return child == strings.TrimSuffix(parent, "/") || strings.HasPrefix(child, parent)
	}
	return child == parent
}

func (p privacyPolicy) effectiveLevel(rel string) int {
	target := "/" + filepath.ToSlash(rel)
	level := p.globalLevel
	for _, override := range p.overrides {
		if privacyPathContains(override.path, target) && override.level < level {
			level = override.level
		}
	}
	return level
}

func (p privacyPolicy) restricted(rel string) bool {
	return p.effectiveLevel(rel) == restrictedAILevel
}
