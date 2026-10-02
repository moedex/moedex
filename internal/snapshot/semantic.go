package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// These constants describe an offline attachment, not a semantic query index.
// Its fingerprint is in Manifest.CorpusFingerprint's content-hash domain, not
// the legacy rank/graph serving fingerprint domain.
const (
	SemanticComponent = "semantic"
	SemanticFormat    = "moedex.semantic"
	SemanticVersion   = 1
	SemanticPath      = "semantic/artifact.json"
	SemanticMaxBytes  = int64(64 << 20)
	SemanticCoverage  = "explicit-project-subset"
	SemanticUsage     = "offline-only"
)

func validateSemanticComponent(m *Manifest) error {
	if err := validateSemanticIndexComponent(m); err != nil {
		return err
	}
	for name, c := range m.Components {
		if name == SemanticComponent {
			continue
		}
		if c.Format == SemanticFormat {
			return fmt.Errorf("snapshot: semantic format requires component %q", SemanticComponent)
		}
		for _, a := range c.Artifacts {
			if a.Path == SemanticPath {
				return fmt.Errorf("snapshot: semantic artifact requires component %q", SemanticComponent)
			}
		}
	}
	component, ok := m.Components[SemanticComponent]
	if !ok {
		return nil
	}
	if component.Format != SemanticFormat || component.Version != SemanticVersion {
		return fmt.Errorf("snapshot: unsupported semantic component format/version")
	}
	if m.CorpusFingerprint == "" || component.InputFingerprint != m.CorpusFingerprint {
		return fmt.Errorf("snapshot: semantic component source fingerprint mismatch")
	}
	if component.Metadata["coverage"] != SemanticCoverage || component.Metadata["usage"] != SemanticUsage {
		return fmt.Errorf("snapshot: semantic component requires explicit-project-subset coverage and offline-only usage")
	}
	if len(component.Artifacts) != 1 {
		return fmt.Errorf("snapshot: semantic component requires exactly one artifact")
	}
	a := component.Artifacts[0]
	if a.Path != SemanticPath || a.Size < 1 || a.Size > SemanticMaxBytes {
		return fmt.Errorf("snapshot: semantic artifact requires path %q and size 1..%d", SemanticPath, SemanticMaxBytes)
	}
	digest, err := hex.DecodeString(a.SHA256)
	if err != nil || len(digest) != sha256.Size {
		return fmt.Errorf("snapshot: semantic artifact has invalid SHA-256")
	}
	return nil
}

// VerifySemanticComponent checks the optional audit attachment and its compact
// lookup index. It does not deserialize records or rehash the serving corpus. The caller's
// snapshot root may be either a private transaction stage or a published root.
// Staging must separately validate artifact completeness and source membership;
// this verifies the manifest-bound file's integrity, not compiler truth.
func VerifySemanticComponent(root string, m *Manifest) error {
	if m == nil {
		return fmt.Errorf("snapshot: nil manifest")
	}
	if err := validateSemanticComponent(m); err != nil {
		return err
	}
	for _, name := range []string{SemanticComponent, SemanticIndexComponent} {
		if component, ok := m.Components[name]; ok {
			if err := verifySemanticFile(root, component.Artifacts[0]); err != nil {
				return fmt.Errorf("snapshot: %s: %w", name, err)
			}
		}
	}
	return nil
}

func verifySemanticFile(root string, want Artifact) error {
	confined, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("snapshot: semantic root: %w", err)
	}
	defer confined.Close()
	// Reject symlinks even when they point inside the root. OpenRoot additionally
	// prevents a racing replacement from escaping the root during the open.
	parts := strings.Split(want.Path, "/")
	for i := range parts {
		path := strings.Join(parts[:i+1], "/")
		info, err := confined.Lstat(path)
		if err != nil {
			return fmt.Errorf("snapshot: semantic artifact: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) || (i == len(parts)-1 && !info.Mode().IsRegular()) {
			return fmt.Errorf("snapshot: semantic artifact path contains symlink or invalid file type")
		}
	}
	f, err := confined.Open(want.Path)
	if err != nil {
		return fmt.Errorf("snapshot: semantic artifact: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("snapshot: semantic artifact stat: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != want.Size {
		return fmt.Errorf("snapshot: semantic artifact size/type mismatch")
	}
	hash := sha256.New()
	// Read one byte beyond the advertised size to catch growth without ever
	// allocating proportional to the artifact or trusting a prior file stat.
	n, err := io.Copy(hash, io.LimitReader(f, want.Size+1))
	if err != nil {
		return fmt.Errorf("snapshot: semantic artifact read: %w", err)
	}
	if n != want.Size || hex.EncodeToString(hash.Sum(nil)) != want.SHA256 {
		return fmt.Errorf("snapshot: semantic artifact failed integrity check")
	}
	return nil
}
