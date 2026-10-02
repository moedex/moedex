package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stageSemanticFixture(t *testing.T) (string, *Transaction, *Manifest) {
	t.Helper()
	indexDir := t.TempDir()
	tx, m := stageFixture(t, indexDir, "semantic-1", "shard fixture")
	m.Version, m.ID, m.Sequence, m.CreatedAt = FormatVersion, "semantic-1", 1, time.Unix(1, 0)
	path := filepath.Join(tx.Dir(), filepath.FromSlash(SemanticPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// This layer intentionally checks the producer's opaque bytes, not JSON.
	if err := os.WriteFile(path, []byte("validated producer bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifact, err := CaptureArtifact(tx.Dir(), SemanticPath)
	if err != nil {
		t.Fatal(err)
	}
	m.Components[SemanticComponent] = Component{
		Format: SemanticFormat, Version: SemanticVersion,
		InputFingerprint: m.CorpusFingerprint,
		Metadata:         map[string]string{"coverage": SemanticCoverage, "usage": SemanticUsage},
		Artifacts:        []Artifact{artifact},
	}
	return indexDir, tx, m
}

func TestSemanticComponentDescriptor(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Manifest, *Component)
	}{
		{"wrong fingerprint", func(m *Manifest, c *Component) { c.InputFingerprint = "rank-fingerprint" }},
		{"empty fingerprint", func(m *Manifest, c *Component) { m.CorpusFingerprint, c.InputFingerprint = "", "" }},
		{"wrong format", func(m *Manifest, c *Component) { c.Format = "compiler-query-index" }},
		{"wrong version", func(m *Manifest, c *Component) { c.Version++ }},
		{"missing coverage", func(m *Manifest, c *Component) { delete(c.Metadata, "coverage") }},
		{"query usage", func(m *Manifest, c *Component) { c.Metadata["usage"] = "query" }},
		{"no artifacts", func(m *Manifest, c *Component) { c.Artifacts = nil }},
		{"multiple artifacts", func(m *Manifest, c *Component) {
			a := c.Artifacts[0]
			a.Path = "semantic/second.json"
			c.Artifacts = append(c.Artifacts, a)
		}},
		{"wrong path", func(m *Manifest, c *Component) { c.Artifacts[0].Path = "semantic/other.json" }},
		{"empty", func(m *Manifest, c *Component) { c.Artifacts[0].Size = 0 }},
		{"oversize", func(m *Manifest, c *Component) { c.Artifacts[0].Size = SemanticMaxBytes + 1 }},
		{"invalid digest", func(m *Manifest, c *Component) { c.Artifacts[0].SHA256 = "not-a-digest" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, tx, m := stageSemanticFixture(t)
			c := m.Components[SemanticComponent]
			test.edit(m, &c)
			m.Components[SemanticComponent] = c
			if err := Validate(m); err == nil {
				t.Fatal("invalid semantic descriptor accepted")
			}
			if err := VerifySemanticComponent(tx.Dir(), m); err == nil {
				t.Fatal("invalid semantic descriptor verified")
			}
		})
	}
}

func TestResolveVerifiesOptionalSemanticComponent(t *testing.T) {
	for _, mutation := range []string{"none", "missing", "corrupt", "truncated", "grown"} {
		t.Run(mutation, func(t *testing.T) {
			indexDir, tx, m := stageSemanticFixture(t)
			if err := VerifySemanticComponent(tx.Dir(), m); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(m); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(SnapshotPath(indexDir, m.ID), filepath.FromSlash(SemanticPath))
			var err error
			switch mutation {
			case "missing":
				err = os.Remove(path)
			case "corrupt":
				err = os.WriteFile(path, []byte("Validated producer bytes"), 0o644)
			case "truncated":
				err = os.WriteFile(path, []byte("short"), 0o644)
			case "grown":
				err = os.WriteFile(path, []byte("validated producer bytes extra"), 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = Resolve(indexDir)
			if (err == nil) != (mutation == "none") {
				t.Fatalf("Resolve error = %v for %s", err, mutation)
			}
		})
	}
}

func TestSemanticComponentAbsentDoesNotReadFiles(t *testing.T) {
	if err := VerifySemanticComponent(filepath.Join(t.TempDir(), "absent-root"), &Manifest{}); err != nil {
		t.Fatal(err)
	}
	indexDir := t.TempDir()
	tx, m := stageFixture(t, indexDir, "ordinary", "shard fixture")
	if err := tx.Commit(m); err != nil {
		t.Fatal(err)
	}
	// Resolve must not become a whole-corpus checksum pass, even when semantic
	// validation is enabled. Existing serving loaders own their usual checks.
	if err := os.WriteFile(filepath.Join(SnapshotPath(indexDir, m.ID), "serve", "shard-0000.idx"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(indexDir); err != nil {
		t.Fatal(err)
	}
}

func TestSemanticComponentCannotUseAlias(t *testing.T) {
	for _, keepFormat := range []bool{false, true} {
		_, tx, m := stageSemanticFixture(t)
		c := m.Components[SemanticComponent]
		delete(m.Components, SemanticComponent)
		if keepFormat {
			c.Artifacts[0].Path = "other/artifact.json"
		} else {
			c.Format = "unrelated"
		}
		m.Components["alias"] = c
		if err := Validate(m); err == nil {
			t.Fatal("semantic component alias accepted")
		}
		if err := VerifySemanticComponent(tx.Dir(), m); err == nil {
			t.Fatal("semantic component alias verified")
		}
	}
}

func TestSemanticComponentRejectsSymlinks(t *testing.T) {
	for _, location := range []string{"file", "directory"} {
		t.Run(location, func(t *testing.T) {
			_, tx, m := stageSemanticFixture(t)
			path := filepath.Join(tx.Dir(), filepath.FromSlash(SemanticPath))
			if location == "directory" {
				path = filepath.Dir(path)
			}
			target := path + "-actual"
			if err := os.Rename(path, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			if err := VerifySemanticComponent(tx.Dir(), m); err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("expected symlink rejection, got %v", err)
			}
		})
	}
}
