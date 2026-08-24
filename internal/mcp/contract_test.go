package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"moedex/internal/contextwin"
)

var allToolNames = []string{
	"search_context",
	"trace_calls", "trace_consumers", "trace_hierarchy", "trace_queries", "trace_renders", "impact_analysis",
	"list_clusters", "list_repos", "graph_schema", "read_source", "graph_neighbors", "list_symbols", "file_tree",
	"find_definition", "find_references", "find_implementations", "find_symbol", "symbols_overview",
}

func modernPost(t *testing.T, h http.Handler, method, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	req.Header.Set("Mcp-Method", method)
	if name != "" {
		req.Header.Set("Mcp-Name", name)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func modernMeta() map[string]interface{} {
	return map[string]interface{}{
		sdkmcp.MetaKeyProtocolVersion:    protocolVersion,
		sdkmcp.MetaKeyClientInfo:         map[string]interface{}{"name": "contract-test", "version": "1"},
		sdkmcp.MetaKeyClientCapabilities: map[string]interface{}{},
	}
}

func modernBody(t *testing.T, id int, method string, params map[string]interface{}) string {
	t.Helper()
	if params == nil {
		params = map[string]interface{}{}
	}
	params["_meta"] = modernMeta()
	data, err := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestModernDiscoveryMetadataAndHeaderValidation(t *testing.T) {
	h := NewServer(&fakeSearcher{}).HTTPHandler()

	discover := modernPost(t, h, "server/discover", "", modernBody(t, 1, "server/discover", nil))
	if discover.Code != http.StatusOK {
		t.Fatalf("server/discover status=%d body=%s", discover.Code, discover.Body.String())
	}
	var envelope map[string]interface{}
	if err := json.Unmarshal(discover.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	result := envelope["result"].(map[string]interface{})
	versions := result["supportedVersions"].([]interface{})
	if len(versions) != 2 || versions[0] != protocolVersion || versions[1] != legacyProtocolVersion {
		t.Fatalf("supportedVersions=%v", versions)
	}
	if result["ttlMs"] != float64(300_000) || result["cacheScope"] != "public" {
		t.Errorf("discovery cache hints=%v/%v", result["ttlMs"], result["cacheScope"])
	}
	meta := result["_meta"].(map[string]interface{})
	serverInfo := meta[sdkmcp.MetaKeyServerInfo].(map[string]interface{})
	if serverInfo["name"] != "moedex" || !strings.Contains(serverInfo["version"].(string), "+") {
		t.Errorf("server identity=%v", serverInfo)
	}
	if !strings.Contains(result["instructions"].(string), "token_budget") || !strings.Contains(result["instructions"].(string), "ready_empty") {
		t.Errorf("instructions incomplete: %v", result["instructions"])
	}

	list := modernPost(t, h, "tools/list", "", modernBody(t, 2, "tools/list", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"ttlMs":300000`) || !strings.Contains(list.Body.String(), `"cacheScope":"public"`) {
		t.Fatalf("tools/list cache contract: status=%d body=%s", list.Code, list.Body.String())
	}

	missingMethod := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(modernBody(t, 3, "tools/list", nil)))
	missingMethod.Header.Set("Content-Type", "application/json")
	missingMethod.Header.Set("Accept", "application/json, text/event-stream")
	missingMethod.Header.Set("MCP-Protocol-Version", protocolVersion)
	missingRec := httptest.NewRecorder()
	h.ServeHTTP(missingRec, missingMethod)
	if missingRec.Code != http.StatusBadRequest || !strings.Contains(missingRec.Body.String(), "Mcp-Method") {
		t.Errorf("missing method header accepted: status=%d body=%s", missingRec.Code, missingRec.Body.String())
	}

	missingVersion := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(modernBody(t, 31, "tools/list", nil)))
	missingVersion.Header.Set("Content-Type", "application/json")
	missingVersion.Header.Set("Accept", "application/json, text/event-stream")
	missingVersion.Header.Set("Mcp-Method", "tools/list")
	missingVersionRec := httptest.NewRecorder()
	h.ServeHTTP(missingVersionRec, missingVersion)
	if missingVersionRec.Code != http.StatusBadRequest || !strings.Contains(missingVersionRec.Body.String(), "does not match") {
		t.Errorf("missing protocol version accepted: status=%d body=%s", missingVersionRec.Code, missingVersionRec.Body.String())
	}

	disagree := modernBody(t, 4, "tools/list", nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(disagree))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", legacyProtocolVersion)
	req.Header.Set("Mcp-Method", "tools/list")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "does not match") {
		t.Errorf("header/body disagreement accepted: status=%d body=%s", rec.Code, rec.Body.String())
	}

	wrongName := modernPost(t, h, "tools/call", "not_search_context", modernBody(t, 5, "tools/call", map[string]interface{}{
		"name": "search_context", "arguments": map[string]interface{}{"query": "x"},
	}))
	if wrongName.Code != http.StatusBadRequest || !strings.Contains(wrongName.Body.String(), "Mcp-Name") {
		t.Errorf("Mcp-Name disagreement accepted: status=%d body=%s", wrongName.Code, wrongName.Body.String())
	}
	missingName := modernPost(t, h, "tools/call", "", modernBody(t, 6, "tools/call", map[string]interface{}{
		"name": "search_context", "arguments": map[string]interface{}{"query": "x"},
	}))
	if missingName.Code != http.StatusBadRequest || !strings.Contains(missingName.Body.String(), "Mcp-Name") {
		t.Errorf("missing Mcp-Name accepted: status=%d body=%s", missingName.Code, missingName.Body.String())
	}
}

func TestModernRequestCancellationPropagatesToTool(t *testing.T) {
	searcher := &blockingSearcher{release: make(chan struct{})}
	h := NewServer(searcher, WithRequestTimeout(10*time.Second)).HTTPHandler()
	ctx, cancel := context.WithCancel(context.Background())
	body := modernBody(t, 1, "tools/call", map[string]interface{}{
		"name": "search_context", "arguments": map[string]interface{}{"query": "blocked"},
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "search_context")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(rec, req)
		close(done)
	}()
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&searcher.inFlight) == 0 {
		select {
		case <-deadline:
			t.Fatal("tool did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP cancellation did not stop request")
	}
	if got := atomic.LoadInt32(&searcher.inFlight); got != 0 {
		t.Fatalf("search remained in flight after cancellation: %d", got)
	}
}

func TestModernBatchRejected(t *testing.T) {
	h := NewServer(&fakeSearcher{}).HTTPHandler()
	one := map[string]interface{}{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
		"params": map[string]interface{}{"_meta": modernMeta()},
	}
	body, _ := json.Marshal([]interface{}{one, one})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("current-protocol batch accepted: %s", rec.Body.String())
	}
}

func TestAllToolDescriptorsHaveClosedWorldContracts(t *testing.T) {
	var extras []ToolHandler
	for _, name := range allToolNames[1:] {
		extras = append(extras, &fakeTool{name: name})
	}
	h := NewServer(&fakeSearcher{}, WithTools(extras...)).HTTPHandler()
	rec := postRPC(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var envelope map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	tools := envelope["result"].(map[string]interface{})["tools"].([]interface{})
	if len(tools) != len(allToolNames) {
		t.Fatalf("tools/list count=%d want=%d", len(tools), len(allToolNames))
	}
	for _, raw := range tools {
		tool := raw.(map[string]interface{})
		name := tool["name"].(string)
		if tool["inputSchema"] == nil || tool["outputSchema"] == nil {
			t.Errorf("%s missing input/output schema", name)
		}
		annotations := tool["annotations"].(map[string]interface{})
		if annotations["readOnlyHint"] != true || annotations["idempotentHint"] != true || annotations["destructiveHint"] != false || annotations["openWorldHint"] != false {
			t.Errorf("%s annotations=%v", name, annotations)
		}
	}
}

func validateFixture(t *testing.T, schemaValue, fixture interface{}) {
	t.Helper()
	schemaJSON, _ := json.Marshal(schemaValue)
	var schema jsonschema.Schema
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		t.Fatalf("schema decode: %v", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatalf("schema resolve: %v", err)
	}
	fixtureJSON, _ := json.Marshal(fixture)
	var wire interface{}
	_ = json.Unmarshal(fixtureJSON, &wire)
	if err := resolved.Validate(wire); err != nil {
		t.Fatalf("fixture invalid: %v\nschema=%s\nfixture=%s", err, schemaJSON, fixtureJSON)
	}
}

func normalFixture(name string) interface{} {
	graph := map[string]interface{}{"tool": name, "query": "q", "depth": 1, "nodes": []interface{}{}, "edges": []interface{}{}}
	position := map[string]interface{}{"line": 1, "column": 1}
	location := map[string]interface{}{"file": "/x.go", "blob_sha": strings.Repeat("a", 40), "start": position, "end": position}
	switch name {
	case "search_context":
		return map[string]interface{}{
			"summary": map[string]interface{}{"blocks": 0, "token_estimate": 0, "truncated": false, "clipped": false},
			"blocks":  []interface{}{},
		}
	case "trace_calls", "trace_consumers", "trace_hierarchy", "trace_queries", "trace_renders", "impact_analysis", "graph_neighbors":
		return graph
	case "list_clusters":
		return map[string]interface{}{"available": false, "status": "unavailable", "graph_generation": 5, "observed_nodes": 1, "observed_edges": 0, "guidance": "rebuild"}
	case "list_repos":
		return map[string]interface{}{"repos": []interface{}{}, "total": 0}
	case "graph_schema":
		return map[string]interface{}{"generation": 5, "corpus_fingerprint": "fp", "build_id": "b", "total_nodes": 0, "total_edges": 0, "node_kinds": map[string]interface{}{}, "edge_types": map[string]interface{}{}}
	case "read_source":
		return map[string]interface{}{"repo": "r", "path": "x.go", "blob_sha": strings.Repeat("a", 40), "lines": 1, "start_line": 1, "end_line": 1, "truncated": false, "content": "x"}
	case "list_symbols":
		return map[string]interface{}{"symbols": []interface{}{}, "total": 0, "truncated": false}
	case "file_tree":
		return map[string]interface{}{"repo": "r", "files": []interface{}{}, "total": 0, "truncated": false}
	case "find_definition", "find_references", "find_implementations":
		return map[string]interface{}{"status": "resolved", "locations": []interface{}{location}}
	case "find_symbol", "symbols_overview":
		return map[string]interface{}{"status": "resolved", "symbols": []interface{}{map[string]interface{}{"name": "F", "kind": "Function", "location": location}}}
	}
	return map[string]interface{}{}
}

func TestEveryOutputSchemaValidatesSuccessAndErrorFixtures(t *testing.T) {
	errorFixture := map[string]interface{}{"error": map[string]interface{}{"code": "invalid_arguments", "message": "bad input"}}
	for _, name := range allToolNames {
		t.Run(name, func(t *testing.T) {
			schema := OutputSchema(name)
			validateFixture(t, schema, normalFixture(name))
			validateFixture(t, schema, errorFixture)
		})
	}
}

func TestEveryOutputSchemaHasPortableRootObject(t *testing.T) {
	for _, name := range allToolNames {
		t.Run(name, func(t *testing.T) {
			schema := OutputSchema(name)
			if schema["type"] != "object" || schema["additionalProperties"] != false {
				t.Fatalf("root schema is not a closed object: %#v", schema)
			}
			for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
				if _, exists := schema[keyword]; exists {
					t.Fatalf("root schema contains host-incompatible %s: %#v", keyword, schema)
				}
			}
		})
	}
}

func TestOfficialSDKClientIntegrationHTTPAndStdio(t *testing.T) {
	newSearcher := func() *fakeSearcher {
		return &fakeSearcher{win: contextwin.ContextWindow{
			Blocks: []contextwin.ContextBlock{{
				BlobSHA: strings.Repeat("a", 40), RelPath: "x.go",
				StartLine: 1, EndLine: 1, Text: "needle\n",
			}},
		}}
	}
	runClient := func(t *testing.T, transport sdkmcp.Transport) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "moedex-contract-client", Version: "1"}, nil)
		session, err := client.Connect(ctx, transport, nil)
		if err != nil {
			t.Fatalf("SDK Connect: %v", err)
		}
		defer session.Close()
		if got := session.InitializeResult().ProtocolVersion; got != protocolVersion {
			t.Fatalf("negotiated protocol=%q want=%q", got, protocolVersion)
		}
		listed, err := session.ListTools(ctx, nil)
		if err != nil || len(listed.Tools) != 1 || listed.Tools[0].OutputSchema == nil {
			t.Fatalf("SDK ListTools: tools=%d err=%v", len(listed.Tools), err)
		}
		called, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "search_context", Arguments: json.RawMessage(`{"query":"needle","token_budget":100}`)})
		if err != nil || called.StructuredContent == nil || len(called.Content) == 0 {
			t.Fatalf("SDK CallTool: result=%+v err=%v", called, err)
		}
		if _, ok := called.Meta[SnapshotMetaKey]; !ok {
			t.Fatalf("SDK CallTool missing %s: %v", SnapshotMetaKey, called.Meta)
		}
		if _, ok := called.Meta[ServerMetaKey]; !ok {
			t.Fatalf("SDK CallTool missing %s: %v", ServerMetaKey, called.Meta)
		}
		if _, ok := called.Meta[sdkmcp.MetaKeyServerInfo]; !ok {
			t.Fatalf("SDK CallTool missing response server identity: %v", called.Meta)
		}
	}

	t.Run("http", func(t *testing.T) {
		srv := NewServer(newSearcher())
		handler := srv.HTTPHandler()
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			return rec.Result(), nil
		})}
		runClient(t, &sdkmcp.StreamableClientTransport{Endpoint: "http://mcp.test/mcp", HTTPClient: client})
	})

	t.Run("stdio", func(t *testing.T) {
		clientReader, serverWriter := io.Pipe()
		serverReader, clientWriter := io.Pipe()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- NewServer(newSearcher()).Serve(ctx, serverReader, serverWriter) }()
		runClient(t, &sdkmcp.IOTransport{Reader: clientReader, Writer: clientWriter})
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("stdio server did not stop")
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
