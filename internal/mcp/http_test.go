package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"moedex/internal/contextwin"
)

// postRPC drives one raw JSON-RPC body through the Streamable HTTP handler.
func postRPC(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeRPC(t *testing.T, rec *httptest.ResponseRecorder) response {
	t.Helper()
	var resp response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, rec.Body.String())
	}
	return resp
}

// TestHTTPInitializeAndToolsList confirms the HTTP transport speaks the same
// handshake as stdio: an initialize gets the serverInfo, tools/list advertises
// search_context — both as application/json with HTTP 200.
func TestHTTPInitializeAndToolsList(t *testing.T) {
	h := NewServer(&fakeSearcher{}).HTTPHandler()

	rec := postRPC(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	resp := decodeRPC(t, rec)
	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}
	result := resp.Result.(map[string]interface{})
	if result["protocolVersion"] != protocolVersion {
		t.Errorf("protocolVersion = %v, want %v", result["protocolVersion"], protocolVersion)
	}
	si := result["serverInfo"].(map[string]interface{})
	if si["name"] != "moedex" {
		t.Errorf("serverInfo.name = %v, want moedex", si["name"])
	}

	rec = postRPC(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := decodeRPC(t, rec).Result.(map[string]interface{})["tools"].([]interface{})
	if len(tools) != 1 || tools[0].(map[string]interface{})["name"] != "search_context" {
		t.Errorf("tools/list did not advertise search_context: %+v", tools)
	}
}

// TestHTTPToolsCall confirms a tools/call over HTTP reaches the searcher with the
// right args and renders the context window into the tool text — identical to the
// stdio path, since both share handleSafe.
func TestHTTPToolsCall(t *testing.T) {
	fs := &fakeSearcher{win: contextwin.ContextWindow{
		Blocks: []contextwin.ContextBlock{
			{RelPath: "a/b.go", StartLine: 1, EndLine: 3, Text: "func F() {}\n", Score: 0.5},
		},
		TokenEstimate: 7,
	}}
	h := NewServer(fs).HTTPHandler()

	rec := postRPC(t, h, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search_context","arguments":{"query":"F","token_budget":1000,"top_k":5}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d, want 200", rec.Code)
	}
	if fs.gotQuery != "F" || fs.gotBudget != 1000 || fs.gotTopK != 5 {
		t.Errorf("searcher got (%q,%d,%d), want (F,1000,5)", fs.gotQuery, fs.gotBudget, fs.gotTopK)
	}
	resp := decodeRPC(t, rec)
	if resp.Error != nil {
		t.Fatalf("tools/call error: %+v", resp.Error)
	}
	result := resp.Result.(map[string]interface{})
	text := result["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(text, "a/b.go:1-3") {
		t.Errorf("tool text missing block header:\n%s", text)
	}
}

// TestHTTPNotificationReturns202 confirms a notification (no id) is accepted with
// no JSON-RPC body, the HTTP analogue of "produces no reply" on stdio.
func TestHTTPNotificationReturns202(t *testing.T) {
	h := NewServer(&fakeSearcher{}).HTTPHandler()
	rec := postRPC(t, h, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("notification status = %d, want 202", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("notification body = %q, want empty", rec.Body.String())
	}
}

// TestHTTPGetNotAllowed confirms GET (server-initiated SSE stream) is refused —
// this stateless transport only answers POSTed messages.
func TestHTTPGetNotAllowed(t *testing.T) {
	h := NewServer(&fakeSearcher{}).HTTPHandler()
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow header = %q, want POST", got)
	}
}

// TestHTTPParseError confirms malformed JSON yields a JSON-RPC parse error at the
// HTTP 400 layer (matching the MCP SDK convention) without killing the handler.
func TestHTTPParseError(t *testing.T) {
	h := NewServer(&fakeSearcher{}).HTTPHandler()
	rec := postRPC(t, h, `{not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", rec.Code)
	}
	if resp := decodeRPC(t, rec); resp.Error == nil || resp.Error.Code != codeParseError {
		t.Errorf("want parse error %d, got %+v", codeParseError, resp.Error)
	}
}

// TestHTTPOversizedRequestRejected confirms the body cap (shared with stdio) is
// enforced: a request beyond maxRequestBytes is a 413, not an OOM.
func TestHTTPOversizedRequestRejected(t *testing.T) {
	h := NewServer(&fakeSearcher{}, WithMaxRequestBytes(64)).HTTPHandler()
	big := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_context","arguments":{"query":"` +
		strings.Repeat("x", 256) + `"}}}`
	rec := postRPC(t, h, big)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body status = %d, want 413", rec.Code)
	}
}

// TestHTTPUnknownMethod confirms an unknown method round-trips as a JSON-RPC
// method-not-found error at HTTP 200 (the request itself was well-formed).
func TestHTTPUnknownMethod(t *testing.T) {
	h := NewServer(&fakeSearcher{}).HTTPHandler()
	rec := postRPC(t, h, `{"jsonrpc":"2.0","id":9,"method":"does/not/exist"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown method status = %d, want 200", rec.Code)
	}
	if resp := decodeRPC(t, rec); resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Errorf("want method-not-found %d, got %+v", codeMethodNotFound, resp.Error)
	}
}
