package graphserve

import (
	"context"
	"encoding/json"
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

type graphFixture struct {
	tools *GraphToolset
	dir   string
}

func newGraphFixture(t *testing.T) graphFixture {
	t.Helper()
	dir := t.TempDir()
	files := []struct {
		rel     string
		sha     string
		content string
	}{
		{"leaf.go", "leaf-sha", "package fixture\nfunc Leaf() {}\n"},
		{"middle.go", "middle-sha", "package fixture\nfunc Middle() { Leaf() }\n"},
		{"root.go", "root-sha", "package fixture\nfunc Root() { Middle() }\n"},
		{"caller.go", "caller-sha", "package fixture\nfunc Caller() { Root() }\n"},
		{"event.cs", "event-sha", "public record OrderSubmitted(int Id);\n"},
		{"consumer.cs", "consumer-sha", "public class OrderConsumer : IConsumer<OrderSubmitted>\n{\n}\n"},
		{"publisher.cs", "publisher-sha", "public class Publisher\n{\n    public void Send()\n    {\n        endpoint.Publish(new OrderSubmitted(1));\n    }\n}\n"},
		// A definition no edge touches. It is a graph NODE (the catalog is built
		// from the symbol corpus, not from the edge set), which is what lets an
		// edgeless search hit report an empty neighborhood rather than none at all.
		{"orphan.go", "orphan-sha", "package fixture\nfunc Orphan() {}\n"},
	}
	ix := index.New()
	content := make(map[string]string, len(files))
	for _, file := range files {
		content[file.sha] = file.content
		ix.AddFile("fixture", file.rel, filepath.Join(dir, file.rel), file.sha, []byte(file.content))
	}
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatalf("save graph fixture shard: %v", err)
	}

	key := func(sha, name string) diskgraph.Key {
		t.Helper()
		off := strings.Index(content[sha], name)
		if off < 0 {
			t.Fatalf("fixture %s lacks %q", sha, name)
		}
		return diskgraph.Key{BlobSHA: sha, SymbolOffset: uint64(off)}
	}
	evidence := func(sha, name string, last bool) graph.Evidence {
		t.Helper()
		off := strings.Index(content[sha], name)
		if last {
			off = strings.LastIndex(content[sha], name)
		}
		if off < 0 {
			t.Fatalf("fixture evidence %s lacks %q", sha, name)
		}
		return graph.Evidence{BlobSHA: sha, ByteOffset: uint64(off), ByteLength: uint64(len(name))}
	}

	leaf := key("leaf-sha", "Leaf")
	middle := key("middle-sha", "Middle")
	root := key("root-sha", "Root")
	caller := key("caller-sha", "Caller")
	event := key("event-sha", "OrderSubmitted")
	consumer := key("consumer-sha", "OrderConsumer")
	publisher := key("publisher-sha", "Send")

	b := diskgraph.NewBuilder()
	for _, edge := range []struct {
		source diskgraph.Key
		typeID diskgraph.EdgeType
		target diskgraph.Key
		tier   graph.ConfidenceTier
		proof  graph.Evidence
	}{
		{middle, diskgraph.EdgeCalls, leaf, graph.Pattern, evidence("middle-sha", "Leaf", true)},
		{root, diskgraph.EdgeCalls, middle, graph.Pattern, evidence("root-sha", "Middle", true)},
		{caller, diskgraph.EdgeCalls, root, graph.Pattern, evidence("caller-sha", "Root", true)},
		{consumer, diskgraph.EdgeConsumes, event, graph.Verified, evidence("consumer-sha", "OrderSubmitted", true)},
		{publisher, diskgraph.EdgePublishes, event, graph.Pattern, evidence("publisher-sha", "OrderSubmitted", true)},
		{leaf, diskgraph.EdgeCalls, key("orphan-sha", "Orphan"), graph.Candidate, evidence("leaf-sha", "Leaf", false)},
		{root, diskgraph.EdgeCandidate, key("orphan-sha", "Orphan"), graph.Candidate, evidence("root-sha", "Root", false)},
	} {
		if err := b.AddEdge(edge.source, diskgraph.Edge{
			Type:         edge.typeID,
			TargetBlob:   edge.target.BlobSHA,
			TargetOffset: edge.target.SymbolOffset,
			Confidence:   edge.tier,
			Evidence:     edge.proof,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Save(GraphPath(dir)); err != nil {
		t.Fatalf("save graph fixture: %v", err)
	}

	tools, err := OpenGraphTools(dir)
	if err != nil {
		t.Fatalf("OpenGraphTools: %v", err)
	}
	t.Cleanup(func() { _ = tools.Close() })
	return graphFixture{tools: tools, dir: dir}
}

func graphHandler(t *testing.T, tools *GraphToolset, name string) mcp.ToolHandler {
	t.Helper()
	for _, tool := range tools.Tools() {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("graph handler %q not registered", name)
	return nil
}

func callGraphTool(t *testing.T, tool mcp.ToolHandler, args string) (GraphQueryResult, map[string]interface{}) {
	t.Helper()
	got, err := tool.Call(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s Call: %v", tool.Name(), err)
	}
	structured, ok := got["structuredContent"].(GraphQueryResult)
	if !ok {
		t.Fatalf("%s result lacks GraphQueryResult: %#v", tool.Name(), got)
	}
	return structured, got
}

func graphNodeSymbols(nodes []GraphNode) []string {
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, node.Symbol)
	}
	return out
}

func graphEdgeTypes(edges []GraphEdge) []string {
	out := make([]string, 0, len(edges))
	for _, edge := range edges {
		out = append(out, edge.Type)
	}
	return out
}

func TestTraceCallsReturnsCallersAndCalleesUpToHops(t *testing.T) {
	fixture := newGraphFixture(t)
	tool := graphHandler(t, fixture.tools, "trace_calls")
	result, _ := callGraphTool(t, tool, `{"symbol":"Root","hops":1}`)

	if got, want := graphNodeSymbols(result.Nodes), []string{"Root", "Caller", "Middle"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("trace_calls symbols = %v, want %v", got, want)
	}
	if got, want := graphEdgeTypes(result.Edges), []string{"calls", "calls"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("trace_calls edge types = %v, want %v", got, want)
	}
	for _, edge := range result.Edges {
		if edge.Confidence.Tier != graph.Pattern || edge.Confidence.Score != graph.Pattern.Score() || !edge.Evidence.Valid() {
			t.Errorf("trace_calls edge lacks structured confidence/evidence: %+v", edge)
		}
	}

	deeper, _ := callGraphTool(t, tool, `{"symbol":"Root","hops":2}`)
	if got, want := graphNodeSymbols(deeper.Nodes), []string{"Root", "Caller", "Middle", "Leaf"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("trace_calls two-hop symbols = %v, want %v", got, want)
	}
	for _, node := range deeper.Nodes {
		wantHop := map[string]int{"Root": 0, "Caller": 1, "Middle": 1, "Leaf": 2}[node.Symbol]
		if node.Hops != wantHop {
			t.Errorf("%s hops = %d, want %d", node.Symbol, node.Hops, wantHop)
		}
	}
}

func TestStandaloneNodeOrderingUsesAnchorProximityBeforeStableIdentity(t *testing.T) {
	dir := t.TempDir()
	type sourceFile struct {
		repo, rel, sha, symbol, content string
	}
	files := []sourceFile{
		{repo: "anchor", rel: "svc/api/root.go", sha: "root-proximity", symbol: "Root", content: "package api\nfunc Root() {}\n"},
		{repo: "anchor", rel: "svc/api/deep.go", sha: "deep-proximity", symbol: "Zulu", content: "package api\nfunc Zulu() {}\n"},
		{repo: "anchor", rel: "svc/other.go", sha: "shallow-proximity", symbol: "Mike", content: "package svc\nfunc Mike() {}\n"},
		{repo: "other", rel: "svc/api/cross.go", sha: "cross-proximity", symbol: "Alpha", content: "package api\nfunc Alpha() {}\n"},
	}
	ix := index.New()
	keys := make(map[string]diskgraph.Key)
	for _, file := range files {
		ix.AddFile(file.repo, file.rel, filepath.Join(dir, file.repo, file.rel), file.sha, []byte(file.content))
		keys[file.symbol] = diskgraph.Key{BlobSHA: file.sha, SymbolOffset: uint64(strings.Index(file.content, file.symbol))}
	}
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}
	builder := diskgraph.NewBuilder()
	for _, target := range []string{"Zulu", "Mike", "Alpha"} {
		if err := builder.AddEdge(keys["Root"], diskgraph.Edge{
			Type: diskgraph.EdgeCalls, TargetBlob: keys[target].BlobSHA, TargetOffset: keys[target].SymbolOffset,
			Confidence: graph.Pattern, Evidence: graph.Evidence{BlobSHA: keys["Root"].BlobSHA, ByteOffset: keys["Root"].SymbolOffset, ByteLength: 1},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := builder.Save(GraphPath(dir)); err != nil {
		t.Fatal(err)
	}
	tools, err := OpenGraphTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer tools.Close()
	result, _ := callGraphTool(t, graphHandler(t, tools, "trace_calls"), `{"symbol":"Root","hops":1}`)
	got := make([]string, 0, len(result.Nodes))
	for _, node := range result.Nodes {
		got = append(got, node.Symbol)
	}
	if want := []string{"Root", "Zulu", "Mike", "Alpha"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("standalone node order = %v, want %v", got, want)
	}
}

func TestGraphConfidenceFloorFiltersBeforeTraversal(t *testing.T) {
	fixture := newGraphFixture(t)
	tool := graphHandler(t, fixture.tools, "trace_calls")
	defaultResult, _ := callGraphTool(t, tool, `{"symbol":"Root","hops":3}`)
	if strings.Contains(strings.Join(graphNodeSymbols(defaultResult.Nodes), ","), "Orphan") {
		t.Fatalf("default Pattern floor exposed Candidate path: %+v", defaultResult.Nodes)
	}
	explicit, _ := callGraphTool(t, tool, `{"symbol":"Root","hops":3,"min_confidence":"Candidate"}`)
	if !strings.Contains(strings.Join(graphNodeSymbols(explicit.Nodes), ","), "Orphan") {
		t.Fatalf("explicit Candidate floor did not restore Candidate edge: %+v", explicit.Nodes)
	}
}

func TestGraphResultsCoalesceDuplicateRelationshipEvidence(t *testing.T) {
	source := diskgraph.Key{BlobSHA: "source", SymbolOffset: 1}
	target := diskgraph.Key{BlobSHA: "target", SymbolOffset: 2}
	relations := make(map[string]graphRelation)
	addRelation(relations, graphRelation{Source: source, Target: target, Type: diskgraph.EdgePublishes,
		Confidence: graph.Pattern, Evidence: graph.Evidence{BlobSHA: "source", ByteOffset: 20, ByteLength: 1}})
	addRelation(relations, graphRelation{Source: source, Target: target, Type: diskgraph.EdgePublishes,
		Confidence: graph.Verified, Evidence: graph.Evidence{BlobSHA: "source", ByteOffset: 10, ByteLength: 1}})
	if len(relations) != 1 {
		t.Fatalf("duplicate source|type|target relationships = %d, want 1", len(relations))
	}
	for _, relation := range relations {
		if relation.Confidence != graph.Verified || relation.Evidence.ByteOffset != 10 {
			t.Fatalf("coalesced relationship = %+v, want strongest evidence", relation)
		}
	}
}

func TestGraphTraversalPrunesDisconnectedNameCollisionRoots(t *testing.T) {
	connected := diskgraph.Key{BlobSHA: "connected", SymbolOffset: 1}
	disconnected := diskgraph.Key{BlobSHA: "collision", SymbolOffset: 1}
	target := diskgraph.Key{BlobSHA: "target", SymbolOffset: 1}
	confidence := rootsWithConfidence([]diskgraph.Key{connected, disconnected})
	distances := rootsWithDistance([]diskgraph.Key{connected, disconnected})
	relations := map[string]graphRelation{"edge": {Source: connected, Target: target, Type: diskgraph.EdgeQueries, Confidence: graph.Verified}}
	pruneDisconnectedRoots(confidence, distances, []diskgraph.Key{connected, disconnected}, relations)
	if _, ok := confidence[disconnected]; ok {
		t.Fatal("same-named root with no matching relationship survived traversal")
	}
	if _, ok := confidence[connected]; !ok {
		t.Fatal("connected root was pruned")
	}
}

func TestTraceConsumersReturnsEveryPublisherAndConsumer(t *testing.T) {
	fixture := newGraphFixture(t)
	result, _ := callGraphTool(t, graphHandler(t, fixture.tools, "trace_consumers"), `{"name":"OrderSubmitted"}`)

	if got, want := graphNodeSymbols(result.Nodes), []string{"OrderSubmitted", "OrderConsumer", "Send"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("trace_consumers symbols = %v, want %v", got, want)
	}
	if got, want := graphEdgeTypes(result.Edges), []string{"consumes", "publishes"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("trace_consumers edge types = %v, want %v", got, want)
	}
	if result.Edges[0].Confidence.Tier != graph.Verified || result.Edges[1].Confidence.Tier != graph.Pattern {
		t.Fatalf("trace_consumers confidence tiers = %+v", result.Edges)
	}
}

func TestImpactAnalysisReturnsTransitiveDependentsForFile(t *testing.T) {
	fixture := newGraphFixture(t)
	tool := graphHandler(t, fixture.tools, "impact_analysis")
	result, _ := callGraphTool(t, tool, `{"file":"leaf.go","depth":3}`)

	if got, want := graphNodeSymbols(result.Nodes), []string{"Leaf", "Middle", "Root", "Caller"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("impact_analysis symbols = %v, want %v", got, want)
	}
	if len(result.Edges) != 3 {
		t.Fatalf("impact_analysis edges = %+v, want three-level reverse closure", result.Edges)
	}
	var callerConfidence graph.Confidence
	for _, node := range result.Nodes {
		if node.Symbol == "Caller" {
			callerConfidence = node.Confidence
		}
	}
	if callerConfidence.Tier != graph.Pattern || callerConfidence.Score != graph.Pattern.Score() {
		t.Errorf("Caller path confidence = %+v, want Pattern", callerConfidence)
	}

	bySymbol, _ := callGraphTool(t, tool, `{"symbol":"Leaf","depth":2}`)
	if got, want := graphNodeSymbols(bySymbol.Nodes), []string{"Leaf", "Middle", "Root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("impact_analysis symbol closure = %v, want %v", got, want)
	}
}

// TestImpactAnalysisFindsTargetOnlyNodeAsFileRoot pins F-35: an edge target
// that has no enclosing named symbol (e.g. httpgraph.go's httpNodeOffset
// falling back to an HTTP handler literal's own raw byte offset when no
// function/method encloses a module-scope route registration) is never a
// SOURCE of any edge, so it is absent from diskgraph.Graph.Keys(). Without
// buildCatalog also folding in edge TARGETS, that node carries no catalog
// entry, so rootsForFile(its file) -- and therefore impact_analysis(file=...)
// -- silently returns zero roots and misses every caller in the blast radius.
func TestImpactAnalysisFindsTargetOnlyNodeAsFileRoot(t *testing.T) {
	dir := t.TempDir()
	callerContent := []byte("package fixture\nfunc Caller() { CallHandler() }\n")
	// A module-scope route registration with no enclosing function: the
	// literal itself has no name, so a real extractor (see
	// internal/graph/httproute/extract.go's emitOwned) would leave
	// SymbolStart at -1 and fall back to the literal's own start offset.
	handlerContent := []byte("app.get('/orders', (req, res) => { doWork() })\n")

	ix := index.New()
	ix.AddFile("fixture", "caller.go", filepath.Join(dir, "caller.go"), "caller-sha", callerContent)
	ix.AddFile("fixture", "handler.js", filepath.Join(dir, "handler.js"), "handler-sha", handlerContent)
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatalf("save shard: %v", err)
	}

	callerOffset := uint64(strings.Index(string(callerContent), "Caller"))
	handlerOffset := uint64(strings.Index(string(handlerContent), "app.get"))

	b := diskgraph.NewBuilder()
	if err := b.AddEdge(diskgraph.Key{BlobSHA: "caller-sha", SymbolOffset: callerOffset}, diskgraph.Edge{
		Type:         diskgraph.EdgeHTTPCalls,
		TargetBlob:   "handler-sha",
		TargetOffset: handlerOffset,
		Confidence:   graph.Verified,
		Evidence:     graph.Evidence{BlobSHA: "caller-sha", ByteOffset: callerOffset, ByteLength: 6},
	}); err != nil {
		t.Fatalf("AddEdge: %v", err)
	}
	if err := b.Save(GraphPath(dir)); err != nil {
		t.Fatalf("save graph fixture: %v", err)
	}

	tools, err := OpenGraphTools(dir)
	if err != nil {
		t.Fatalf("OpenGraphTools: %v", err)
	}
	t.Cleanup(func() { _ = tools.Close() })
	snapshot := tools.acquire()
	if snapshot == nil {
		t.Fatal("graph snapshot unavailable")
	}
	if kind := snapshot.nodes[diskgraph.Key{BlobSHA: "handler-sha", SymbolOffset: handlerOffset}].Kind; kind != "Route" {
		t.Errorf("raw HTTP target kind = %q, want Route", kind)
	}
	snapshot.wg.Done()

	tool := graphHandler(t, tools, "impact_analysis")
	result, _ := callGraphTool(t, tool, `{"file":"handler.js","depth":1}`)
	if len(result.Nodes) == 0 {
		t.Fatalf("impact_analysis(file=handler.js) found no roots; a target-only node with no enclosing symbol is unreachable via rootsForFile (F-35)")
	}
	foundCaller := false
	for _, edge := range result.Edges {
		if edge.Type == "http_calls" {
			foundCaller = true
		}
	}
	if !foundCaller {
		t.Fatalf("impact_analysis(file=handler.js) edges = %+v, want the caller's http_calls edge into the handler", result.Edges)
	}
}

// TestOpenGraphToolsDegradesWhenGraphSidecarMissing pins F-02: a shard dir with
// no corpus-graph.graph on disk (unbuilt, or built by an older moedex-index that
// predates this feature) must still let OpenGraphTools succeed, exactly like
// moedex-index build/refresh treats a graph build failure as a non-fatal
// warning and Reload keeps serving the old (possibly absent) graph rather than
// failing. A hard error here propagates straight to a fatal daemon-boot exit in
// cmd/moedex-serve, taking every navigation and search tool down over an
// optional sidecar.
func TestOpenGraphToolsDegradesWhenGraphSidecarMissing(t *testing.T) {
	dir := t.TempDir()
	ix := index.New()
	ix.AddFile("fixture", "leaf.go", filepath.Join(dir, "leaf.go"), "leaf-sha", []byte("package fixture\nfunc Leaf() {}\n"))
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatalf("save shard: %v", err)
	}
	// Deliberately no graph builder run here: GraphPath(dir) does not exist.

	tools, err := OpenGraphTools(dir)
	if err != nil {
		t.Fatalf("OpenGraphTools with missing graph sidecar must degrade, not fail: %v", err)
	}
	t.Cleanup(func() { _ = tools.Close() })

	// Every registered tool must still answer (not panic, not transport-error)
	// with a graceful "no graph" result rather than crashing the caller.
	for _, tool := range tools.Tools() {
		args := `{}`
		switch tool.Name() {
		case "trace_calls", "trace_hierarchy", "trace_queries", "trace_renders":
			args = `{"symbol":"Leaf"}`
		case "trace_consumers":
			args = `{"name":"Leaf"}`
		case "impact_analysis":
			args = `{"symbol":"Leaf"}`
		}
		got, callErr := tool.Call(context.Background(), json.RawMessage(args))
		if callErr != nil {
			t.Fatalf("%s Call returned transport error on a graphless toolset: %v", tool.Name(), callErr)
		}
		if isErr, _ := got["isError"].(bool); !isErr {
			t.Fatalf("%s on a graphless toolset = %#v, want a graceful isError result", tool.Name(), got)
		}
	}

	// Graph-fused search annotation must also degrade to "no annotation" rather
	// than erroring, matching the documented Neighbors contract.
	neighbors, err := tools.Neighbors(context.Background(), []contextwin.ContextBlock{{AbsPath: filepath.Join(dir, "leaf.go"), StartLine: 1, EndLine: 2}}, 1)
	if err != nil || neighbors != nil {
		t.Fatalf("Neighbors on a graphless toolset = (%v, %v), want (nil, nil)", neighbors, err)
	}
}

// TestGraphToolsetReloadReportsOpenFailureWithoutSwapping pins half of F-23:
// when the NEW generation fails to build, Reload must report that failure
// through its FIRST (openErr) return value and must leave the old generation
// installed and serving -- a genuinely failed reload, not a live-but-leaky
// one.
func TestGraphToolsetReloadReportsOpenFailureWithoutSwapping(t *testing.T) {
	fixture := newGraphFixture(t)

	// Reload against a directory with no shards and no graph sidecar at all:
	// openGraphSnapshot must fail before ever touching g.cur.
	badDir := t.TempDir()
	openErr, closeErr := fixture.tools.Reload(badDir)
	if openErr == nil {
		t.Fatal("Reload against an unopenable dir returned no openErr")
	}
	if closeErr != nil {
		t.Fatalf("Reload against an unopenable dir returned a closeErr (%v); the swap never happened, so nothing should have been closed", closeErr)
	}

	// The old generation must still be the one being served: a query that only
	// the original fixture graph can answer still works.
	result, _ := callGraphTool(t, graphHandler(t, fixture.tools, "trace_calls"), `{"symbol":"Root","hops":1}`)
	if len(result.Nodes) == 0 {
		t.Fatalf("trace_calls after a failed Reload returned no nodes; the old generation was not kept serving")
	}
}

// TestGraphToolsetReloadSwapsInNewGenerationOnSuccess pins the other half of
// F-23: a clean Reload reports success on both return values and the NEW
// generation is what subsequent calls see.
func TestGraphToolsetReloadSwapsInNewGenerationOnSuccess(t *testing.T) {
	fixture := newGraphFixture(t)

	openErr, closeErr := fixture.tools.Reload(fixture.dir)
	if openErr != nil {
		t.Fatalf("Reload openErr = %v, want nil", openErr)
	}
	if closeErr != nil {
		t.Fatalf("Reload closeErr = %v, want nil (releasing the retired-but-valid old generation should not fail)", closeErr)
	}

	result, _ := callGraphTool(t, graphHandler(t, fixture.tools, "trace_calls"), `{"symbol":"Root","hops":1}`)
	if len(result.Nodes) == 0 {
		t.Fatalf("trace_calls after a successful Reload returned no nodes; the new generation was not installed")
	}
}

func TestGraphToolInputValidation(t *testing.T) {
	fixture := newGraphFixture(t)
	impactSchema := graphHandler(t, fixture.tools, "impact_analysis").Specification().InputSchema
	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		if _, exists := impactSchema[keyword]; exists {
			t.Fatalf("impact_analysis input schema contains host-incompatible root %s: %#v", keyword, impactSchema)
		}
	}
	tests := []struct {
		tool string
		args string
	}{
		{"trace_calls", `{}`},
		{"trace_calls", `{"symbol":"Root","hops":0}`},
		{"trace_calls", `{"symbol":"Root","hops":11}`},
		{"trace_calls", `{"symbol":"Root","extra":true}`},
		{"trace_calls", `{"symbol":"Root","min_confidence":"candidate"}`},
		{"trace_consumers", `null`},
		{"trace_consumers", `{"name":" "}`},
		{"impact_analysis", `{}`},
		{"impact_analysis", `{"file":"leaf.go","symbol":"Leaf"}`},
		{"impact_analysis", `{"file":"leaf.go","depth":0}`},
		{"impact_analysis", `{"symbol":"Leaf","unknown":1}`},
	}
	for _, tc := range tests {
		t.Run(tc.tool+"_"+tc.args, func(t *testing.T) {
			result, err := graphHandler(t, fixture.tools, tc.tool).Call(context.Background(), json.RawMessage(tc.args))
			if err != nil {
				t.Fatalf("validation returned transport error: %v", err)
			}
			isError, ok := result["isError"].(bool)
			if !ok || !isError {
				t.Fatalf("invalid input accepted: %#v", result)
			}
			meta := result["_meta"].(map[string]interface{})
			identity := meta[mcp.SnapshotMetaKey].(mcp.SnapshotIdentity)
			if identity.Cacheable {
				t.Fatalf("invalid input was marked cacheable: %+v", identity)
			}
		})
	}
}

type graphFakeSearcher struct{}

func (graphFakeSearcher) SearchContext(context.Context, string, int, int) (contextwin.ContextWindow, error) {
	return contextwin.ContextWindow{}, nil
}

func TestGraphToolsAreListedAndDispatchedThroughMCP(t *testing.T) {
	fixture := newGraphFixture(t)
	handler := mcp.NewServer(graphFakeSearcher{}, mcp.WithTools(fixture.tools.Tools()...)).HTTPHandler()
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	listed := post(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	for _, name := range []string{"trace_calls", "trace_consumers", "impact_analysis"} {
		if !strings.Contains(listed.Body.String(), `"name":"`+name+`"`) {
			t.Errorf("tools/list missing %s: %s", name, listed.Body.String())
		}
	}
	called := post(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"trace_calls","arguments":{"symbol":"Root","hops":1}}}`)
	if called.Code != http.StatusOK || !strings.Contains(called.Body.String(), `"structuredContent"`) || !strings.Contains(called.Body.String(), `"symbol":"Caller"`) {
		t.Fatalf("MCP trace_calls dispatch failed: status=%d body=%s", called.Code, called.Body.String())
	}
}
