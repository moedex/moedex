package indexcmd

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"moedex/internal/semantic"
	"moedex/internal/semanticindex"
	indexsnapshot "moedex/internal/snapshot"
)

// Attachment runs the same recorded-input revalidation as production. Keep this
// separate from direct Build/Open query tests, which cannot catch a stale worker
// version allowlist at the snapshot publication boundary.
func TestSnapshotBuildAcceptsWorkerV2(t *testing.T) {
	corpus, repo, indexDir := semanticSnapshotFixture(t)
	original := semanticSnapshotArtifact(t, repo, "repo", "", "Main.cs")
	a, err := semantic.Read(original, semantic.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	c := &a.Contexts[0]
	var capture map[string]any
	if err := json.Unmarshal([]byte(c.Capture), &capture); err != nil {
		t.Fatal(err)
	}
	capture["extractor_version"] = "2"
	raw, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	c.Capture = string(raw)
	c.InputFingerprint = semanticSnapshotSHA(raw)
	c.ExtractorVersion = "2"
	c.ID = c.ComputeID()
	occurrenceIDs := map[string]string{}
	for i := range a.Occurrences {
		o := &a.Occurrences[i]
		old := o.ID
		o.ContextID = c.ID
		o.ID = o.ComputeID()
		occurrenceIDs[old] = o.ID
	}
	for i := range a.Bindings {
		b := &a.Bindings[i]
		b.OccurrenceID = occurrenceIDs[b.OccurrenceID]
		b.ExtractorVersion = "2"
		b.ID = b.ComputeID()
	}
	updated := filepath.Join(t.TempDir(), "v2.semantic")
	if err := semantic.Write(updated, a); err != nil {
		t.Fatal(err)
	}
	if err := runSnapshotBuild([]string{"--corpus", corpus, "--index-dir", indexDir, "--id", "worker-v2", "--graph=false", "--semantic-artifact", updated}); err != nil {
		t.Fatal(err)
	}
	manifest, root, err := indexsnapshot.Current(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	audit, ok := manifest.Components["semantic"]
	if !ok || len(audit.Artifacts) != 1 {
		t.Fatal("missing attached compiler artifact")
	}
	x, err := semanticindex.Open(filepath.Join(root, indexsnapshot.SemanticIndexPath), semanticindex.Expected{ArtifactSHA256: audit.Artifacts[0].SHA256, CorpusFingerprint: manifest.CorpusFingerprint}, semanticindex.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
}
