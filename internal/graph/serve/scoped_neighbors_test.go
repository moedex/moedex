package graphserve

import (
	"context"
	"testing"

	"moedex/internal/contextwin"
	"moedex/internal/graph"
	"moedex/internal/sourcescope"
)

func TestScopedNeighborsCannotTraverseOutsideBridge(t *testing.T) {
	fixture := newGraphFixture(t)
	snap := fixture.tools.acquire()
	for key, meta := range snap.nodes {
		if meta.Symbol == "Middle" {
			for i := range meta.Locations {
				meta.Locations[i].Repo = "outside"
			}
			snap.nodes[key] = meta
		}
	}
	snap.wg.Done()
	blocks := []contextwin.ContextBlock{blockFor(fixture.dir, "root.go", 2)}
	unscoped, err := fixture.tools.NeighborsWithSnapshot(context.Background(), blocks, 3, graph.Pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(unscoped.Neighbors[0].Callees) != 2 {
		t.Fatalf("expected two-hop control, got %+v", unscoped.Neighbors)
	}
	scoped, err := fixture.tools.NeighborsScopedWithSnapshot(context.Background(), blocks, 3, graph.Pattern, sourcescope.Scope{Repo: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped.Neighbors[0].Callees) != 0 {
		t.Fatalf("escaped through excluded bridge: %+v", scoped.Neighbors)
	}
	// The same boundary applies to incoming traversal.
	scoped, err = fixture.tools.NeighborsScopedWithSnapshot(context.Background(), []contextwin.ContextBlock{blockFor(fixture.dir, "leaf.go", 2)}, 3, graph.Pattern, sourcescope.Scope{Repo: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped.Neighbors[0].Callers) != 0 {
		t.Fatalf("incoming traversal escaped: %+v", scoped.Neighbors)
	}
}

func TestScopedNeighborsSelectMatchingSharedLocation(t *testing.T) {
	fixture := newGraphFixture(t)
	snap := fixture.tools.acquire()
	for key, meta := range snap.nodes {
		if meta.Symbol == "Middle" {
			matching := meta.Locations[0]
			outside := matching
			outside.Repo = "outside"
			outside.Path = "wrong.py"
			meta.Locations = []GraphLocation{outside, matching}
			snap.nodes[key] = meta
		}
	}
	snap.wg.Done()
	scoped, err := fixture.tools.NeighborsScopedWithSnapshot(context.Background(), []contextwin.ContextBlock{blockFor(fixture.dir, "root.go", 2)}, 1, graph.Pattern, sourcescope.Scope{Repo: "fixture", Language: "go"})
	if err != nil {
		t.Fatal(err)
	}
	n := scoped.Neighbors[0].Callees
	if len(n) != 1 || n[0].Repo != "fixture" || n[0].RelPath != "middle.go" {
		t.Fatalf("wrong shared occurrence: %+v", n)
	}
}

func TestScopedAnchorsRejectBarePathFallbackFromOtherRepo(t *testing.T) {
	fixture := newGraphFixture(t)
	block := blockFor(fixture.dir, "root.go", 2)
	block.AbsPath = ""
	block.Repo = "other"
	result, err := fixture.tools.NeighborsScopedWithSnapshot(context.Background(), []contextwin.ContextBlock{block}, 2, graph.Pattern, sourcescope.Scope{Repo: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Neighbors[0].Anchors) != 0 || len(result.Neighbors[0].Callees) != 0 {
		t.Fatalf("borrowed another repository's anchor: %+v", result.Neighbors)
	}
}
