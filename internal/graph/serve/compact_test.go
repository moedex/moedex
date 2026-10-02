package graphserve

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"moedex/internal/contextwin"
	"moedex/internal/diskstore"
	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
	"moedex/internal/mcp"
)

func compactWalkFixture(t *testing.T, factored bool, targets int) (*graphSnapshot, diskgraph.Key, []diskgraph.Key) {
	t.Helper()
	dir := t.TempDir()
	content := "package fixture\nfunc Root() {}\n"
	ix := index.New()
	ix.AddFile("fixture", "root.go", "/fixture/root.go", "source-sha", []byte(content))
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}
	source := diskgraph.Key{BlobSHA: "source-sha", SymbolOffset: uint64(strings.Index(content, "Root"))}
	keys := make([]diskgraph.Key, targets)
	for i := range keys {
		keys[i] = diskgraph.Key{BlobSHA: fmt.Sprintf("target-%06d", i), SymbolOffset: 0}
	}
	prototype := diskgraph.Edge{Type: diskgraph.EdgeCalls, Confidence: graph.Pattern, Name: "Target", Evidence: graph.Evidence{BlobSHA: source.BlobSHA, ByteOffset: source.SymbolOffset, ByteLength: 4}}
	builder := diskgraph.NewBuilder()
	if factored {
		id, err := builder.AddTargetSet(keys)
		if err != nil {
			t.Fatal(err)
		}
		if err := builder.AddFactoredSource(source, prototype, id, -1); err != nil {
			t.Fatal(err)
		}
	} else {
		for _, key := range keys {
			edge := prototype
			edge.TargetBlob = key.BlobSHA
			edge.TargetOffset = key.SymbolOffset
			if err := builder.AddEdge(source, edge); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := builder.Save(GraphPath(dir)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := openGraphSnapshotWithClusters(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { snapshot.graph.Close(); snapshot.symbols.Close() })
	return snapshot, source, keys
}

func TestCompactQueryReverseCatalogAndSchemaParity(t *testing.T) {
	explicit, _, _ := compactWalkFixture(t, false, 4)
	compact, _, targets := compactWalkFixture(t, true, 4)
	a, err := explicit.traceCalls(context.Background(), "Root", 2, graph.Pattern)
	if err != nil {
		t.Fatal(err)
	}
	b, err := compact.traceCalls(context.Background(), "Root", 2, graph.Pattern)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("query differs\nexplicit %+v\ncompact %+v", a, b)
	}
	if !b.TotalIsExact || b.Truncated {
		t.Fatalf("small query marked incomplete %+v", b)
	}
	for _, snapshot := range []*graphSnapshot{explicit, compact} {
		if len(snapshot.nodes) != 5 {
			t.Fatalf("target-only catalog count %d", len(snapshot.nodes))
		}
		snapshot.ensureSchema()
		if snapshot.schemaInfo.TotalEdges != 4 || snapshot.schemaInfo.EdgeTypes["calls"] != 4 {
			t.Fatalf("schema %+v", snapshot.schemaInfo)
		}
		incoming, err := snapshot.incoming(context.Background(), map[diskgraph.Key]bool{targets[2]: true}, func(diskgraph.EdgeType) bool { return true }, graph.Pattern, newTraversalBudget())
		if err != nil || len(incoming) != 1 || incoming[0].Target != targets[2] {
			t.Fatalf("selective incoming %+v %v", incoming, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := compact.traceCalls(ctx, "Root", 2, graph.Pattern); err != context.Canceled {
		t.Fatalf("cancel error %v", err)
	}
}

func TestCompactQueryExpansionLimitIsTruthful(t *testing.T) {
	snapshot, _, _ := compactWalkFixture(t, true, maxGraphTraversalEdges+1)
	result, err := snapshot.traceCalls(context.Background(), "Root", 1, graph.Pattern)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.TotalIsExact || result.ExpansionLimit != maxGraphTraversalEdges || len(result.Edges) != maxGraphTraversalEdges {
		t.Fatalf("budget metadata truncated=%t exact=%t limit=%d edges=%d", result.Truncated, result.TotalIsExact, result.ExpansionLimit, len(result.Edges))
	}

	neighbors, err := snapshot.neighbors(context.Background(), []contextwin.ContextBlock{{Repo: "fixture", RelPath: "root.go", AbsPath: "/fixture/root.go", StartLine: 1, EndLine: 3}}, 1, graph.Pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(neighbors) != 1 || !neighbors[0].Truncated || neighbors[0].TotalIsExact || neighbors[0].ExpansionLimit != maxGraphTraversalEdges || neighbors[0].BucketTotals["callees"] != maxGraphTraversalEdges || len(neighbors[0].Callees) != mcp.MaxNeighborsPerBucket {
		t.Fatalf("neighbor lower-bound metadata %+v", neighbors)
	}
	again, err := snapshot.traceCalls(context.Background(), "Root", 1, graph.Pattern)
	if err != nil || !reflect.DeepEqual(result, again) {
		t.Fatal("limited traversal not deterministic", err)
	}
}
