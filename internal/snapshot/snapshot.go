// Package snapshot defines the atomic, immutable bundle that will replace the
// independently published shard, rank, graph, cluster, and dense sidecars.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	FormatVersion = 1
	ManifestName  = "snapshot.json"
	CurrentName   = "CURRENT"
	SnapshotsDir  = "snapshots"
	StagingDir    = ".staging"
)

var ErrNoCurrent = errors.New("snapshot: no current snapshot")

// Manifest is the completeness marker for one immutable index generation.
// It is written last, after every listed artifact has been fsynced and hashed.
type Manifest struct {
	Version           int                  `json:"version"`
	ID                string               `json:"id"`
	Sequence          uint64               `json:"sequence"`
	ParentID          string               `json:"parent_id,omitempty"`
	CreatedAt         time.Time            `json:"created_at"`
	Producer          Producer             `json:"producer"`
	CorpusFingerprint string               `json:"corpus_fingerprint"`
	ServingRoot       string               `json:"serving_root"`
	Capabilities      []string             `json:"capabilities,omitempty"`
	Components        map[string]Component `json:"components"`
}

type Producer struct {
	Version   string   `json:"version,omitempty"`
	Commit    string   `json:"commit,omitempty"`
	GoVersion string   `json:"go_version,omitempty"`
	BuildTags []string `json:"build_tags,omitempty"`
}

// Component records one independently built part of the bundle. Artifacts are
// still ordinary files, but are valid only as members of this manifest.
type Component struct {
	Format           string            `json:"format"`
	Version          int               `json:"version"`
	InputFingerprint string            `json:"input_fingerprint,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	Artifacts        []Artifact        `json:"artifacts"`
}

type Artifact struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Resolved is either a manifest-backed snapshot or a legacy shard directory.
// The legacy result is the compatibility bridge while builders and serving move
// to snapshots; callers can consume ShardDir without guessing the layout.
type Resolved struct {
	ID       string
	Root     string
	Manifest *Manifest
	Legacy   bool
}

func (r Resolved) ShardDir() string {
	if r.Legacy || r.Manifest == nil || r.Manifest.ServingRoot == "" {
		return r.Root
	}
	return filepath.Join(r.Root, filepath.FromSlash(r.Manifest.ServingRoot))
}

func SnapshotPath(indexDir, id string) string {
	return filepath.Join(indexDir, SnapshotsDir, id)
}

func Validate(m *Manifest) error {
	if m == nil {
		return errors.New("snapshot: nil manifest")
	}
	if m.Version != FormatVersion {
		return fmt.Errorf("snapshot: manifest version %d is unsupported (want %d)", m.Version, FormatVersion)
	}
	if err := validateID(m.ID); err != nil {
		return err
	}
	if m.Sequence == 0 {
		return errors.New("snapshot: sequence must be positive")
	}
	if m.ParentID != "" {
		if err := validateID(m.ParentID); err != nil {
			return fmt.Errorf("snapshot: parent_id: %w", err)
		}
	}
	if m.CreatedAt.IsZero() {
		return errors.New("snapshot: created_at is required")
	}
	if err := validateRelativePath(m.ServingRoot, true); err != nil {
		return fmt.Errorf("snapshot: serving_root: %w", err)
	}
	if len(m.Components) == 0 {
		return errors.New("snapshot: at least one component is required")
	}
	seen := make(map[string]string)
	for name, component := range m.Components {
		if name == "" {
			return errors.New("snapshot: component name is empty")
		}
		if component.Format == "" || component.Version <= 0 {
			return fmt.Errorf("snapshot: component %q needs a format and positive version", name)
		}
		if len(component.Artifacts) == 0 {
			return fmt.Errorf("snapshot: component %q has no artifacts", name)
		}
		for _, artifact := range component.Artifacts {
			if err := validateRelativePath(artifact.Path, false); err != nil {
				return fmt.Errorf("snapshot: component %q artifact: %w", name, err)
			}
			if owner, duplicate := seen[artifact.Path]; duplicate {
				return fmt.Errorf("snapshot: artifact %q belongs to both %q and %q", artifact.Path, owner, name)
			}
			seen[artifact.Path] = name
			if artifact.Size < 0 {
				return fmt.Errorf("snapshot: artifact %q has negative size", artifact.Path)
			}
			decoded, err := hex.DecodeString(artifact.SHA256)
			if err != nil || len(decoded) != sha256.Size {
				return fmt.Errorf("snapshot: artifact %q has invalid SHA-256", artifact.Path)
			}
		}
	}
	return validateSemanticComponent(m)
}

func validateID(id string) error {
	if id == "" || len(id) > 128 {
		return fmt.Errorf("snapshot: invalid id %q", id)
	}
	for i, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || (i > 0 && (r == '-' || r == '_' || r == '.')) {
			continue
		}
		return fmt.Errorf("snapshot: invalid id %q", id)
	}
	return nil
}

func validateRelativePath(path string, directory bool) error {
	if path == "" {
		if directory {
			return nil
		}
		return errors.New("empty path")
	}
	if filepath.IsAbs(path) || strings.Contains(path, "\\") {
		return fmt.Errorf("path %q must be slash-separated and relative", path)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean != path || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path %q is not clean", path)
	}
	return nil
}

func CaptureArtifact(root, relativePath string) (Artifact, error) {
	if err := validateRelativePath(relativePath, false); err != nil {
		return Artifact{}, err
	}
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	info, err := os.Lstat(path)
	if err != nil {
		return Artifact{}, err
	}
	if !info.Mode().IsRegular() {
		return Artifact{}, fmt.Errorf("snapshot: artifact %q is not a regular file", relativePath)
	}
	f, err := os.Open(path)
	if err != nil {
		return Artifact{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return Artifact{}, err
	}
	return Artifact{Path: relativePath, Size: info.Size(), SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func ValidateFiles(root string, m *Manifest) error {
	if err := Validate(m); err != nil {
		return err
	}
	servingRoot := root
	if m.ServingRoot != "" {
		servingRoot = filepath.Join(root, filepath.FromSlash(m.ServingRoot))
	}
	if info, err := os.Stat(servingRoot); err != nil || !info.IsDir() {
		if err != nil {
			return fmt.Errorf("snapshot: serving root: %w", err)
		}
		return fmt.Errorf("snapshot: serving root %q is not a directory", m.ServingRoot)
	}
	listed := make(map[string]struct{})
	for name, component := range m.Components {
		for _, want := range component.Artifacts {
			listed[want.Path] = struct{}{}
			got, err := CaptureArtifact(root, want.Path)
			if err != nil {
				return fmt.Errorf("snapshot: validate %s/%s: %w", name, want.Path, err)
			}
			if got.Size != want.Size || got.SHA256 != want.SHA256 {
				return fmt.Errorf("snapshot: artifact %q failed integrity check", want.Path)
			}
		}
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ManifestName {
			return nil // the completeness marker describes, rather than hashes, itself
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("snapshot: unmanifested non-regular file %q", rel)
		}
		if _, ok := listed[rel]; !ok {
			return fmt.Errorf("snapshot: unmanifested artifact %q", rel)
		}
		return nil
	})
}

func Load(indexDir, id string) (*Manifest, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	return loadManifest(SnapshotPath(indexDir, id))
}

func loadManifest(root string) (*Manifest, error) {
	f, err := os.Open(filepath.Join(root, ManifestName))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 16<<20))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("snapshot: decode manifest: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("snapshot: manifest contains multiple JSON values")
		}
		return nil, fmt.Errorf("snapshot: decode manifest trailer: %w", err)
	}
	if err := Validate(&m); err != nil {
		return nil, err
	}
	if filepath.Base(root) != m.ID {
		return nil, fmt.Errorf("snapshot: directory %q contains manifest id %q", filepath.Base(root), m.ID)
	}
	return &m, nil
}

func Current(indexDir string) (*Manifest, string, error) {
	id, err := readCurrentID(indexDir)
	if err != nil {
		return nil, "", err
	}
	m, err := Load(indexDir, id)
	if err != nil {
		return nil, "", fmt.Errorf("snapshot: CURRENT points to unusable snapshot %q: %w", id, err)
	}
	return m, SnapshotPath(indexDir, id), nil
}

func readCurrentID(indexDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(indexDir, CurrentName))
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNoCurrent
	}
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if err := validateID(id); err != nil {
		return "", fmt.Errorf("snapshot: invalid CURRENT: %w", err)
	}
	return id, nil
}

func Resolve(indexDir string) (Resolved, error) {
	m, root, err := Current(indexDir)
	if err == nil {
		if err := VerifySemanticComponent(root, m); err != nil {
			return Resolved{}, err
		}
		return Resolved{ID: m.ID, Root: root, Manifest: m}, nil
	}
	if !errors.Is(err, ErrNoCurrent) {
		return Resolved{}, err
	}
	matches, globErr := filepath.Glob(filepath.Join(indexDir, "*.idx"))
	if globErr != nil {
		return Resolved{}, globErr
	}
	if len(matches) > 0 {
		return Resolved{Root: indexDir, Legacy: true}, nil
	}
	return Resolved{}, ErrNoCurrent
}

func List(indexDir string) ([]Manifest, error) {
	entries, err := os.ReadDir(filepath.Join(indexDir, SnapshotsDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]Manifest, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		m, err := Load(indexDir, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("snapshot: list %s: %w", entry.Name(), err)
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sequence != out[j].Sequence {
			return out[i].Sequence > out[j].Sequence
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
