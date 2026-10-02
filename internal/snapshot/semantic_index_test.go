package snapshot

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"moedex/internal/semanticindex"
)

func TestSemanticIndexManifestVersionCompatibility(t *testing.T) {
	if SemanticIndexVersion != semanticindex.Version {
		t.Fatal("snapshot and writer semantic index versions differ")
	}
	for _, version := range []int{1, 2, 3, 4, 5} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			_, tx, m := stageSemanticIndexFixture(t)
			c := m.Components[SemanticIndexComponent]
			c.Version = version
			m.Components[SemanticIndexComponent] = c
			if err := Validate(m); err != nil {
				t.Fatal("supported index manifest rejected", err)
			}
			if err := VerifySemanticComponent(tx.Dir(), m); err != nil {
				t.Fatal("supported index attachment rejected", err)
			}
		})
	}
}

func stageSemanticIndexFixture(t *testing.T) (string, *Transaction, *Manifest) {
	t.Helper()
	dir, tx, m := stageSemanticFixture(t)
	if err := os.WriteFile(filepath.Join(tx.Dir(), SemanticIndexPath), []byte("derived compact index"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := CaptureArtifact(tx.Dir(), SemanticIndexPath)
	if err != nil {
		t.Fatal(err)
	}
	m.Components[SemanticIndexComponent] = Component{
		Format: SemanticIndexFormat, Version: SemanticIndexVersion,
		InputFingerprint: m.CorpusFingerprint,
		Metadata:         map[string]string{"source_artifact_sha256": m.Components[SemanticComponent].Artifacts[0].SHA256, "query_scope": SemanticIndexScope},
		Artifacts:        []Artifact{a},
	}
	return dir, tx, m
}

func TestSemanticIndexRequiresMatchingAuditAndCorpus(t *testing.T) {
	for _, name := range []string{"missing_audit", "wrong_audit", "wrong_corpus", "wrong_version", "wrong_scope", "wrong_path", "oversized", "aliased"} {
		t.Run(name, func(t *testing.T) {
			_, tx, m := stageSemanticIndexFixture(t)
			c := m.Components[SemanticIndexComponent]
			switch name {
			case "missing_audit":
				delete(m.Components, SemanticComponent)
			case "wrong_audit":
				c.Metadata["source_artifact_sha256"] = "other-capture"
			case "wrong_corpus":
				c.InputFingerprint = "other-corpus"
			case "wrong_version":
				c.Version++
			case "wrong_scope":
				c.Metadata["query_scope"] = "live-current-compilation"
			case "wrong_path":
				c.Artifacts[0].Path = "serve/index.msi"
			case "oversized":
				c.Artifacts[0].Size = SemanticIndexMaxBytes + 1
			}
			m.Components[SemanticIndexComponent] = c
			if name == "aliased" {
				delete(m.Components, SemanticIndexComponent)
				m.Components["unvalidated_alias"] = c
			}
			if err := Validate(m); err == nil {
				t.Fatal("accepted invalid compact index component")
			}
			if err := VerifySemanticComponent(tx.Dir(), m); err == nil {
				t.Fatal("verified invalid compact index component")
			}
		})
	}
}

func TestResolveVerifiesCompactSemanticIndex(t *testing.T) {
	for _, mutation := range []string{"none", "missing", "corrupt"} {
		t.Run(mutation, func(t *testing.T) {
			dir, tx, m := stageSemanticIndexFixture(t)
			if err := tx.Commit(m); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(SnapshotPath(dir, m.ID), SemanticIndexPath)
			switch mutation {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(path, []byte("Derived compact index"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := Resolve(dir)
			if (err == nil) != (mutation == "none") {
				t.Fatalf("resolve %s: %v", mutation, err)
			}
		})
	}
}
