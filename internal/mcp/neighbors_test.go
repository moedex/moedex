package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"moedex/internal/contextwin"
	"moedex/internal/graph"
)

// stubAnnotator records the depth it was asked for and returns one canned
// neighborhood per block (or the configured failure mode).
type stubAnnotator struct {
	calls     int
	gotDepth  int
	gotBlocks int
	gotMin    graph.ConfidenceTier
	ret       []BlockNeighbors
	retNil    bool
	err       error
}

func (s *stubAnnotator) NeighborsWithConfidence(ctx context.Context, blocks []contextwin.ContextBlock, depth int, minConfidence graph.ConfidenceTier) ([]BlockNeighbors, error) {
	s.gotMin = minConfidence
	return s.Neighbors(ctx, blocks, depth)
}

func (s *stubAnnotator) Neighbors(_ context.Context, blocks []contextwin.ContextBlock, depth int) ([]BlockNeighbors, error) {
	s.calls++
	s.gotDepth = depth
	s.gotBlocks = len(blocks)
	if s.err != nil {
		return nil, s.err
	}
	if s.retNil {
		return nil, nil
	}
	if s.ret != nil {
		return s.ret, nil
	}
	out := make([]BlockNeighbors, len(blocks))
	for i := range blocks {
		out[i] = NewBlockNeighbors()
		out[i].Anchors = []string{"Root"}
		out[i].Callers = []Neighbor{{
			ID: "caller-sha:21", Symbol: "Caller", Kind: "Func",
			Repo: "fixture", RelPath: "caller.go", Line: 2,
			Edge: "calls", Direction: DirectionIn, Hops: 1, Anchor: "Root",
			Confidence: graph.ConfidenceOf(graph.Pattern),
		}}
	}
	return out, nil
}

// oneBlockSearcher answers every search with a single block.
func oneBlockSearcher() *fakeSearcher {
	return &fakeSearcher{win: contextwin.ContextWindow{
		Blocks: []contextwin.ContextBlock{{
			Blob: 7, Repo: "fixture", RelPath: "root.go", AbsPath: "/c/fixture/root.go",
			StartLine: 1, EndLine: 2, Text: "func Root() { Middle() }\n", Score: 0.5,
		}},
		TokenEstimate: 7,
	}}
}

func callSearch(t *testing.T, s *Server, args string) string {
	t.Helper()
	rec := postRPC(t, s.HTTPHandler(), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_context","arguments":`+args+`}}`)
	return rec.Body.String()
}

func TestSearchContextAnnotatesBlocksWithGraphNeighborsByDefault(t *testing.T) {
	ann := &stubAnnotator{}
	s := NewServer(oneBlockSearcher(), WithGraphAnnotator(ann))

	body := callSearch(t, s, `{"query":"Root","format":"structured"}`)
	if ann.calls != 1 || ann.gotDepth != DefaultGraphDepth || ann.gotBlocks != 1 {
		t.Fatalf("annotator calls=%d depth=%d blocks=%d, want 1/%d/1", ann.calls, ann.gotDepth, ann.gotBlocks, DefaultGraphDepth)
	}
	if ann.gotMin != graph.Pattern {
		t.Errorf("default min confidence = %s, want Pattern", ann.gotMin)
	}
	for _, want := range []string{
		`"neighbors":`,
		`"anchors":["Root"]`,
		`"symbol":"Caller"`,
		`"edge":"calls"`,
		`"direction":"in"`,
		`"hops":1`,
		`"confidence":{"tier":"Pattern","score":0.6}`,
		`"callees":[]`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("structured response missing %s: %s", want, body)
		}
	}
}

func TestSearchContextGraphDepthZeroReturnsNoNeighbors(t *testing.T) {
	ann := &stubAnnotator{}
	s := NewServer(oneBlockSearcher(), WithGraphAnnotator(ann))

	body := callSearch(t, s, `{"query":"Root","format":"structured","graph_depth":0}`)
	if ann.calls != 0 {
		t.Errorf("graph_depth=0 must not consult the graph, got %d call(s)", ann.calls)
	}
	if strings.Contains(body, `"neighbors":`) {
		t.Errorf("graph_depth=0 response carries neighbors: %s", body)
	}
	if !strings.Contains(body, `"rel_path":"root.go"`) {
		t.Errorf("graph_depth=0 dropped the search result itself: %s", body)
	}

	if text := callSearch(t, s, `{"query":"Root","graph_depth":0}`); strings.Contains(text, "[graph]") {
		t.Errorf("graph_depth=0 text response carries a graph line: %s", text)
	}
}

func TestSearchContextGraphDepthIsForwardedAndBounded(t *testing.T) {
	ann := &stubAnnotator{}
	s := NewServer(oneBlockSearcher(), WithGraphAnnotator(ann))

	callSearch(t, s, `{"query":"Root","graph_depth":3}`)
	if ann.gotDepth != 3 {
		t.Errorf("graph_depth=3 forwarded as %d", ann.gotDepth)
	}
	for _, bad := range []string{`{"query":"Root","graph_depth":-1}`, `{"query":"Root","graph_depth":11}`} {
		body := callSearch(t, s, bad)
		if !strings.Contains(body, `"isError":true`) || !strings.Contains(body, "graph_depth must be between") {
			t.Errorf("%s was accepted: %s", bad, body)
		}
	}
}

func TestSearchContextConfidenceFloorIsForwardedAndValidated(t *testing.T) {
	ann := &stubAnnotator{}
	s := NewServer(oneBlockSearcher(), WithGraphAnnotator(ann))
	callSearch(t, s, `{"query":"Root","min_confidence":"Candidate"}`)
	if ann.gotMin != graph.Candidate {
		t.Errorf("min_confidence forwarded as %s, want Candidate", ann.gotMin)
	}
	body := callSearch(t, s, `{"query":"Root","min_confidence":"candidate"}`)
	if !strings.Contains(body, `"isError":true`) || !strings.Contains(body, "min_confidence must be") {
		t.Fatalf("invalid min_confidence accepted: %s", body)
	}
}

func TestSearchContextWithoutAnnotatorIsUnchanged(t *testing.T) {
	s := NewServer(oneBlockSearcher())

	body := callSearch(t, s, `{"query":"Root","format":"structured","graph_depth":2}`)
	if strings.Contains(body, `"neighbors":`) {
		t.Errorf("no annotator wired but response carries neighbors: %s", body)
	}
	if !strings.Contains(body, `"rel_path":"root.go"`) {
		t.Errorf("search result lost: %s", body)
	}
	if text := callSearch(t, s, `{"query":"Root"}`); strings.Contains(text, "[graph]") {
		t.Errorf("un-annotated text response carries a graph line: %s", text)
	}
}

func TestSearchContextNilAnnotationLeavesResponseUnannotated(t *testing.T) {
	ann := &stubAnnotator{retNil: true}
	s := NewServer(oneBlockSearcher(), WithGraphAnnotator(ann))

	body := callSearch(t, s, `{"query":"Root","format":"structured"}`)
	if strings.Contains(body, `"neighbors":`) {
		t.Errorf("nil annotation still produced a neighbors field: %s", body)
	}
	if !strings.Contains(body, `"rel_path":"root.go"`) {
		t.Errorf("nil annotation dropped the search result: %s", body)
	}
}

func TestSearchContextRejectsMisalignedAnnotation(t *testing.T) {
	// Two neighbor sets for one block would silently attribute one block's
	// neighborhood to another; that must surface, not be papered over.
	ann := &stubAnnotator{ret: []BlockNeighbors{NewBlockNeighbors(), NewBlockNeighbors()}}
	s := NewServer(oneBlockSearcher(), WithGraphAnnotator(ann))

	body := callSearch(t, s, `{"query":"Root","format":"structured"}`)
	if !strings.Contains(body, `"error"`) || !strings.Contains(body, "2 neighbor set(s) for 1 block(s)") {
		t.Fatalf("misaligned annotation accepted: %s", body)
	}
}

func TestSearchContextSurfacesAnnotatorFailure(t *testing.T) {
	ann := &stubAnnotator{err: errors.New("sidecar unreadable")}
	s := NewServer(oneBlockSearcher(), WithGraphAnnotator(ann))

	body := callSearch(t, s, `{"query":"Root"}`)
	if !strings.Contains(body, "graph annotation: sidecar unreadable") {
		t.Fatalf("annotator error not surfaced: %s", body)
	}
}

func TestSearchContextTextFormatRendersNeighborLine(t *testing.T) {
	s := NewServer(oneBlockSearcher(), WithGraphAnnotator(&stubAnnotator{}))

	body := callSearch(t, s, `{"query":"Root"}`)
	if !strings.Contains(body, `[graph] callers: Caller [Pattern] (fixture/caller.go:2)`) {
		t.Fatalf("text rendering missing the graph line: %s", body)
	}
	// The line sits between the block header and the code, so the code is never
	// interrupted by annotation.
	if !strings.Contains(body, `--- root.go:1-2 (score 0.5000) ---\n[graph] `) {
		t.Errorf("graph line is not directly under the block header: %s", body)
	}
}

func TestGraphDepthIsAdvertisedInTheToolDescriptor(t *testing.T) {
	s := NewServer(oneBlockSearcher(), WithGraphAnnotator(&stubAnnotator{}))
	body := postRPC(t, s.HTTPHandler(), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`).Body.String()
	for _, want := range []string{`"graph_depth"`, `"maximum":10`, `\"neighbors\" field`} {
		if !strings.Contains(body, want) {
			t.Errorf("tools/list descriptor missing %s: %s", want, body)
		}
	}
}

func TestRenderNeighborsTrimsLongBucketsHonestly(t *testing.T) {
	n := NewBlockNeighbors()
	for i := 0; i < maxRenderedNeighbors+3; i++ {
		n.Callers = append(n.Callers, Neighbor{Symbol: string(rune('A' + i)), Edge: "calls", Direction: DirectionIn, Hops: 1})
	}
	n.BucketTotals["callers"] = 30
	n.Truncated = true

	line := renderNeighbors(&n)
	if !strings.Contains(line, "+25 more (of 30 total)") {
		t.Errorf("rendered line hides the dropped neighbors: %s", line)
	}
	if !strings.Contains(line, "(buckets trimmed)") {
		t.Errorf("rendered line hides bucket truncation: %s", line)
	}
	if empty := renderNeighbors(&BlockNeighbors{}); empty != "" {
		t.Errorf("an empty neighborhood must render nothing, got %q", empty)
	}
	if none := renderNeighbors(nil); none != "" {
		t.Errorf("a nil annotation must render nothing, got %q", none)
	}
}

func TestSortAndTrimNeighborsAreDeterministic(t *testing.T) {
	list := []Neighbor{
		{ID: "b:2", Symbol: "B", Hops: 2, Confidence: graph.ConfidenceOf(graph.Proven)},
		{ID: "a:1", Symbol: "A", Hops: 1, Confidence: graph.ConfidenceOf(graph.Candidate)},
		{ID: "c:3", Symbol: "C", Hops: 1, Confidence: graph.ConfidenceOf(graph.Verified)},
	}
	SortNeighbors(list)
	if got := []string{list[0].Symbol, list[1].Symbol, list[2].Symbol}; got[0] != "C" || got[1] != "A" || got[2] != "B" {
		t.Fatalf("sort order = %v, want nearest hop then strongest tier (C, A, B)", got)
	}

	long := make([]Neighbor, MaxNeighborsPerBucket+1)
	kept, trimmed := TrimNeighbors(long)
	if !trimmed || len(kept) != MaxNeighborsPerBucket {
		t.Fatalf("trim kept %d (trimmed=%v), want %d capped", len(kept), trimmed, MaxNeighborsPerBucket)
	}
	if _, trimmed := TrimNeighbors(long[:MaxNeighborsPerBucket]); trimmed {
		t.Error("an exactly-full bucket must not report truncation")
	}

	proximity := []Neighbor{
		{ID: "cross:1", Symbol: "Alpha", Hops: 1, Confidence: graph.ConfidenceOf(graph.Pattern), ProximityDepth: 9},
		{ID: "shallow:1", Symbol: "Beta", Hops: 1, Confidence: graph.ConfidenceOf(graph.Pattern), ProximitySameRepo: true, ProximityDepth: 1},
		{ID: "deep:1", Symbol: "Zulu", Hops: 1, Confidence: graph.ConfidenceOf(graph.Pattern), ProximitySameRepo: true, ProximityDepth: 3},
	}
	SortNeighbors(proximity)
	if got := []string{proximity[0].ID, proximity[1].ID, proximity[2].ID}; got[0] != "deep:1" || got[1] != "shallow:1" || got[2] != "cross:1" {
		t.Fatalf("proximity sort = %v, want same repo then deepest shared directory", got)
	}
}

func TestRenderNeighborTraversalLimitUsesLowerBounds(t *testing.T) {
	n := NewBlockNeighbors()
	n.TotalIsExact = false
	n.Truncated = true
	n.ExpansionLimit = 10000
	if got := renderNeighbors(&n); !strings.Contains(got, "lower bounds") {
		t.Fatalf("empty limited annotation hides cutoff: %q", got)
	}
	n.Callees = []Neighbor{{Symbol: "Callee"}}
	n.BucketTotals["callees"] = 50
	got := renderNeighbors(&n)
	if !strings.Contains(got, "at least") || !strings.Contains(got, "lower bounds") || strings.Contains(got, "50 total") {
		t.Fatalf("misleading exact total: %q", got)
	}
}
