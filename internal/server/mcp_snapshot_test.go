package server

import (
	"context"
	"path/filepath"
	"sort"
	"testing"

	"moedex/internal/contextwin"
	"moedex/internal/graph"
	"moedex/internal/mcp"
)

func snapshotFromToolResult(t *testing.T, result map[string]interface{}) mcp.SnapshotIdentity {
	t.Helper()
	meta, ok := result["_meta"].(map[string]interface{})
	if !ok {
		t.Fatalf("result metadata=%T", result["_meta"])
	}
	identity, ok := meta[mcp.SnapshotMetaKey].(mcp.SnapshotIdentity)
	if !ok {
		t.Fatalf("snapshot metadata=%T", meta[mcp.SnapshotMetaKey])
	}
	return identity.Normalize()
}

func TestGraphToolResultCarriesAcquiredSnapshotAndConsumedBlobs(t *testing.T) {
	fixture := newGraphFixture(t)
	tool := graphHandler(t, fixture.tools, "trace_calls")
	result, err := tool.Call(context.Background(), []byte(`{"symbol":"Root","hops":1}`))
	if err != nil {
		t.Fatal(err)
	}
	identity := snapshotFromToolResult(t, result)
	snapshot := fixture.tools.acquire()
	if snapshot == nil {
		t.Fatal("graph snapshot unavailable")
	}
	wantGeneration := snapshot.graph.Generation()
	wantFingerprint := snapshot.corpusFingerprint
	wantBuildID := snapshot.buildID
	snapshot.wg.Done()
	if !identity.Cacheable || identity.GraphGeneration != wantGeneration || identity.CorpusFingerprint != wantFingerprint || identity.GraphBuildID != wantBuildID {
		t.Fatalf("snapshot identity=%+v", identity)
	}
	if !sort.StringsAreSorted(identity.BlobSHAs) || len(identity.BlobSHAs) == 0 {
		t.Fatalf("blob_shas not deterministic: %v", identity.BlobSHAs)
	}
	structured := result["structuredContent"].(GraphQueryResult)
	want := map[string]bool{}
	for _, node := range structured.Nodes {
		want[node.BlobSHA] = true
		for _, location := range node.Locations {
			if location.BlobSHA != node.BlobSHA {
				t.Errorf("node location blob_sha=%q node=%q", location.BlobSHA, node.BlobSHA)
			}
		}
	}
	for _, edge := range structured.Edges {
		want[edge.Evidence.BlobSHA] = true
	}
	for sha := range want {
		if !contains(identity.BlobSHAs, sha) {
			t.Errorf("metadata omitted consumed blob %q: %v", sha, identity.BlobSHAs)
		}
	}
}

func TestGraphAnnotationsCarryOneAcquiredSnapshot(t *testing.T) {
	fixture := newGraphFixture(t)
	block := contextwin.ContextBlock{
		BlobSHA: "root-sha",
		Repo:    "fixture", RelPath: "root.go", AbsPath: filepath.Join(fixture.dir, "root.go"),
		StartLine: 1, EndLine: 2,
	}
	result, err := fixture.tools.NeighborsWithSnapshot(context.Background(), []contextwin.ContextBlock{block}, 1, graph.Pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Neighbors) != 1 || result.Snapshot.GraphGeneration == 0 || result.Snapshot.GraphBuildID == "" || result.Snapshot.CorpusFingerprint == "" {
		t.Fatalf("annotation snapshot=%+v neighbors=%d", result.Snapshot, len(result.Neighbors))
	}
	if !sort.StringsAreSorted(result.Snapshot.BlobSHAs) || !contains(result.Snapshot.BlobSHAs, block.BlobSHA) {
		t.Fatalf("annotation blob_shas=%v", result.Snapshot.BlobSHAs)
	}
}
