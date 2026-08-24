package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"moedex/internal/graph"
)

// fakeTool is a minimal ToolHandler for exercising the extra-tool registry
// (the mechanism the warm daemon uses to add LSP navigation tools under -tags lsp).
type fakeTool struct {
	name string
	last json.RawMessage
}

func (f *fakeTool) Name() string { return f.name }
func (f *fakeTool) Specification() ToolSpecification {
	return NewToolSpecification(f.name, "fake", map[string]interface{}{"type": "object"})
}
func (f *fakeTool) Descriptor() map[string]interface{} {
	return map[string]interface{}{"name": f.name, "description": "fake", "inputSchema": map[string]interface{}{"type": "object"}}
}
func (f *fakeTool) Call(_ context.Context, args json.RawMessage) (map[string]interface{}, error) {
	f.last = args
	return TextResult("ok:"+f.name, false), nil
}

func TestWithTools_ListedAndDispatched(t *testing.T) {
	ft := &fakeTool{name: "find_definition"}
	h := NewServer(&fakeSearcher{}, WithTools(ft)).HTTPHandler()

	// tools/list must include both the built-in and the extra tool.
	rec := postRPC(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	body := rec.Body.String()
	if !strings.Contains(body, "search_context") || !strings.Contains(body, "find_definition") {
		t.Fatalf("tools/list missing a tool: %s", body)
	}

	// tools/call must dispatch to the extra tool with its raw arguments.
	rec = postRPC(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"find_definition","arguments":{"file":"/x.go","line":5}}}`)
	if out := rec.Body.String(); !strings.Contains(out, "ok:find_definition") {
		t.Fatalf("extra tool not dispatched: %s", out)
	}
	if !strings.Contains(string(ft.last), `"file":"/x.go"`) {
		t.Errorf("handler did not receive raw arguments, got %s", ft.last)
	}
}

func TestWithTools_ReservedAndDuplicateIgnored(t *testing.T) {
	reserved := &fakeTool{name: "search_context"} // must not override the built-in
	a := &fakeTool{name: "dup"}
	b := &fakeTool{name: "dup"} // duplicate name ignored
	s := NewServer(&fakeSearcher{}, WithTools(reserved, a, b))
	if len(s.extraOrder) != 1 || s.byName["dup"] != a {
		t.Fatalf("expected only the first 'dup' registered and reserved name ignored; got %d extras", len(s.extraOrder))
	}
	if _, hijacked := s.byName["search_context"]; hijacked {
		t.Error("search_context must not be overridable via WithTools")
	}
}

func TestStructuredResultPreservesGraphConfidenceAndEvidence(t *testing.T) {
	payload := struct {
		Confidence graph.Confidence `json:"confidence"`
		Evidence   graph.Evidence   `json:"evidence"`
	}{
		Confidence: graph.ConfidenceOf(graph.Pattern),
		Evidence: graph.Evidence{
			BlobSHA:    "0123456789abcdef",
			ByteOffset: 41,
			ByteLength: 12,
		},
	}
	body, err := json.Marshal(StructuredResult("one edge", payload, false))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		`"confidence":{"tier":"Pattern","score":0.6}`,
		`"evidence":{"blob_sha":"0123456789abcdef","byte_offset":41,"byte_length":12}`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("structured MCP result %s does not contain %s", text, want)
		}
	}
}
