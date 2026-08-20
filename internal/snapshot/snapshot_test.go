package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func stageFixture(t *testing.T, indexDir, id, content string) (*Transaction, *Manifest) {
	t.Helper()
	tx, err := Begin(indexDir, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Abort() })
	if err := os.MkdirAll(filepath.Join(tx.Dir(), "serve"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tx.Dir(), "serve", "shard-0000.idx"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	artifact, err := CaptureArtifact(tx.Dir(), "serve/shard-0000.idx")
	if err != nil {
		t.Fatal(err)
	}
	m := &Manifest{
		ServingRoot:       "serve",
		CorpusFingerprint: "corpus-fixture",
		Components: map[string]Component{
			"shards": {Format: "moedex", Version: 5, Artifacts: []Artifact{artifact}},
		},
	}
	return tx, m
}

func TestCommitPublishesCompleteSnapshotAndCurrent(t *testing.T) {
	indexDir := t.TempDir()
	tx, manifest := stageFixture(t, indexDir, "snap-1", "first")
	if err := tx.Commit(manifest); err != nil {
		t.Fatal(err)
	}
	got, root, err := Current(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "snap-1" || got.Sequence != 1 || root != SnapshotPath(indexDir, "snap-1") {
		t.Fatalf("current = id=%q sequence=%d root=%q", got.ID, got.Sequence, root)
	}
	resolved, err := Resolve(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resolved.ShardDir(), filepath.Join(root, "serve"); got != want {
		t.Fatalf("ShardDir = %q, want %q", got, want)
	}
	if LockOwner(indexDir) != "" {
		t.Fatal("writer lock remained after commit")
	}
}

func TestRollbackOnlyMovesCurrentPointer(t *testing.T) {
	indexDir := t.TempDir()
	first, firstManifest := stageFixture(t, indexDir, "snap-1", "first")
	if err := first.Commit(firstManifest); err != nil {
		t.Fatal(err)
	}
	second, secondManifest := stageFixture(t, indexDir, "snap-2", "second")
	if err := second.Commit(secondManifest); err != nil {
		t.Fatal(err)
	}
	if secondManifest.ParentID != "snap-1" || secondManifest.Sequence != 2 {
		t.Fatalf("second lineage = parent %q sequence %d", secondManifest.ParentID, secondManifest.Sequence)
	}
	if err := SetCurrent(indexDir, "snap-1"); err != nil {
		t.Fatal(err)
	}
	got, _, err := Current(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "snap-1" {
		t.Fatalf("current id = %q, want snap-1", got.ID)
	}
	if _, err := os.Stat(SnapshotPath(indexDir, "snap-2")); err != nil {
		t.Fatalf("rollback altered newer immutable snapshot: %v", err)
	}
	list, err := List(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	if ids := []string{list[0].ID, list[1].ID}; !reflect.DeepEqual(ids, []string{"snap-2", "snap-1"}) {
		t.Fatalf("List ids = %v", ids)
	}
}

func TestCommitRejectsTamperedArtifactWithoutPublishing(t *testing.T) {
	indexDir := t.TempDir()
	tx, manifest := stageFixture(t, indexDir, "snap-bad", "before")
	if err := os.WriteFile(filepath.Join(tx.Dir(), "serve", "shard-0000.idx"), []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(manifest); err == nil {
		t.Fatal("Commit succeeded with a hash-mismatched artifact")
	}
	if _, err := os.Stat(SnapshotPath(indexDir, "snap-bad")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bad snapshot was published: %v", err)
	}
	if _, _, err := Current(indexDir); !errors.Is(err, ErrNoCurrent) {
		t.Fatalf("Current error = %v, want ErrNoCurrent", err)
	}
}

func TestCommitRejectsUnmanifestedArtifact(t *testing.T) {
	indexDir := t.TempDir()
	tx, manifest := stageFixture(t, indexDir, "snap-extra", "content")
	if err := os.WriteFile(filepath.Join(tx.Dir(), "serve", "unexpected.sidecar"), []byte("not declared"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(manifest); err == nil {
		t.Fatal("Commit succeeded with an unmanifested artifact")
	}
}

func TestSingleWriterAndLegacyResolution(t *testing.T) {
	indexDir := t.TempDir()
	first, err := Begin(indexDir, "snap-1")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Abort()
	if _, err := Begin(indexDir, "snap-2"); !errors.Is(err, ErrBuildLocked) {
		t.Fatalf("second Begin error = %v, want ErrBuildLocked", err)
	}
	if err := first.Abort(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(indexDir, "shard-0000.idx"), []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := Resolve(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Legacy || resolved.ShardDir() != indexDir {
		t.Fatalf("legacy resolve = %+v", resolved)
	}
}

func TestStageLegacyCopiesCompleteServingTree(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "legacy")
	indexDir := filepath.Join(root, "index")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"shard-0000.idx":          "shard",
		"manifest.json":           "manifest",
		"corpus-graph.graph":      "graph",
		"corpus-embeddings.store": "dense",
	} {
		if err := os.WriteFile(filepath.Join(legacy, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := Begin(indexDir, "migrated")
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Abort()
	manifest, err := StageLegacy(tx, legacy, Producer{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(manifest); err != nil {
		t.Fatal(err)
	}
	resolved, err := Resolve(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Legacy || resolved.Manifest == nil {
		t.Fatalf("Resolve = %+v, want manifest snapshot", resolved)
	}
	if len(resolved.Manifest.CorpusFingerprint) != 64 {
		t.Fatalf("corpus fingerprint = %q, want SHA-256", resolved.Manifest.CorpusFingerprint)
	}
	got, err := os.ReadFile(filepath.Join(resolved.ShardDir(), "shard-0000.idx"))
	if err != nil || string(got) != "shard" {
		t.Fatalf("migrated shard = %q, %v", got, err)
	}
	if want := []string{"dense", "graph", "search"}; !reflect.DeepEqual(resolved.Manifest.Capabilities, want) {
		t.Fatalf("capabilities = %v, want %v", resolved.Manifest.Capabilities, want)
	}
	if err := os.WriteFile(filepath.Join(legacy, "shard-0000.idx"), []byte("mutated"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(filepath.Join(resolved.ShardDir(), "shard-0000.idx"))
	if err != nil || string(got) != "shard" {
		t.Fatalf("snapshot changed with legacy source = %q, %v", got, err)
	}
}

func TestStageLegacyRejectsUnrecognizedFiles(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "legacy")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "shard-0000.idx"), []byte("shard"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "auth-token"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	tx, err := Begin(filepath.Join(root, "index"), "safe")
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Abort()
	if _, err := StageLegacy(tx, legacy, Producer{}); err == nil {
		t.Fatal("StageLegacy copied an unrecognized secret-bearing file")
	}
}
