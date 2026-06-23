package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"moedex/internal/contextwin"
	"moedex/internal/index"
	"moedex/internal/rank"
	"moedex/internal/tokenindex"
)

// fakeSearcher records the args it was called with and returns a canned window.
type fakeSearcher struct {
	gotQuery  string
	gotBudget int
	gotTopK   int
	win       contextwin.ContextWindow
}

func (f *fakeSearcher) SearchContext(_ context.Context, q string, budget, topK int) (contextwin.ContextWindow, error) {
	f.gotQuery, f.gotBudget, f.gotTopK = q, budget, topK
	return f.win, nil
}

// drive feeds newline-delimited JSON-RPC requests through Serve and returns the
// decoded responses (in order). Notifications produce no response.
func drive(t *testing.T, s *Server, msgs ...interface{}) []response {
	t.Helper()
	var in bytes.Buffer
	enc := json.NewEncoder(&in)
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := s.Serve(context.Background(), &in, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var resps []response
	dec := json.NewDecoder(&out)
	for dec.More() {
		var r response
		if err := dec.Decode(&r); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		resps = append(resps, r)
	}
	return resps
}

func TestInitializeHandshake(t *testing.T) {
	s := NewServer(&fakeSearcher{})
	resps := drive(t, s, map[string]interface{}{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
	})
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	res := resps[0].Result.(map[string]interface{})
	if res["protocolVersion"] != protocolVersion {
		t.Errorf("protocolVersion = %v, want %v", res["protocolVersion"], protocolVersion)
	}
	si := res["serverInfo"].(map[string]interface{})
	if si["name"] != "moedex" {
		t.Errorf("serverInfo.name = %v, want moedex", si["name"])
	}
}

func TestInitializedNotificationGetsNoReply(t *testing.T) {
	s := NewServer(&fakeSearcher{})
	// A notification has no id; it must not produce a response.
	resps := drive(t, s, map[string]interface{}{
		"jsonrpc": "2.0", "method": "notifications/initialized",
	})
	if len(resps) != 0 {
		t.Fatalf("notification should yield no response, got %d", len(resps))
	}
}

func TestToolsListAdvertisesSearchContext(t *testing.T) {
	s := NewServer(&fakeSearcher{})
	resps := drive(t, s, map[string]interface{}{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list",
	})
	res := resps[0].Result.(map[string]interface{})
	tools := res["tools"].([]interface{})
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool := tools[0].(map[string]interface{})
	if tool["name"] != "search_context" {
		t.Errorf("tool name = %v, want search_context", tool["name"])
	}
	if _, ok := tool["inputSchema"]; !ok {
		t.Error("tool missing inputSchema")
	}
}

func TestToolsCallPassesArgsAndReturnsText(t *testing.T) {
	fs := &fakeSearcher{win: contextwin.ContextWindow{
		Blocks: []contextwin.ContextBlock{
			{RelPath: "a.go", StartLine: 3, EndLine: 5, Text: "func A() {}\n", Score: 1.5},
		},
		TokenEstimate: 4,
	}}
	s := NewServer(fs)
	resps := drive(t, s, map[string]interface{}{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]interface{}{
			"name": "search_context",
			"arguments": map[string]interface{}{
				"query": "needle", "token_budget": 500, "top_k": 7,
			},
		},
	})
	if fs.gotQuery != "needle" || fs.gotBudget != 500 || fs.gotTopK != 7 {
		t.Errorf("searcher got (%q,%d,%d), want (needle,500,7)", fs.gotQuery, fs.gotBudget, fs.gotTopK)
	}
	res := resps[0].Result.(map[string]interface{})
	if res["isError"] != false {
		t.Errorf("isError = %v, want false", res["isError"])
	}
	content := res["content"].([]interface{})
	text := content[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(text, "a.go:3-5") || !strings.Contains(text, "func A()") {
		t.Errorf("rendered text missing block header/body:\n%s", text)
	}
}

func TestEmptyQueryIsToolError(t *testing.T) {
	s := NewServer(&fakeSearcher{})
	resps := drive(t, s, map[string]interface{}{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]interface{}{"name": "search_context", "arguments": map[string]interface{}{"query": "   "}},
	})
	res := resps[0].Result.(map[string]interface{})
	if res["isError"] != true {
		t.Errorf("empty query should set isError=true, got %v", res["isError"])
	}
}

func TestUnknownMethodErrors(t *testing.T) {
	s := NewServer(&fakeSearcher{})
	resps := drive(t, s, map[string]interface{}{
		"jsonrpc": "2.0", "id": 5, "method": "does/not/exist",
	})
	if resps[0].Error == nil || resps[0].Error.Code != codeMethodNotFound {
		t.Errorf("expected method-not-found error, got %+v", resps[0].Error)
	}
}

// TestIndexSearcherEndToEnd exercises the real adapter: rank -> contextwin over a
// tiny index, with no dense arm.
func TestIndexSearcherEndToEnd(t *testing.T) {
	ix := index.New()
	content := "package main\n\nfunc Connect() {\n\tdatabase.Open()\n}\n"
	ix.AddFile("repo", "db.go", "/abs/db.go", "sha-db", []byte(content))
	ti := tokenindex.Build(ix)
	ranker := rank.New(ix, ti, nil, nil, rank.Config{})
	searcher := NewIndexSearcher(ix, ranker, 0)

	win, err := searcher.SearchContext(context.Background(), "database", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(win.Blocks) == 0 {
		t.Fatal("expected at least one context block for 'database'")
	}
	if !strings.Contains(win.Blocks[0].Text, "database.Open()") {
		t.Errorf("block should contain the database line, got:\n%s", win.Blocks[0].Text)
	}
}
