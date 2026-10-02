package graphbuild

import (
	"reflect"
	"testing"

	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
)

func TestFactoredPatternGroupsUnionExcludedThenComplete(t *testing.T) {
	targets := &factoredTargets{keys: []diskgraph.Key{{BlobSHA: "a", SymbolOffset: 1}, {BlobSHA: "b", SymbolOffset: 2}}}
	prototype := diskgraph.Edge{Type: diskgraph.EdgeCalls, Confidence: graph.Pattern, Name: "Target", Evidence: graph.Evidence{BlobSHA: "source", ByteOffset: 7, ByteLength: 6}}
	first := factoredSource{source: diskgraph.Key{BlobSHA: "source", SymbolOffset: 1}, prototype: prototype, targets: targets, exclude: 0, rawEdges: 1, moveTarget: -1, moveAfter: -1}
	second := first
	second.exclude = -1
	second.rawEdges = 2
	groups := factoredPatternCallGroups([]factoredNameResult{{groups: []factoredSource{first, second}}})
	if len(groups) != 1 || !reflect.DeepEqual(groups[0].targets, targets.keys) || groups[0].numEdges() != 3 {
		t.Fatalf("lost later surviving target: %#v", groups)
	}
	if len(groups[0].edges) != 0 {
		t.Fatal("factored LSP grouping expanded edges")
	}
}

func TestFactoredPatternGroupsShareTargetRosters(t *testing.T) {
	targets := &factoredTargets{keys: []diskgraph.Key{{BlobSHA: "b", SymbolOffset: 2}, {BlobSHA: "a", SymbolOffset: 1}}}
	first := factoredSource{source: diskgraph.Key{BlobSHA: "source", SymbolOffset: 1}, prototype: diskgraph.Edge{Type: diskgraph.EdgeCalls, Confidence: graph.Pattern, Name: "Target", Evidence: graph.Evidence{BlobSHA: "source", ByteOffset: 7, ByteLength: 6}}, targets: targets, exclude: -1, rawEdges: 2, moveTarget: -1, moveAfter: -1}
	second := first
	second.prototype.Evidence.ByteOffset = 20
	groups := factoredPatternCallGroups([]factoredNameResult{{groups: []factoredSource{first, second}}})
	if len(groups) != 2 || len(groups[0].targets) != 2 || &groups[0].targets[0] != &groups[1].targets[0] {
		t.Fatal("identical target rosters were copied per call site")
	}
}

func TestFactoredRefreshCarriesMovedTargetOrder(t *testing.T) {
	duplicate := "package demo\nfunc Target() {}\n"
	files := []graphFile{{"repo", "a.go", duplicate}, {"repo", "b.go", "package demo\n// other\nfunc Target() {}\n"}, {"repo", "copy.go", duplicate}, {"repo", "other.go", "package demo\nfunc Unrelated() {}\n"}}
	dir := t.TempDir()
	writeGraphShards(t, dir, files, 1)
	path, _, err := BuildGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	before := readGraph(t, path)
	files[3].content += "// changed unrelated\n"
	writeGraphShards(t, dir, files, 1)
	path, stats, err := RefreshGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stats.EdgesCarried == 0 {
		t.Fatalf("fixture did not exercise compact carry: %+v", stats)
	}
	refreshed := readGraph(t, path)
	clean := copyShards(t, dir)
	cleanPath, _, err := BuildGraph(clean)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withoutGenerations(refreshed), withoutGenerations(readGraph(t, cleanPath))) {
		t.Fatal("compact carry changed surviving target order")
	}
	if !reflect.DeepEqual(withoutGenerations(before), withoutGenerations(refreshed)) {
		t.Fatal("unrelated modification changed Target adjacency")
	}
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	moved := false
	g.EachFactoredSource(func(source diskgraph.FactoredSource) bool { moved = moved || source.MoveTarget >= 0; return true })
	if !moved {
		t.Fatal("carried graph lost compact target-order exception")
	}
}
