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

func TestToolsCallStructuredFormat(t *testing.T) {
	fs := &fakeSearcher{win: contextwin.ContextWindow{
		Blocks: []contextwin.ContextBlock{
			{Blob: 42, Repo: "r", RelPath: "a.go", AbsPath: "/abs/a.go", StartLine: 3, EndLine: 5,
				Text: "func A() {}\n", Score: 1.5, Lexical: 7.4, Dense: 0.8},
		},
		TokenEstimate: 4,
		Truncated:     true,
	}}
	s := NewServer(fs)
	resps := drive(t, s, map[string]interface{}{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]interface{}{
			"name": "search_context",
			"arguments": map[string]interface{}{
				"query": "needle", "format": "structured",
			},
		},
	})
	res := resps[0].Result.(map[string]interface{})
	if res["isError"] != false {
		t.Errorf("isError = %v, want false", res["isError"])
	}
	// Text fallback (content) must still be present for clients that ignore structuredContent.
	content := res["content"].([]interface{})
	if _, ok := content[0].(map[string]interface{})["text"].(string); !ok {
		t.Error("structured result missing text fallback in content")
	}
	sc, ok := res["structuredContent"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing structuredContent, got %T", res["structuredContent"])
	}
	summary := sc["summary"].(map[string]interface{})
	if summary["blocks"].(float64) != 1 || summary["truncated"].(bool) != true {
		t.Errorf("summary = %+v, want blocks=1 truncated=true", summary)
	}
	blocks := sc["blocks"].([]interface{})
	if len(blocks) != 1 {
		t.Fatalf("want 1 structured block, got %d", len(blocks))
	}
	blk := blocks[0].(map[string]interface{})
	// Provenance the text format drops must survive here.
	if blk["blob"].(float64) != 42 {
		t.Errorf("blob = %v, want 42", blk["blob"])
	}
	if blk["score"].(float64) != 1.5 || blk["lexical"].(float64) != 7.4 || blk["dense"].(float64) != 0.8 {
		t.Errorf("provenance = (score %v, lexical %v, dense %v), want (1.5, 7.4, 0.8)", blk["score"], blk["lexical"], blk["dense"])
	}
	if blk["rel_path"].(string) != "a.go" || blk["start_line"].(float64) != 3 || blk["end_line"].(float64) != 5 {
		t.Errorf("block location = %+v, want a.go:3-5", blk)
	}
}

func TestStructuredBlockCarriesPathWithNamespace(t *testing.T) {
	// The corpus is laid out as <root>/<path_with_namespace>, so a block's
	// abs_path = <root>/<namespace>/<rel_path>. With the corpus root known, the
	// emitted structured block must carry the FULL namespace (so an agent can
	// clone the repo), while keeping the leaf `repo` field for back-compat.
	root := "/corpus/TCGitlab"
	fs := &fakeSearcher{win: contextwin.ContextWindow{
		Blocks: []contextwin.ContextBlock{{
			Blob:    7,
			Repo:    "TC.MarketplaceApi", // leaf only (the bug's symptom)
			RelPath: "src/Api/Handler.cs",
			AbsPath: "/corpus/TCGitlab/Services.Domains/TC.MarketplaceApi/src/Api/Handler.cs",
			Text:    "class Handler {}\n",
		}},
	}}
	s := NewServer(fs, WithCorpusRoot(root))
	resps := drive(t, s, map[string]interface{}{
		"jsonrpc": "2.0", "id": 9, "method": "tools/call",
		"params": map[string]interface{}{
			"name":      "search_context",
			"arguments": map[string]interface{}{"query": "needle", "format": "structured"},
		},
	})
	res := resps[0].Result.(map[string]interface{})
	sc := res["structuredContent"].(map[string]interface{})
	blk := sc["blocks"].([]interface{})[0].(map[string]interface{})

	if got := blk["repo"].(string); got != "TC.MarketplaceApi" {
		t.Errorf("repo (leaf, back-compat) = %q, want %q", got, "TC.MarketplaceApi")
	}
	pwn, ok := blk["path_with_namespace"].(string)
	if !ok {
		t.Fatalf("structured block missing path_with_namespace field; block=%+v", blk)
	}
	if pwn != "Services.Domains/TC.MarketplaceApi" {
		t.Errorf("path_with_namespace = %q, want %q", pwn, "Services.Domains/TC.MarketplaceApi")
	}
}

func TestStructuredBlockNamespaceOmittedWhenUnderivable(t *testing.T) {
	// No corpus root configured (e.g. single-repo moedex-mcp): the namespace can't
	// be derived, so the field must be empty/omitted, never fabricated.
	fs := &fakeSearcher{win: contextwin.ContextWindow{
		Blocks: []contextwin.ContextBlock{{
			Repo: "r", RelPath: "a.go", AbsPath: "/somewhere/a.go", Text: "x\n",
		}},
	}}
	s := NewServer(fs) // no WithCorpusRoot
	resps := drive(t, s, map[string]interface{}{
		"jsonrpc": "2.0", "id": 10, "method": "tools/call",
		"params": map[string]interface{}{
			"name":      "search_context",
			"arguments": map[string]interface{}{"query": "x", "format": "structured"},
		},
	})
	res := resps[0].Result.(map[string]interface{})
	sc := res["structuredContent"].(map[string]interface{})
	blk := sc["blocks"].([]interface{})[0].(map[string]interface{})
	if pwn, ok := blk["path_with_namespace"]; ok && pwn.(string) != "" {
		t.Errorf("path_with_namespace should be empty/omitted when underivable, got %q", pwn)
	}
}

func TestDeriveNamespace(t *testing.T) {
	cases := []struct {
		name, root, abs, rel, want string
	}{
		{"multi-level namespace", "/c/TCGitlab",
			"/c/TCGitlab/Services.Domains/TC.MarketplaceApi/src/Foo.cs", "src/Foo.cs",
			"Services.Domains/TC.MarketplaceApi"},
		{"single-level namespace", "/c/TCGitlab",
			"/c/TCGitlab/Solo/main.go", "main.go", "Solo"},
		{"trailing-slash root tolerated", "/c/TCGitlab/",
			"/c/TCGitlab/Grp/Repo/x.go", "x.go", "Grp/Repo"},
		{"abs not under root -> empty", "/c/TCGitlab",
			"/other/Repo/x.go", "x.go", ""},
		{"no root -> empty", "",
			"/c/TCGitlab/Grp/Repo/x.go", "x.go", ""},
		{"rel not a suffix of abs -> empty", "/c/TCGitlab",
			"/c/TCGitlab/Grp/Repo/x.go", "totally/different.go", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deriveNamespace(c.abs, c.rel, c.root); got != c.want {
				t.Errorf("deriveNamespace(%q, %q, %q) = %q, want %q", c.abs, c.rel, c.root, got, c.want)
			}
		})
	}
}

func TestUnknownFormatIsToolError(t *testing.T) {
	s := NewServer(&fakeSearcher{})
	resps := drive(t, s, map[string]interface{}{
		"jsonrpc": "2.0", "id": 6, "method": "tools/call",
		"params": map[string]interface{}{
			"name":      "search_context",
			"arguments": map[string]interface{}{"query": "x", "format": "yaml"},
		},
	})
	res := resps[0].Result.(map[string]interface{})
	if res["isError"] != true {
		t.Errorf("unknown format should set isError=true, got %v", res["isError"])
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
