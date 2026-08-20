package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// blockFor builds the context block search_context would emit for one fixture
// file, covering the whole (two-to-seven line) file so the block certainly
// contains the definition the graph is keyed on.
func blockFor(dir, rel string, endLine int) contextwin.ContextBlock {
	return contextwin.ContextBlock{
		Repo:      "fixture",
		RelPath:   rel,
		AbsPath:   filepath.Join(dir, rel),
		StartLine: 1,
		EndLine:   endLine,
	}
}

func neighborSymbols(list []mcp.Neighbor) []string {
	out := make([]string, 0, len(list))
	for _, n := range list {
		out = append(out, n.Symbol)
	}
	return out
}

func TestDependencyBucketIncludesManifestDependsOnAndCandidateEvidence(t *testing.T) {
	for _, edgeType := range []diskgraph.EdgeType{diskgraph.EdgeDependsOn, diskgraph.EdgeCandidate} {
		if !isDependencyEdge(edgeType) {
			t.Errorf("%s is not classified as a dependency edge", edgeType)
		}
	}
}

func TestNeighborsAnnotateCallersAndCalleesOfASearchHit(t *testing.T) {
	fixture := newGraphFixture(t)
	blocks := []contextwin.ContextBlock{blockFor(fixture.dir, "root.go", 2)}

	got, err := fixture.tools.Neighbors(context.Background(), blocks, 1)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if len(got) != len(blocks) {
		t.Fatalf("Neighbors returned %d annotations for %d blocks", len(got), len(blocks))
	}
	if want := []string{"Root"}; !reflect.DeepEqual(got[0].Anchors, want) {
		t.Fatalf("anchors = %v, want %v", got[0].Anchors, want)
	}
	if want := []string{"Caller"}; !reflect.DeepEqual(neighborSymbols(got[0].Callers), want) {
		t.Errorf("callers = %v, want %v", neighborSymbols(got[0].Callers), want)
	}
	if want := []string{"Middle"}; !reflect.DeepEqual(neighborSymbols(got[0].Callees), want) {
		t.Errorf("callees = %v, want %v", neighborSymbols(got[0].Callees), want)
	}

	caller := got[0].Callers[0]
	if caller.Edge != "calls" || caller.Direction != mcp.DirectionIn || caller.Hops != 1 {
		t.Errorf("caller relationship = %+v, want an incoming one-hop calls edge", caller)
	}
	if caller.Anchor != "Root" || caller.RelPath != "caller.go" || caller.Repo != "fixture" || caller.Line != 2 {
		t.Errorf("caller provenance = %+v, want fixture/caller.go:2 anchored on Root", caller)
	}
	if caller.Confidence.Tier.String() != "Pattern" || caller.ID != "caller-sha:21" {
		t.Errorf("caller confidence/id = %+v, want the Pattern-tier node caller-sha:21", caller)
	}
	if callee := got[0].Callees[0]; callee.Direction != mcp.DirectionOut || callee.Edge != "calls" {
		t.Errorf("callee relationship = %+v, want an outgoing calls edge", callee)
	}
}

func TestCandidateDependencyRequiresExplicitCandidateFloor(t *testing.T) {
	fixture := newGraphFixture(t)
	blocks := []contextwin.ContextBlock{blockFor(fixture.dir, "root.go", 2)}
	defaults, err := fixture.tools.Neighbors(context.Background(), blocks, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := neighborSymbols(defaults[0].DependsOn); len(got) != 0 {
		t.Fatalf("default Pattern floor exposed Candidate dependency: %v", got)
	}
	explicit, err := fixture.tools.NeighborsWithConfidence(context.Background(), blocks, 1, graph.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := neighborSymbols(explicit[0].DependsOn), []string{"Orphan"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit Candidate dependencies = %v, want %v", got, want)
	}
}

func TestNeighborsAnnotateMessagingTopologyOfAnEvent(t *testing.T) {
	fixture := newGraphFixture(t)
	blocks := []contextwin.ContextBlock{blockFor(fixture.dir, "event.cs", 1)}

	got, err := fixture.tools.Neighbors(context.Background(), blocks, 1)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if want := []string{"OrderConsumer"}; !reflect.DeepEqual(neighborSymbols(got[0].Consumers), want) {
		t.Errorf("consumers = %v, want %v", neighborSymbols(got[0].Consumers), want)
	}
	if want := []string{"Send"}; !reflect.DeepEqual(neighborSymbols(got[0].Publishers), want) {
		t.Errorf("publishers = %v, want %v", neighborSymbols(got[0].Publishers), want)
	}
	if tier := got[0].Consumers[0].Confidence.Tier.String(); tier != "Verified" {
		t.Errorf("consumer confidence = %s, want Verified", tier)
	}
	if got[0].Callers != nil && len(got[0].Callers) != 0 {
		t.Errorf("event has no call edges, got callers %v", neighborSymbols(got[0].Callers))
	}

	// The publisher's own block reports the reverse view: it DEPENDS_ON the event
	// it emits, and has no publishers of its own.
	pub, err := fixture.tools.Neighbors(context.Background(),
		[]contextwin.ContextBlock{blockFor(fixture.dir, "publisher.cs", 7)}, 1)
	if err != nil {
		t.Fatalf("Neighbors(publisher): %v", err)
	}
	if want := []string{"OrderSubmitted"}; !reflect.DeepEqual(neighborSymbols(pub[0].DependsOn), want) {
		t.Errorf("publisher depends_on = %v, want %v", neighborSymbols(pub[0].DependsOn), want)
	}
	if dep := pub[0].DependsOn[0]; dep.Edge != "publishes" || dep.Direction != mcp.DirectionOut {
		t.Errorf("publisher dependency = %+v, want an outgoing publishes edge", dep)
	}
	if len(pub[0].Publishers) != 0 {
		t.Errorf("publisher should have no publishers of its own, got %v", neighborSymbols(pub[0].Publishers))
	}
}

func TestNeighborsForASymbolWithNoEdgesIsPresentAndEmpty(t *testing.T) {
	fixture := newGraphFixture(t)
	blocks := []contextwin.ContextBlock{blockFor(fixture.dir, "orphan.go", 2)}

	got, err := fixture.tools.Neighbors(context.Background(), blocks, 1)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Neighbors returned %d annotations, want 1", len(got))
	}
	if want := []string{"Orphan"}; !reflect.DeepEqual(got[0].Anchors, want) {
		t.Fatalf("anchors = %v, want %v — the block must still resolve to its symbol", got[0].Anchors, want)
	}
	if got[0].Total() != 0 {
		t.Fatalf("edgeless symbol reported %d neighbor(s): %+v", got[0].Total(), got[0])
	}
	// Empty must serialize as [] (a known-empty neighborhood), never as null.
	raw, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, bucket := range []string{"callers", "callees", "consumers", "publishers", "depends_on", "similar_to"} {
		if !strings.Contains(string(raw), `"`+bucket+`":[]`) {
			t.Errorf("empty %s did not serialize as []: %s", bucket, raw)
		}
	}
}

func TestNeighborBucketTotalsAreCapturedBeforeStorageCap(t *testing.T) {
	dir := t.TempDir()
	ix := index.New()
	targetContent := []byte("package fixture\nfunc Target() {}\n")
	ix.AddFile("fixture", "target.go", filepath.Join(dir, "target.go"), "target-sha", targetContent)
	target := diskgraph.Key{BlobSHA: "target-sha", SymbolOffset: uint64(strings.Index(string(targetContent), "Target"))}
	builder := diskgraph.NewBuilder()
	for i := 0; i < mcp.MaxNeighborsPerBucket+5; i++ {
		name := fmt.Sprintf("Caller%02d", i)
		content := []byte("package fixture\nfunc " + name + "() { Target() }\n")
		sha := fmt.Sprintf("caller-%02d-sha", i)
		rel := fmt.Sprintf("callers/caller-%02d.go", i)
		ix.AddFile("fixture", rel, filepath.Join(dir, rel), sha, content)
		source := diskgraph.Key{BlobSHA: sha, SymbolOffset: uint64(strings.Index(string(content), name))}
		if err := builder.AddEdge(source, diskgraph.Edge{Type: diskgraph.EdgeCalls,
			TargetBlob: target.BlobSHA, TargetOffset: target.SymbolOffset, Confidence: graph.Pattern,
			Evidence: graph.Evidence{BlobSHA: sha, ByteOffset: source.SymbolOffset, ByteLength: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}
	if err := builder.Save(GraphPath(dir)); err != nil {
		t.Fatal(err)
	}
	tools, err := OpenGraphTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer tools.Close()
	got, err := tools.Neighbors(context.Background(), []contextwin.ContextBlock{blockFor(dir, "target.go", 2)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0].Callers) != mcp.MaxNeighborsPerBucket || got[0].BucketTotals["callers"] != mcp.MaxNeighborsPerBucket+5 || !got[0].Truncated {
		t.Fatalf("capped callers = %d, totals=%v truncated=%v", len(got[0].Callers), got[0].BucketTotals, got[0].Truncated)
	}
	for _, bucket := range []string{"callers", "callees", "consumers", "publishers", "depends_on", "similar_to"} {
		if _, ok := got[0].BucketTotals[bucket]; !ok {
			t.Errorf("bucket_totals missing %q: %v", bucket, got[0].BucketTotals)
		}
	}
}

func TestNeighborsResolveABlockByItsRepoQualifiedPath(t *testing.T) {
	fixture := newGraphFixture(t)
	// A block whose absolute path does not match the one recorded at index time
	// (a shard dir built on another machine, a moved mirror) still anchors through
	// the repo-qualified spelling rather than silently reporting no neighborhood.
	blocks := []contextwin.ContextBlock{{
		Repo: "fixture", RelPath: "root.go", AbsPath: "/moved/elsewhere/root.go",
		StartLine: 1, EndLine: 2,
	}}

	got, err := fixture.tools.Neighbors(context.Background(), blocks, 1)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if want := []string{"Root"}; !reflect.DeepEqual(got[0].Anchors, want) {
		t.Fatalf("anchors = %v, want %v", got[0].Anchors, want)
	}
	if want := []string{"Middle"}; !reflect.DeepEqual(neighborSymbols(got[0].Callees), want) {
		t.Errorf("callees = %v, want %v", neighborSymbols(got[0].Callees), want)
	}
}

func TestNeighborsForABlockWithNoSymbolIsPresentAndAnchorless(t *testing.T) {
	fixture := newGraphFixture(t)
	// Line 1 of root.go is the package clause; the definition sits on line 2. A hit
	// that lands outside every symbol still gets an annotation object, just an
	// empty one — the absence is reported, not implied by a missing field.
	blocks := []contextwin.ContextBlock{blockFor(fixture.dir, "root.go", 1)}

	got, err := fixture.tools.Neighbors(context.Background(), blocks, 1)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Neighbors returned %d annotations, want 1", len(got))
	}
	if len(got[0].Anchors) != 0 {
		t.Errorf("anchors = %v, want none for a block containing no definition", got[0].Anchors)
	}
	if got[0].Total() != 0 {
		t.Errorf("anchorless block reported %d neighbor(s)", got[0].Total())
	}
	if raw, err := json.Marshal(got[0]); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(raw), `"anchors":[]`) {
		t.Errorf("anchorless block did not serialize anchors as []: %s", raw)
	}
}

func TestNeighborsDepthZeroReturnsNoAnnotation(t *testing.T) {
	fixture := newGraphFixture(t)
	blocks := []contextwin.ContextBlock{blockFor(fixture.dir, "root.go", 2)}

	got, err := fixture.tools.Neighbors(context.Background(), blocks, 0)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if got != nil {
		t.Fatalf("graph_depth=0 must annotate nothing, got %+v", got)
	}
}

func TestNeighborsDepthTwoReachesTransitiveCallersAndCallees(t *testing.T) {
	fixture := newGraphFixture(t)
	blocks := []contextwin.ContextBlock{blockFor(fixture.dir, "middle.go", 2)}

	one, err := fixture.tools.Neighbors(context.Background(), blocks, 1)
	if err != nil {
		t.Fatalf("Neighbors(1): %v", err)
	}
	if want := []string{"Root"}; !reflect.DeepEqual(neighborSymbols(one[0].Callers), want) {
		t.Fatalf("one-hop callers = %v, want %v", neighborSymbols(one[0].Callers), want)
	}

	two, err := fixture.tools.Neighbors(context.Background(), blocks, 2)
	if err != nil {
		t.Fatalf("Neighbors(2): %v", err)
	}
	if want := []string{"Root", "Caller"}; !reflect.DeepEqual(neighborSymbols(two[0].Callers), want) {
		t.Fatalf("two-hop callers = %v, want %v (callers of callers)", neighborSymbols(two[0].Callers), want)
	}
	if two[0].Callers[1].Hops != 2 {
		t.Errorf("transitive caller hops = %d, want 2", two[0].Callers[1].Hops)
	}
	// The traversal is directed per lane, so widening the radius upstream must not
	// drag Leaf (a callee of the anchor) into the callers lane.
	if want := []string{"Leaf"}; !reflect.DeepEqual(neighborSymbols(two[0].Callees), want) {
		t.Errorf("two-hop callees = %v, want %v", neighborSymbols(two[0].Callees), want)
	}
}

func TestNeighborsOnAClosedGraphLeavesSearchUnannotated(t *testing.T) {
	fixture := newGraphFixture(t)
	blocks := []contextwin.ContextBlock{blockFor(fixture.dir, "root.go", 2)}
	if err := fixture.tools.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := fixture.tools.Neighbors(context.Background(), blocks, 1)
	if err != nil {
		t.Fatalf("a closed graph must not fail a search: %v", err)
	}
	if got != nil {
		t.Fatalf("closed graph annotated %+v, want no annotation", got)
	}
}

// TestSearchContextResponseCarriesGraphNeighbors is the phase-14 end-to-end
// proof: a real ranked search over the fixture corpus, served through the MCP
// tools/call surface, comes back with the hit's graph edges attached — and comes
// back without them when the caller opts out with graph_depth=0.
func TestSearchContextResponseCarriesGraphNeighbors(t *testing.T) {
	fixture := newGraphFixture(t)
	rc, err := OpenRank(context.Background(), fixture.dir, RankConfig{})
	if err != nil {
		t.Fatalf("OpenRank: %v", err)
	}
	defer rc.Close()

	handler := mcp.NewServer(rc,
		mcp.WithTools(fixture.tools.Tools()...),
		mcp.WithGraphAnnotator(fixture.tools),
	).HTTPHandler()
	call := func(args string) string {
		req := httptest.NewRequest(http.MethodPost, "/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_context","arguments":`+args+`}}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("search_context status = %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	annotated := call(`{"query":"Root","format":"structured"}`)
	for _, want := range []string{
		`"neighbors":`,
		`"anchors":["Root"]`,
		`"symbol":"Caller"`,
		`"symbol":"Middle"`,
		`"edge":"calls"`,
	} {
		if !strings.Contains(annotated, want) {
			t.Errorf("search_context response missing %s: %s", want, annotated)
		}
	}
	if strings.Contains(annotated, `"symbol":"Orphan"`) {
		t.Errorf("default Pattern floor exposed Candidate neighbor: %s", annotated)
	}
	candidate := call(`{"query":"Root","format":"structured","graph_depth":3,"min_confidence":"Candidate"}`)
	if !strings.Contains(candidate, `"symbol":"Orphan"`) {
		t.Errorf("explicit Candidate floor did not expose Candidate neighbor: %s", candidate)
	}

	off := call(`{"query":"Root","format":"structured","graph_depth":0}`)
	if strings.Contains(off, `"neighbors":`) {
		t.Errorf("graph_depth=0 still annotated the response: %s", off)
	}

	// The default text format carries the same fusion, so an agent that never asks
	// for structured output still sees the neighborhood beside the code.
	text := call(`{"query":"Root"}`)
	if !strings.Contains(text, `[graph]`) || !strings.Contains(text, "callers: Caller") {
		t.Errorf("text search_context response lacks the graph line: %s", text)
	}
}
