//go:build lsp

package servecmd

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/mcp"
	"moedex/internal/navigate"
)

// These are the ADR 0018 MCP-surface tests: find_symbol (workspace/symbol) and
// symbols_overview (documentSymbol) exposed as tools on the warm daemon,
// mirroring the position-tool coverage nav_lsp.go already has none of (the
// position tools were tested only via the underlying navigate package). Skips
// automatically when gopls is not on PATH.

const symFixtureShape = `package shape

// Shape is the interface whose implementations we navigate to.
type Shape interface {
	Area() float64
}

// Circle is a concrete Shape.
type Circle struct {
	R float64
}

func (c Circle) Area() float64 {
	return 3.14159 * c.R * c.R
}
`

func writeSymFixture(t *testing.T) (root, shapeFile string) {
	t.Helper()
	root = t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module navfix\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	shapeFile = filepath.Join(root, "shape", "shape.go")
	if err := os.MkdirAll(filepath.Dir(shapeFile), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(shapeFile, []byte(symFixtureShape), 0o644); err != nil {
		t.Fatalf("write shape.go: %v", err)
	}
	return root, shapeFile
}

func symGoplsAvailable(t *testing.T) bool {
	t.Helper()
	_, err := exec.LookPath("gopls")
	return err == nil
}

// callTool runs a tool.Call and extracts the flat text + isError, the shape
// every ToolHandler in this file returns via mcp.TextResult.
func callTool(t *testing.T, h interface {
	Call(context.Context, json.RawMessage) (map[string]interface{}, error)
}, args interface{}) (text string, isError bool) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := h.Call(ctx, raw)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	content, _ := res["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("expected 1 content item, got %d: %v", len(content), res)
	}
	item, _ := content[0].(map[string]interface{})
	text, _ = item["text"].(string)
	isError, _ = res["isError"].(bool)
	return text, isError
}

// TestNavTools_RegistersFindSymbolAndSymbolsOverview is the integration-point
// check: navTools() — what the daemon actually wires up via mcp.WithTools —
// must include both new tools alongside the three position tools. Every other
// test in this file constructs a tool struct directly, which would stay green
// even if navTools() never registered it.
func TestNavTools_RegistersFindSymbolAndSymbolsOverview(t *testing.T) {
	tools, closeFn := navTools()
	t.Cleanup(func() { _ = closeFn() })

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name()] = true
	}
	for _, want := range []string{"find_definition", "find_references", "find_implementations", "find_symbol", "symbols_overview"} {
		if !names[want] {
			t.Errorf("navTools() missing %q; got %v", want, names)
		}
	}
}

func TestFindSymbolTool_Descriptor(t *testing.T) {
	tool := &findSymbolTool{pool: navigate.NewPool(navigate.Config{})}
	t.Cleanup(func() { _ = tool.pool.Close() })
	if tool.Name() != "find_symbol" {
		t.Fatalf("Name() = %q, want find_symbol", tool.Name())
	}
	schema := tool.Specification().InputSchema
	props, _ := schema["properties"].(map[string]interface{})
	for _, want := range []string{"query", "root", "lang"} {
		if _, ok := props[want]; !ok {
			t.Errorf("inputSchema.properties missing %q", want)
		}
	}
	required, _ := schema["required"].([]string)
	if !containsStr(required, "query") || !containsStr(required, "root") {
		t.Errorf("inputSchema.required = %v, want query and root required", required)
	}
}

func TestSymbolsOverviewTool_Descriptor(t *testing.T) {
	tool := &symbolsOverviewTool{pool: navigate.NewPool(navigate.Config{})}
	t.Cleanup(func() { _ = tool.pool.Close() })
	if tool.Name() != "symbols_overview" {
		t.Fatalf("Name() = %q, want symbols_overview", tool.Name())
	}
	schema := tool.Specification().InputSchema
	props, _ := schema["properties"].(map[string]interface{})
	if _, ok := props["file"]; !ok {
		t.Errorf("inputSchema.properties missing %q", "file")
	}
	required, _ := schema["required"].([]string)
	if !containsStr(required, "file") {
		t.Errorf("inputSchema.required = %v, want file required", required)
	}
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func navSnapshot(t *testing.T, result map[string]interface{}) mcp.SnapshotIdentity {
	t.Helper()
	meta, ok := result["_meta"].(map[string]interface{})
	if !ok {
		t.Fatalf("metadata=%T", result["_meta"])
	}
	identity, ok := meta[mcp.SnapshotMetaKey].(mcp.SnapshotIdentity)
	if !ok {
		t.Fatalf("snapshot=%T", meta[mcp.SnapshotMetaKey])
	}
	return identity.Normalize()
}

func TestNavLocationResultHashesQueryAndReturnedFiles(t *testing.T) {
	dir := t.TempDir()
	queryFile := filepath.Join(dir, "query.go")
	returnedFile := filepath.Join(dir, "definition.go")
	queryContent := []byte("package fixture\nfunc use() { Definition() }\n")
	returnedContent := []byte("package fixture\nfunc Definition() {}\n")
	if err := os.WriteFile(queryFile, queryContent, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(returnedFile, returnedContent, 0o600); err != nil {
		t.Fatal(err)
	}
	result := navLocationResult(context.Background(), queryFile, navigate.LocationQueryResult{
		Status: navigate.LocationQueryResolved,
		Locations: []navigate.Location{{
			File:  returnedFile,
			Start: navigate.Pos{File: returnedFile, Line: 2, Col: 6},
			End:   navigate.Pos{File: returnedFile, Line: 2, Col: 16},
		}},
	}, nil)
	identity := navSnapshot(t, result)
	wantQuery := diskstore.GitBlobSHA1(queryContent)
	wantReturned := diskstore.GitBlobSHA1(returnedContent)
	if !identity.Cacheable || !containsStr(identity.BlobSHAs, wantQuery) || !containsStr(identity.BlobSHAs, wantReturned) {
		t.Fatalf("snapshot=%+v", identity)
	}
	structured := result["structuredContent"].(map[string]interface{})
	locations := structured["locations"].([]map[string]interface{})
	if got := locations[0]["blob_sha"]; got != wantReturned {
		t.Fatalf("location blob_sha=%v want %q", got, wantReturned)
	}
}

func TestNavLocationResultKeepsUnreadableLocationUnanchored(t *testing.T) {
	dir := t.TempDir()
	queryFile := filepath.Join(dir, "query.go")
	missing := filepath.Join(dir, "missing.go")
	if err := os.WriteFile(queryFile, []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := navLocationResult(context.Background(), queryFile, navigate.LocationQueryResult{
		Status:    navigate.LocationQueryResolved,
		Locations: []navigate.Location{{File: missing, Start: navigate.Pos{File: missing, Line: 1, Col: 1}}},
	}, nil)
	identity := navSnapshot(t, result)
	if identity.Cacheable || !containsStr(identity.UnanchoredPaths, missing) {
		t.Fatalf("snapshot=%+v", identity)
	}
	locations := result["structuredContent"].(map[string]interface{})["locations"].([]map[string]interface{})
	if _, ok := locations[0]["blob_sha"]; ok {
		t.Fatalf("unreadable location unexpectedly anchored: %v", locations[0])
	}
}

func TestSymbolsResultIsStructuredAndContentAnchored(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "symbols.go")
	content := []byte("package fixture\ntype Shape interface{}\n")
	if err := os.WriteFile(file, content, 0o600); err != nil {
		t.Fatal(err)
	}
	result := symbolsResult(context.Background(), []navigate.Symbol{{
		Name: "Shape", Kind: "Interface",
		Loc: navigate.Location{File: file, Start: navigate.Pos{File: file, Line: 2, Col: 6}},
	}}, file)
	structured := result["structuredContent"].(map[string]interface{})
	if structured["status"] != "resolved" {
		t.Fatalf("status=%v", structured["status"])
	}
	symbols := structured["symbols"].([]map[string]interface{})
	want := diskstore.GitBlobSHA1(content)
	location := symbols[0]["location"].(map[string]interface{})
	if location["blob_sha"] != want {
		t.Fatalf("symbol location=%v want sha %q", location, want)
	}
	if identity := navSnapshot(t, result); !identity.Cacheable || len(identity.BlobSHAs) != 1 || identity.BlobSHAs[0] != want {
		t.Fatalf("snapshot=%+v", identity)
	}
}

func TestFindSymbolTool_Call_MissingArgs_ReturnsError(t *testing.T) {
	tool := &findSymbolTool{pool: navigate.NewPool(navigate.Config{})}
	t.Cleanup(func() { _ = tool.pool.Close() })

	text, isError := callTool(t, tool, map[string]string{"root": "/tmp"}) // missing query
	if !isError {
		t.Errorf("expected isError=true for missing query, got text=%q", text)
	}
}

func TestSymbolsOverviewTool_Call_MissingFile_ReturnsError(t *testing.T) {
	tool := &symbolsOverviewTool{pool: navigate.NewPool(navigate.Config{})}
	t.Cleanup(func() { _ = tool.pool.Close() })

	text, isError := callTool(t, tool, map[string]string{})
	if !isError {
		t.Errorf("expected isError=true for missing file, got text=%q", text)
	}
}

func TestNavTool_Call_ExposesDetailedLocationStatus(t *testing.T) {
	tests := []struct {
		name      string
		result    navigate.LocationQueryResult
		err       error
		wantText  string
		wantError bool
	}{
		{
			name: "resolved",
			result: navigate.LocationQueryResult{
				Status: navigate.LocationQueryResolved,
				Locations: []navigate.Location{{
					File:  "/tmp/a.go",
					Start: navigate.Pos{File: "/tmp/a.go", Line: 3, Col: 4},
				}},
			},
			wantText: "/tmp/a.go:3:4",
		},
		{
			name:     "ready empty",
			result:   navigate.LocationQueryResult{Status: navigate.LocationQueryReadyEmpty},
			wantText: "ready_empty: language server returned no locations",
		},
		{
			name:     "unsupported",
			result:   navigate.LocationQueryResult{Status: navigate.LocationQueryUnsupported},
			wantText: "unsupported: language server does not implement this navigation method",
		},
		{
			name:      "unavailable",
			result:    navigate.LocationQueryResult{Status: navigate.LocationQueryUnavailable},
			err:       context.DeadlineExceeded,
			wantText:  "unavailable: context deadline exceeded",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := &navTool{
				name: "find_definition",
				run: func(context.Context, *navigate.Pool, navigate.Pos, navArgs) (navigate.LocationQueryResult, error) {
					return tt.result, tt.err
				},
			}
			text, isError := callTool(t, tool, map[string]any{"file": "/tmp/a.go", "line": 1})
			if text != tt.wantText {
				t.Errorf("text = %q, want %q", text, tt.wantText)
			}
			if isError != tt.wantError {
				t.Errorf("isError = %v, want %v", isError, tt.wantError)
			}

			raw, err := json.Marshal(map[string]any{"file": "/tmp/a.go", "line": 1})
			if err != nil {
				t.Fatal(err)
			}
			res, err := tool.Call(context.Background(), raw)
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			structured, ok := res["structuredContent"].(map[string]interface{})
			if !ok {
				t.Fatalf("structuredContent = %T, want map", res["structuredContent"])
			}
			if got := structured["status"]; got != string(tt.result.Status) {
				t.Errorf("structured status = %v, want %q", got, tt.result.Status)
			}
			if strings.Contains(strings.ToLower(text), "external") {
				t.Errorf("text must not infer an external symbol: %q", text)
			}
		})
	}
}

// TestFindSymbolTool_Call_ColdRootNoLang_ReturnsNoResults pins the "coverage
// honesty" contract at the MCP surface: find_symbol with no lang on a root with
// no already-live server must answer "no results", not spawn a server or
// error. No gopls needed — the pool never touches a process.
func TestFindSymbolTool_Call_ColdRootNoLang_ReturnsNoResults(t *testing.T) {
	tool := &findSymbolTool{pool: navigate.NewPool(navigate.Config{})}
	t.Cleanup(func() { _ = tool.pool.Close() })

	text, isError := callTool(t, tool, map[string]string{"query": "Circle", "root": "/tmp/does-not-exist"})
	if isError {
		t.Fatalf("expected isError=false, got true (text=%q)", text)
	}
	if text != "no results" {
		t.Errorf("text = %q, want %q", text, "no results")
	}
}

func TestFindSymbolTool_Call_ReturnsPinnedFormat(t *testing.T) {
	if !symGoplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping find_symbol tool test")
	}
	root, shapeFile := writeSymFixture(t)
	tool := &findSymbolTool{pool: navigate.NewPool(navigate.Config{})}
	t.Cleanup(func() { _ = tool.pool.Close() })

	var text string
	var isError bool
	for attempt := 0; attempt < 8; attempt++ {
		text, isError = callTool(t, tool, map[string]string{"query": "Circle", "root": root, "lang": "go"})
		if !isError && text != "no results" {
			break
		}
		time.Sleep(750 * time.Millisecond)
	}
	if isError {
		t.Fatalf("find_symbol returned an error: %q", text)
	}
	if text == "no results" {
		t.Fatal("find_symbol never found Circle")
	}

	declLine := lineOf(t, shapeFile, "type Circle struct")
	var found bool
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			t.Fatalf("line %q does not have 3 tab-separated fields (name\\tkind\\tfile:line:col)", line)
		}
		if fields[0] != "Circle" {
			continue
		}
		found = true
		if fields[1] == "" {
			t.Errorf("Circle line has empty kind: %q", line)
		}
		wantPrefix := shapeFile + ":" + strconv.Itoa(declLine) + ":"
		if !strings.HasPrefix(fields[2], wantPrefix) {
			t.Errorf("Circle location = %q, want file:line prefix %s", fields[2], wantPrefix)
		}
	}
	if !found {
		t.Errorf("Circle not found in find_symbol output:\n%s", text)
	}
}

func TestSymbolsOverviewTool_Call_ReturnsPinnedFormat(t *testing.T) {
	if !symGoplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping symbols_overview tool test")
	}
	_, shapeFile := writeSymFixture(t)
	tool := &symbolsOverviewTool{pool: navigate.NewPool(navigate.Config{})}
	t.Cleanup(func() { _ = tool.pool.Close() })

	var text string
	var isError bool
	for attempt := 0; attempt < 8; attempt++ {
		text, isError = callTool(t, tool, map[string]string{"file": shapeFile})
		if !isError && text != "no results" {
			break
		}
		time.Sleep(750 * time.Millisecond)
	}
	if isError {
		t.Fatalf("symbols_overview returned an error: %q", text)
	}
	if text == "no results" {
		t.Fatal("symbols_overview never returned results")
	}

	names := make(map[string]bool)
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			t.Fatalf("line %q does not have 3 tab-separated fields", line)
		}
		names[fields[0]] = true
	}
	for _, want := range []string{"Shape", "Circle", "Area"} {
		if !names[want] {
			t.Errorf("symbols_overview missing %q; got:\n%s", want, text)
		}
	}
}

func lineOf(t *testing.T, file, needle string) int {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	idx := strings.Index(string(data), needle)
	if idx < 0 {
		t.Fatalf("needle %q not found in %s", needle, file)
	}
	return 1 + strings.Count(string(data[:idx]), "\n")
}
