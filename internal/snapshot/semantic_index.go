package snapshot

import "fmt"

const (
	SemanticIndexComponent       = "semantic_index"
	SemanticIndexFormat          = "moedex.semantic-index"
	SemanticIndexVersion         = 6
	SemanticIndexPath            = "semantic/index.msi"
	SemanticIndexMaxBytes  int64 = 128 << 20
	SemanticIndexScope           = "recorded-compiler-context"
)

// The compact query index is derived from one independently validated audit
// artifact. Its hash binding is explicit, so an index from another capture
// cannot silently join this generation even when source text is shared.
func validateSemanticIndexComponent(m *Manifest) error {
	for name, c := range m.Components {
		if name == SemanticIndexComponent {
			continue
		}
		if c.Format == SemanticIndexFormat {
			return fmt.Errorf("snapshot: semantic index format requires component %q", SemanticIndexComponent)
		}
		for _, a := range c.Artifacts {
			if a.Path == SemanticIndexPath {
				return fmt.Errorf("snapshot: semantic index path requires component %q", SemanticIndexComponent)
			}
		}
	}
	c, ok := m.Components[SemanticIndexComponent]
	if !ok {
		return nil
	}
	audit, ok := m.Components[SemanticComponent]
	if !ok || len(audit.Artifacts) != 1 {
		return fmt.Errorf("snapshot: semantic index requires its audit attachment")
	}
	if c.Format != SemanticIndexFormat || (c.Version < 1 || c.Version > SemanticIndexVersion) {
		return fmt.Errorf("snapshot: unsupported semantic index format/version")
	}
	if m.CorpusFingerprint == "" || c.InputFingerprint != m.CorpusFingerprint {
		return fmt.Errorf("snapshot: semantic index corpus fingerprint mismatch")
	}
	if c.Metadata["source_artifact_sha256"] != audit.Artifacts[0].SHA256 || c.Metadata["query_scope"] != SemanticIndexScope {
		return fmt.Errorf("snapshot: semantic index audit identity/scope mismatch")
	}
	if len(c.Artifacts) != 1 || c.Artifacts[0].Path != SemanticIndexPath || c.Artifacts[0].Size < 1 || c.Artifacts[0].Size > SemanticIndexMaxBytes {
		return fmt.Errorf("snapshot: semantic index requires one bounded artifact at %q", SemanticIndexPath)
	}
	return nil
}
