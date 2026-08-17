package server

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

	if got, want := graphNodeSymbols(result.Nodes), []string{"Caller", "Middle", "Root"}; !reflect.DeepEqual(got, want) {
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
	if got, want := graphNodeSymbols(deeper.Nodes), []string{"Caller", "Leaf", "Middle", "Root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("trace_calls two-hop symbols = %v, want %v", got, want)
	}
}

func TestTraceConsumersReturnsEveryPublisherAndConsumer(t *testing.T) {
	fixture := newGraphFixture(t)
	result, _ := callGraphTool(t, graphHandler(t, fixture.tools, "trace_consumers"), `{"name":"OrderSubmitted"}`)

	if got, want := graphNodeSymbols(result.Nodes), []string{"OrderConsumer", "OrderSubmitted", "Send"}; !reflect.DeepEqual(got, want) {
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

	if got, want := graphNodeSymbols(result.Nodes), []string{"Caller", "Leaf", "Middle", "Root"}; !reflect.DeepEqual(got, want) {
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

func TestGraphToolInputValidation(t *testing.T) {
	fixture := newGraphFixture(t)
	tests := []struct {
		tool string
		args string
	}{
		{"trace_calls", `{}`},
		{"trace_calls", `{"symbol":"Root","hops":0}`},
		{"trace_calls", `{"symbol":"Root","hops":11}`},
		{"trace_calls", `{"symbol":"Root","extra":true}`},
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
