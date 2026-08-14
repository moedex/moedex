package corpus

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const (
	// ManagedSchemaVersion is the only managed-corpus schema understood by this
	// release. Readers fail closed when they encounter another version.
	ManagedSchemaVersion = 1
	// ManagedDirName contains the ownership marker and acquisition snapshot.
	ManagedDirName = ".moedex"
	// CatalogFileName is the managed-corpus ownership marker.
	CatalogFileName = "corpus.json"
	// LockFileName is the exact acquisition snapshot consumed by indexing.
	LockFileName = "corpus.lock.json"
	// RefPolicyDefault selects only the enumerated default branch in schema v1.
	RefPolicyDefault = "default"
)

// GroupPolicy records the curated top-level GitLab namespaces. An empty list
// preserves Config's existing "all visible projects" behavior.
type GroupPolicy struct {
	TopLevelGroups []string `json:"top_level_groups"`
}

// Catalog is the versioned ownership marker for a Moedex-managed corpus.
// Its presence alone is not sufficient: callers must load and validate it.
type Catalog struct {
	Version     int         `json:"version"`
	Host        string      `json:"host"`
	GroupPolicy GroupPolicy `json:"group_policy"`
	RefPolicy   string      `json:"ref_policy"`
}

// NewCatalog builds the canonical version-1 marker for cfg.
func NewCatalog(cfg Config) (Catalog, error) {
	c := Catalog{
		Version: ManagedSchemaVersion,
		Host:    cfg.Host,
		GroupPolicy: GroupPolicy{
			TopLevelGroups: cloneStrings(cfg.Groups),
		},
		RefPolicy: RefPolicyDefault,
	}
	canonicalizeCatalog(&c)
	if err := c.Validate(); err != nil {
		return Catalog{}, err
	}
	return c, nil
}

// Validate checks the catalog without silently accepting a newer schema or a
// non-canonical marker. WriteCatalog canonicalizes caller-owned slices first.
func (c Catalog) Validate() error {
	if c.Version != ManagedSchemaVersion {
		return fmt.Errorf("unsupported managed corpus catalog version %d", c.Version)
	}
	if err := validatePinnedHost(c.Host); err != nil {
		return err
	}
	if c.RefPolicy != RefPolicyDefault {
		return fmt.Errorf("unsupported managed corpus ref policy %q", c.RefPolicy)
	}
	groups := c.GroupPolicy.TopLevelGroups
	if !sort.StringsAreSorted(groups) {
		return errors.New("managed corpus groups are not sorted")
	}
	seen := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		if err := validateGroup(group); err != nil {
			return err
		}
		if _, ok := seen[group]; ok {
			return fmt.Errorf("duplicate managed corpus group %q", group)
		}
		seen[group] = struct{}{}
	}
	return nil
}

// CatalogPath returns the marker path beneath root.
func CatalogPath(root string) string {
	return filepath.Join(root, ManagedDirName, CatalogFileName)
}

// LoadCatalog strictly decodes and validates root's ownership marker.
func LoadCatalog(root string) (Catalog, error) {
	var c Catalog
	if err := decodeStrictJSON(CatalogPath(root), &c); err != nil {
		return Catalog{}, fmt.Errorf("load managed corpus catalog: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Catalog{}, fmt.Errorf("validate managed corpus catalog: %w", err)
	}
	return c, nil
}

// WriteCatalog writes a canonical, fsync-durable ownership marker.
func WriteCatalog(root string, c Catalog) error {
	canonicalizeCatalog(&c)
	if err := c.Validate(); err != nil {
		return fmt.Errorf("validate managed corpus catalog: %w", err)
	}
	if err := writeCanonicalJSON(CatalogPath(root), c, nil); err != nil {
		return fmt.Errorf("write managed corpus catalog: %w", err)
	}
	return nil
}

func canonicalizeCatalog(c *Catalog) {
	c.GroupPolicy.TopLevelGroups = cloneStrings(c.GroupPolicy.TopLevelGroups)
	sort.Strings(c.GroupPolicy.TopLevelGroups)
}

func cloneStrings(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func validatePinnedHost(host string) error {
	if host == "" || host != strings.TrimSpace(host) || strings.HasPrefix(host, "-") ||
		strings.ContainsAny(host, "/\\@") || strings.ContainsFunc(host, unicode.IsControl) {
		return fmt.Errorf("invalid managed corpus host %q", host)
	}
	return nil
}

func validateGroup(group string) error {
	if group == "" || group != strings.TrimSpace(group) || group == "." || group == ".." ||
		strings.ContainsAny(group, "/\\") || strings.ContainsFunc(group, unicode.IsControl) {
		return fmt.Errorf("invalid managed corpus group %q", group)
	}
	return nil
}

func decodeStrictJSON(path string, dst any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

// writeCanonicalJSON writes to a sibling temporary file, fsyncs its contents,
// renames it over the destination, and best-effort fsyncs the parent directory.
// beforeRename is an internal fault-injection seam used by lifecycle tests.
func writeCanonicalJSON(path string, value any, beforeRename func(string) error) (err error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if f != nil {
			_ = f.Close()
		}
		_ = os.Remove(tmp)
	}()
	if err := f.Chmod(0o644); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	f = nil
	if beforeRename != nil {
		if err := beforeRename(tmp); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, openErr := os.Open(dir); openErr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
