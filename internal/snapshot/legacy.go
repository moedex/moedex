package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// StageLegacy copies a complete legacy servable shard directory into tx as one
// transitional component. It deliberately copies rather than hard-linking:
// immutable snapshots must not share inodes with files an old refresh workflow
// may still replace or modify.
func StageLegacy(tx *Transaction, legacyDir string, producer Producer) (*Manifest, error) {
	if tx == nil || tx.closed {
		return nil, fmt.Errorf("snapshot: transaction is closed")
	}
	legacyAbs, err := filepath.Abs(legacyDir)
	if err != nil {
		return nil, err
	}
	stageAbs, err := filepath.Abs(tx.dir)
	if err != nil {
		return nil, err
	}
	if within(legacyAbs, stageAbs) {
		return nil, fmt.Errorf("snapshot: staging directory %s is inside legacy source %s", stageAbs, legacyAbs)
	}
	serveRoot := filepath.Join(tx.dir, "serve")
	if err := os.MkdirAll(serveRoot, 0o755); err != nil {
		return nil, err
	}

	hasShard := false
	err = filepath.WalkDir(legacyAbs, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(legacyAbs, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("snapshot: legacy source contains symlink %s", path)
		}
		destination := filepath.Join(serveRoot, rel)
		if entry.IsDir() {
			return fmt.Errorf("snapshot: legacy serving directory must be flat; found %s", path)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("snapshot: legacy source contains non-regular file %s", path)
		}
		if !legacyServingFile(filepath.Base(rel)) {
			return fmt.Errorf("snapshot: refusing unrecognized legacy file %s", path)
		}
		if strings.HasSuffix(rel, ".idx") {
			hasShard = true
		}
		if err := copyFile(path, destination, entry); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !hasShard {
		return nil, fmt.Errorf("snapshot: legacy source %s has no top-level *.idx shards", legacyAbs)
	}
	return CaptureServingTree(tx, producer)
}

func legacyServingFile(base string) bool {
	return strings.HasSuffix(base, ".idx") || strings.HasPrefix(base, "corpus-") ||
		base == "manifest.json" || base == "blobmanifest.json" || base == "blobs.dat"
}

// CaptureServingTree inventories files a builder wrote under tx.Dir()/serve
// without copying them. It is the direct-build path into an atomic snapshot.
func CaptureServingTree(tx *Transaction, producer Producer) (*Manifest, error) {
	if tx == nil || tx.closed {
		return nil, fmt.Errorf("snapshot: transaction is closed")
	}
	serveRoot := filepath.Join(tx.dir, "serve")
	var artifacts []Artifact
	err := filepath.WalkDir(serveRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == serveRoot || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("snapshot: serving tree contains non-regular file %s", path)
		}
		rel, err := filepath.Rel(tx.dir, path)
		if err != nil {
			return err
		}
		artifact, err := CaptureArtifact(tx.dir, filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(artifacts) == 0 {
		return nil, fmt.Errorf("snapshot: serving tree %s contains no files", serveRoot)
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })

	capabilities := legacyCapabilities(artifacts)
	return &Manifest{
		Producer:          producer,
		CorpusFingerprint: servingCorpusFingerprint(artifacts),
		ServingRoot:       "serve",
		Capabilities:      capabilities,
		Components: map[string]Component{
			"legacy_serving_tree": {
				Format:    "moedex-shard-directory",
				Version:   1,
				Artifacts: artifacts,
			},
		},
	}, nil
}

func servingCorpusFingerprint(artifacts []Artifact) string {
	h := sha256.New()
	for _, artifact := range artifacts {
		base := filepath.Base(filepath.FromSlash(artifact.Path))
		if !strings.HasSuffix(base, ".idx") && base != "blobs.dat" {
			continue
		}
		_, _ = io.WriteString(h, base)
		_, _ = io.WriteString(h, "\x00")
		_, _ = io.WriteString(h, artifact.SHA256)
		_, _ = io.WriteString(h, "\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func copyFile(source, destination string, entry fs.DirEntry) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := entry.Info()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func legacyCapabilities(artifacts []Artifact) []string {
	capabilities := map[string]bool{}
	for _, artifact := range artifacts {
		base := filepath.Base(filepath.FromSlash(artifact.Path))
		switch {
		case strings.HasSuffix(base, ".idx"):
			capabilities["search"] = true
		case strings.Contains(base, "token"):
			capabilities["rank_tokens"] = true
		case strings.Contains(base, "symbol"):
			capabilities["rank_symbols"] = true
		case strings.Contains(base, "cluster"):
			capabilities["graph_clusters"] = true
		case strings.Contains(base, "graph"):
			capabilities["graph"] = true
		case strings.Contains(base, "embedding"):
			capabilities["dense"] = true
		}
	}
	out := make([]string, 0, len(capabilities))
	for capability := range capabilities {
		out = append(out, capability)
	}
	sort.Strings(out)
	return out
}
