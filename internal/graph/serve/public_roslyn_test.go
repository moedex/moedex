package graphserve

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"moedex/internal/graph"
	"moedex/internal/mcp"
)

// ConfidenceTier is an internal enum with string JSON output. Decode the
// public wire shape independently rather than unmarshalling the server model.
type publicGraphWireResult struct {
	Nodes []json.RawMessage `json:"nodes"`
	Edges []struct {
		Type       string `json:"type"`
		Confidence struct {
			Tier  string  `json:"tier"`
			Score float64 `json:"score"`
		} `json:"confidence"`
		Evidence graph.Evidence `json:"evidence"`
	} `json:"edges"`
	Truncated      bool `json:"truncated"`
	TotalIsExact   bool `json:"total_is_exact"`
	ExpansionLimit int  `json:"expansion_limit"`
}

// Exercise the real graph through the MCP wire, including lazy common-name
// traversal and arithmetic schema counts. Default tests require no corpus.
func TestPinnedRoslynFactoredGraphMCP(t *testing.T) {
	dir := os.Getenv("MOEDEX_ROSLYN_GRAPH_DIR")
	if dir == "" {
		t.Skip("set MOEDEX_ROSLYN_GRAPH_DIR for the provisioned public graph gate")
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Heads []struct{ Head string }
	}
	if err := json.Unmarshal(data, &manifest); err != nil || len(manifest.Heads) != 1 || manifest.Heads[0].Head != "36d26c5466e4d25940657ccb8d5b9557ccaf7be1" {
		t.Fatal("graph inputs do not identify the pinned Roslyn checkout")
	}
	started := time.Now()
	tools, err := OpenGraphToolsStrict(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer tools.Close()
	snapshot := tools.acquire()
	if snapshot == nil {
		t.Fatal("published graph unavailable")
	}
	wantEdges, records, sets := snapshot.graph.NumEdges(), snapshot.graph.NumRecords(), snapshot.graph.NumTargetSets()
	wantID, wantGeneration := snapshot.buildID, snapshot.graph.Generation()
	snapshot.wg.Done()
	if records >= wantEdges || sets == 0 {
		t.Fatal("public graph lacks compact source records")
	}
	t.Logf("warm open=%s logical_edges=%d stored_records=%d target_sets=%d build_id=%s", time.Since(started), wantEdges, records, sets, wantID)
	handler := mcp.NewServer(graphFakeSearcher{}, mcp.WithTools(tools.Tools()...)).HTTPHandler()
	call := func(body string) json.RawMessage {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("MCP status %d", rec.Code)
		}
		var wire struct {
			Error  json.RawMessage `json:"error"`
			Result struct {
				IsError    bool                       `json:"isError"`
				Structured json.RawMessage            `json:"structuredContent"`
				Meta       map[string]json.RawMessage `json:"_meta"`
			} `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil || len(wire.Error) > 0 || wire.Result.IsError {
			t.Fatalf("MCP failed: %s", rec.Body.String())
		}
		var identity mcp.SnapshotIdentity
		if err := json.Unmarshal(wire.Result.Meta[mcp.SnapshotMetaKey], &identity); err != nil || identity.GraphBuildID != wantID || identity.GraphGeneration != wantGeneration || !identity.Cacheable {
			t.Fatalf("MCP graph identity changed: %+v (%v)", identity, err)
		}
		t.Logf("MCP structured sha256=%x request=%s", sha256.Sum256(wire.Result.Structured), body)
		return wire.Result.Structured
	}
	var schema struct {
		TotalEdges int            `json:"total_edges"`
		EdgeTypes  map[string]int `json:"edge_types"`
	}
	if err := json.Unmarshal(call(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"graph_schema","arguments":{}}}`), &schema); err != nil {
		t.Fatal(err)
	}
	sum := 0
	for _, n := range schema.EdgeTypes {
		sum += n
	}
	if schema.TotalEdges != wantEdges || sum != wantEdges {
		t.Fatalf("schema omitted factored relationships: total=%d types=%d want=%d", schema.TotalEdges, sum, wantEdges)
	}
	for _, name := range []string{"Main", "Create"} {
		start := time.Now()
		body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"trace_calls","arguments":{"symbol":"` + name + `","hops":1}}}`
		var result publicGraphWireResult
		if err := json.Unmarshal(call(body), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Edges) == 0 || len(result.Edges) > maxGraphTraversalEdges || result.TotalIsExact == result.Truncated {
			t.Fatalf("invalid bounded traversal: %s edges=%d truncated=%t exact=%t", name, len(result.Edges), result.Truncated, result.TotalIsExact)
		}
		if result.Truncated && result.ExpansionLimit != maxGraphTraversalEdges {
			t.Fatalf("missing or incorrect traversal budget: %d", result.ExpansionLimit)
		}
		for _, edge := range result.Edges {
			if edge.Type != "calls" || edge.Confidence.Tier != graph.Pattern.String() || edge.Confidence.Score != graph.Pattern.Score() || !edge.Evidence.Valid() {
				t.Fatalf("syntax evidence was changed or promoted: %+v", edge)
			}
		}
		t.Logf("MCP trace_calls %s=%s nodes=%d edges=%d truncated=%t total_is_exact=%t", name, time.Since(start), len(result.Nodes), len(result.Edges), result.Truncated, result.TotalIsExact)
	}
	start := time.Now()
	var reverse publicGraphWireResult
	if err := json.Unmarshal(call(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"impact_analysis","arguments":{"symbol":"Create","depth":1}}}`), &reverse); err != nil {
		t.Fatal(err)
	}
	if len(reverse.Edges) == 0 || len(reverse.Edges) > maxGraphTraversalEdges || reverse.TotalIsExact == reverse.Truncated || (reverse.Truncated && reverse.ExpansionLimit != maxGraphTraversalEdges) {
		t.Fatalf("invalid bounded reverse traversal: edges=%d truncated=%t exact=%t limit=%d", len(reverse.Edges), reverse.Truncated, reverse.TotalIsExact, reverse.ExpansionLimit)
	}
	t.Logf("MCP impact_analysis Create=%s nodes=%d edges=%d truncated=%t total_is_exact=%t", time.Since(start), len(reverse.Nodes), len(reverse.Edges), reverse.Truncated, reverse.TotalIsExact)
}
