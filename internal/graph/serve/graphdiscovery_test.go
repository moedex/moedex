package graphserve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"moedex/internal/contextwin"
	"moedex/internal/diskstore"
	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
	"moedex/internal/ingest"
	"moedex/internal/mcp"
	"moedex/internal/rank"
)

// buildDiscoveryFixture creates a temp shard dir with two repos, a few files,
// and a small graph — enough to exercise all six discovery tools.
func buildDiscoveryFixture(t *testing.T) *GraphToolset {
	t.Helper()
	dir := t.TempDir()
	ix := index.New()

	type blob struct {
		repo, file, sha string
		content         []byte
	}
	blobs := []blob{
		{"accounts", "api.go", "sha-a", []byte("package accounts\n\nfunc AccountAPI() {}\n")},
		{"accounts", "store.go", "sha-b", []byte("package accounts\n\nfunc AccountStore() {}\n")},
		{"accounts", "model.go", "sha-d", []byte("package accounts\n\ntype Account struct {}\n")},
		{"billing", "worker.go", "sha-c", []byte("package billing\n\nfunc BillingWorker() {}\n")},
		{"billing", "service.go", "sha-e", []byte("package billing\n\ntype Service struct{}\n\nfunc (s *Service) Run() {}\n")},
	}
	keys := make(map[string]diskgraph.Key)
	for _, b := range blobs {
		ix.AddFile(b.repo, b.file, filepath.Join(dir, b.repo, b.file), b.sha, b.content)
		nameStart := strings.Index(string(b.content), "func ") + 5
		if nameStart < 5 {
			nameStart = strings.Index(string(b.content), "type ") + 5
		}
		keys[b.sha] = diskgraph.Key{BlobSHA: b.sha, SymbolOffset: uint64(nameStart)}
	}
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}

	builder := diskgraph.NewBuilder()
	builder.AddEdge(keys["sha-a"], diskgraph.Edge{
		Type: diskgraph.EdgeCalls, TargetBlob: "sha-b", TargetOffset: keys["sha-b"].SymbolOffset,
		Confidence: graph.Proven,
		Evidence:   graph.Evidence{BlobSHA: "sha-a", ByteOffset: keys["sha-a"].SymbolOffset, ByteLength: 1},
	})
	builder.AddEdge(keys["sha-a"], diskgraph.Edge{
		Type: diskgraph.EdgeImports, TargetBlob: "sha-d", TargetOffset: keys["sha-d"].SymbolOffset,
		Confidence: graph.Candidate,
		Evidence:   graph.Evidence{BlobSHA: "sha-a", ByteOffset: keys["sha-a"].SymbolOffset, ByteLength: 1},
	})
	builder.AddNode(keys["sha-b"])
	builder.AddNode(keys["sha-c"])
	builder.AddNode(keys["sha-d"])
	if err := builder.Save(GraphPath(dir)); err != nil {
		t.Fatal(err)
	}

	tools, err := OpenGraphTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tools.Close() })
	return tools
}

func findTool(tools *GraphToolset, name string) *discoveryTool {
	for _, t := range tools.Tools() {
		if t.Name() == name {
			if dt, ok := t.(*discoveryTool); ok {
				return dt
			}
		}
	}
	return nil
}

func callTool(t *testing.T, tools *GraphToolset, name string, argsJSON string) map[string]interface{} {
	t.Helper()
	tool := findTool(tools, name)
	if tool == nil {
		t.Fatalf("tool %q not registered", name)
	}
	result, err := tool.Call(context.Background(), json.RawMessage(argsJSON))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return result
}

// ---------------------------------------------------------------------------
// list_repos
// ---------------------------------------------------------------------------

func TestListReposReturnsAllRepos(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	result := callTool(t, tools, "list_repos", `{}`)
	if result["isError"] == true {
		t.Fatalf("unexpected error: %v", result)
	}
	sc := result["structuredContent"]
	raw, _ := json.Marshal(sc)
	var parsed struct {
		Repos []repoSummary `json:"repos"`
		Total int           `json:"total"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, raw)
	}
	if parsed.Total != 2 {
		t.Fatalf("total = %d, want 2", parsed.Total)
	}
	names := make(map[string]bool)
	for _, r := range parsed.Repos {
		names[r.Name] = true
	}
	if !names["accounts"] || !names["billing"] {
		t.Fatalf("repos = %v, want accounts and billing", names)
	}
}

func TestListReposFilter(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	result := callTool(t, tools, "list_repos", `{"filter":"bill"}`)
	raw, _ := json.Marshal(result["structuredContent"])
	var parsed struct {
		Repos []repoSummary `json:"repos"`
		Total int           `json:"total"`
	}
	json.Unmarshal(raw, &parsed)
	if parsed.Total != 1 || parsed.Repos[0].Name != "billing" {
		t.Fatalf("filtered repos = %+v, want [billing]", parsed.Repos)
	}
}

// ---------------------------------------------------------------------------
// graph_schema
// ---------------------------------------------------------------------------

func TestGraphSchemaReturnsCountsAndTypes(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	result := callTool(t, tools, "graph_schema", ``)
	if result["isError"] == true {
		t.Fatalf("unexpected error: %v", result)
	}
	raw, _ := json.Marshal(result["structuredContent"])
	var schema discoverySchema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if schema.TotalNodes <= 0 {
		t.Fatalf("total_nodes = %d, want > 0", schema.TotalNodes)
	}
	if schema.TotalEdges <= 0 {
		t.Fatalf("total_edges = %d, want > 0", schema.TotalEdges)
	}
	if schema.Generation != diskgraph.FirstGeneration {
		t.Fatalf("generation = %d, want %d", schema.Generation, diskgraph.FirstGeneration)
	}
	if len(schema.CorpusFingerprint) != 64 {
		t.Fatalf("corpus_fingerprint = %q, want SHA-256", schema.CorpusFingerprint)
	}
	if len(schema.BuildID) != 64 {
		t.Fatalf("build_id = %q, want graph SHA-256", schema.BuildID)
	}
	if _, ok := schema.EdgeTypes["calls"]; !ok {
		t.Fatalf("edge_types missing 'calls': %v", schema.EdgeTypes)
	}
}

func TestGraphSchemaRejectsArgs(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	result := callTool(t, tools, "graph_schema", `{"extra":1}`)
	if result["isError"] != true {
		t.Fatalf("expected error for extra args, got: %v", result)
	}
}

// ---------------------------------------------------------------------------
// read_source
// ---------------------------------------------------------------------------

func TestReadSourceExactPath(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	result := callTool(t, tools, "read_source", `{"repo":"accounts","path":"api.go"}`)
	if result["isError"] == true {
		t.Fatalf("unexpected error: %v", result)
	}
	raw, _ := json.Marshal(result["structuredContent"])
	var sr sourceResult
	json.Unmarshal(raw, &sr)
	if sr.Repo != "accounts" {
		t.Fatalf("repo = %q, want accounts", sr.Repo)
	}
	if !strings.Contains(sr.Content, "AccountAPI") {
		t.Fatalf("content missing AccountAPI: %q", sr.Content)
	}
}

func TestReadSourceSuffixFallback(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	result := callTool(t, tools, "read_source", `{"path":"worker.go"}`)
	if result["isError"] == true {
		t.Fatalf("unexpected error: %v", result)
	}
	raw, _ := json.Marshal(result["structuredContent"])
	var sr sourceResult
	json.Unmarshal(raw, &sr)
	if sr.Repo != "billing" {
		t.Fatalf("repo = %q, want billing", sr.Repo)
	}
	if !strings.Contains(sr.Content, "BillingWorker") {
		t.Fatalf("content missing BillingWorker: %q", sr.Content)
	}
}

func TestReadSourceNotFound(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	result := callTool(t, tools, "read_source", `{"repo":"accounts","path":"nonexistent.go"}`)
	if result["isError"] != true {
		t.Fatalf("expected error for missing file, got: %v", result)
	}
}

func TestReadSourceLineRange(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	result := callTool(t, tools, "read_source", `{"repo":"billing","path":"service.go","start_line":3,"end_line":3}`)
	if result["isError"] == true {
		t.Fatalf("unexpected error: %v", result)
	}
	raw, _ := json.Marshal(result["structuredContent"])
	var sr sourceResult
	json.Unmarshal(raw, &sr)
	if sr.StartLine != 3 || sr.EndLine != 3 {
		t.Fatalf("line range = %d-%d, want 3-3", sr.StartLine, sr.EndLine)
	}
	if strings.Contains(sr.Content, "package") {
		t.Fatalf("content should not contain package line: %q", sr.Content)
	}
}

func TestReadSourceCitationTextPreservesStructuredContent(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	result := callTool(t, tools, "read_source", `{"repo":"billing","path":"service.go","start_line":2,"end_line":3}`)
	raw, err := json.Marshal(result["structuredContent"])
	if err != nil {
		t.Fatal(err)
	}
	var source sourceResult
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	if source.Content != "\ntype Service struct{}\n" || source.StartLine != 2 || source.EndLine != 3 || source.BlobSHA != "sha-e" {
		t.Fatalf("structured source or provenance changed: %+v", source)
	}
	text := result["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	if text != "--- service.go:2-3 (repo billing) ---\n2 | \n3 | type Service struct{}\n" {
		t.Fatalf("incorrect numbered citation: %q", text)
	}
}

func TestReadSourceCompactPresentation(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	full := callTool(t, tools, "read_source", `{"repo":"billing","path":"service.go","start_line":2,"end_line":3}`)
	compact := callTool(t, tools, "read_source", `{"repo":"billing","path":"service.go","start_line":2,"end_line":3,"format":"structured"}`)
	if !reflect.DeepEqual(full["structuredContent"], compact["structuredContent"]) || !reflect.DeepEqual(full["_meta"], compact["_meta"]) {
		t.Fatalf("presentation changed source or identity: full=%+v compact=%+v", full, compact)
	}
	text := compact["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	if strings.Contains(text, "type Service") || !strings.Contains(text, "service.go:2-3") || !strings.Contains(text, "truncated=true") {
		t.Fatalf("unexpected compact fallback: %q", text)
	}
	for _, args := range []string{
		`{"path":"service.go","format":"raw"}`,
		`{"path":"service.go","format":""}`,
		`{"path":"service.go","format":7}`,
		`{"path":"service.go","start_line":4,"end_line":2}`,
		`{"path":"service.go","start_line":9223372036854775807}`,
	} {
		got := callTool(t, tools, "read_source", args)
		if got["isError"] != true {
			t.Fatalf("expected invalid arguments for %s: %+v", args, got)
		}
	}
}

func buildSourceLineFixture(t *testing.T, raw, indexed []byte) (*GraphToolset, *index.Index) {
	t.Helper()
	dir := t.TempDir()
	ix := index.New()
	ix.AddFile("sample", "sample.txt", filepath.Join(dir, "sample.txt"), diskstore.GitBlobSHA1(raw), indexed)
	if err := diskstore.Save(ix, filepath.Join(dir, "000.idx")); err != nil {
		t.Fatal(err)
	}
	tools, err := OpenSourceTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tools.Close() })
	return tools, ix
}

func decodeSourceResult(t *testing.T, result map[string]interface{}) sourceResult {
	t.Helper()
	if result["isError"] == true {
		t.Fatalf("unexpected read_source error: %+v", result)
	}
	raw, err := json.Marshal(result["structuredContent"])
	if err != nil {
		t.Fatal(err)
	}
	var source sourceResult
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	return source
}

func TestReadSourcePhysicalLines(t *testing.T) {
	for _, tc := range []struct {
		name, content, last string
		lines               int
	}{
		{"ten CRLF lines", "one\r\ntwo\r\nthree\r\nfour\r\nfive\r\nsix\r\nseven\r\neight\r\nnine\r\nten\r\n", "ten\r\n", 10},
		{"terminal LF", "first\nlast\n", "last\n", 2},
		{"unterminated LF", "first\nlast", "last", 2},
		{"interior blank", "first\n\nlast\n", "last\n", 3},
		{"physical blank last", "first\n\n", "\n", 2},
		{"single blank line", "\n", "\n", 1},
		{"empty", "", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools, ix := buildSourceLineFixture(t, []byte(tc.content), []byte(tc.content))
			for _, format := range []string{"text", "structured"} {
				result := callTool(t, tools, "read_source", fmt.Sprintf(`{"path":"sample.txt","format":%q}`, format))
				source := decodeSourceResult(t, result)
				start := 1
				if tc.lines == 0 {
					start = 0
				}
				if source.Content != tc.content || source.Lines != tc.lines || source.StartLine != start || source.EndLine != tc.lines || source.Truncated || source.BlobSHA != diskstore.GitBlobSHA1([]byte(tc.content)) {
					t.Fatalf("incorrect full physical source: %+v", source)
				}
				if format == "text" {
					text := result["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
					if strings.Contains(text, fmt.Sprintf("\n%d |", tc.lines+1)) {
						t.Fatalf("numbered nonexistent source line: %q", text)
					}
				}
			}
			if tc.lines == 0 {
				result := callTool(t, tools, "read_source", `{"path":"sample.txt","start_line":2}`)
				if result["isError"] != true {
					t.Fatalf("start beyond empty content must fail: %+v", result)
				}
			} else {
				// LineOf for an actual final byte agrees with physical citation
				// bounds. Do not reinterpret the index's EOF offset sentinel.
				if got := ix.Blob(0).LineOf(len(tc.content) - 1); got != tc.lines {
					t.Fatalf("index last-byte line = %d, want %d", got, tc.lines)
				}
				for _, end := range []int{tc.lines, tc.lines + 1} {
					result := callTool(t, tools, "read_source", fmt.Sprintf(`{"path":"sample.txt","start_line":%d,"end_line":%d}`, tc.lines, end))
					source := decodeSourceResult(t, result)
					if source.StartLine != tc.lines || source.EndLine != tc.lines || source.Content != tc.last || source.Truncated {
						t.Fatalf("incorrect EOF selection/clamping: %+v", source)
					}
				}
				result := callTool(t, tools, "read_source", fmt.Sprintf(`{"path":"sample.txt","start_line":%d}`, tc.lines+1))
				if result["isError"] != true {
					t.Fatalf("EOF+1 start must fail: %+v", result)
				}
			}
			if tc.name == "interior blank" {
				source := decodeSourceResult(t, callTool(t, tools, "read_source", `{"path":"sample.txt","start_line":2,"end_line":2}`))
				if source.Content != "\n" || source.StartLine != 2 || source.EndLine != 2 || !source.Truncated {
					t.Fatalf("blank line lost its terminator or coordinates: %+v", source)
				}
			}
			assertSearchSourceLineAgreement(t, tools, ix, tc.content, tc.lines)
		})
	}
}

// Exercise context assembly and both MCP presentations against the same
// indexed bytes. Search context retains its existing final-LF presentation
// convention; read_source must return the exact source bytes instead.
func assertSearchSourceLineAgreement(t *testing.T, tools *GraphToolset, ix *index.Index, content string, lines int) {
	t.Helper()
	window := contextwin.Assemble(ix, []rank.RankedResult{{
		Blob: 0, Files: ix.Blob(0).Files, Score: 1,
		LineSpans: []rank.LineSpan{{StartLine: 1, EndLine: max(1, lines)}},
	}}, contextwin.Options{TokenBudget: 1000})
	handler := mcp.NewServer(staticContextSearcher{window: window}, mcp.WithTools(tools.Tools()...)).HTTPHandler()
	for _, format := range []string{"text", "structured"} {
		request := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_context","arguments":{"query":"sample","format":%q}}}`, format)
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(request))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("search_context status = %d: %s", rec.Code, rec.Body.String())
		}
		var response struct {
			Result struct {
				StructuredContent struct {
					Blocks []struct {
						StartLine int    `json:"start_line"`
						EndLine   int    `json:"end_line"`
						Text      string `json:"text"`
						BlobSHA   string `json:"blob_sha"`
					} `json:"blocks"`
				} `json:"structuredContent"`
			} `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		blocks := response.Result.StructuredContent.Blocks
		if lines == 0 {
			if len(blocks) != 0 {
				t.Fatalf("empty source produced citeable search blocks: %+v", blocks)
			}
			continue
		}
		if len(blocks) != 1 {
			t.Fatalf("expected one search block: %+v", blocks)
		}
		block := blocks[0]
		source := decodeSourceResult(t, callTool(t, tools, "read_source", fmt.Sprintf(`{"path":"sample.txt","start_line":%d,"end_line":%d}`, block.StartLine, block.EndLine)))
		wantSearch := content
		if !strings.HasSuffix(wantSearch, "\n") {
			wantSearch += "\n"
		}
		if block.StartLine != 1 || block.EndLine != lines || block.Text != wantSearch || block.BlobSHA != source.BlobSHA || source.Content != content || source.EndLine != block.EndLine {
			t.Fatalf("search/read_source physical bounds or bytes disagree: block=%+v source=%+v", block, source)
		}
	}
}

func TestReadSourceBOMIngest(t *testing.T) {
	dir := t.TempDir()
	const content = "first\r\nlast\r\n"
	raw := append([]byte{0xef, 0xbb, 0xbf}, []byte(content)...)
	if err := os.WriteFile(filepath.Join(dir, "sample.txt"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "sample.txt"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	files, err := ingest.Repo("sample", dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("ingest: files=%+v err=%v", files, err)
	}
	file := files[0]
	if !bytes.Equal(file.Content, []byte(content)) || file.SHA != diskstore.GitBlobSHA1(raw) || file.SHA == diskstore.GitBlobSHA1(file.Content) {
		t.Fatalf("BOM ingest bytes or raw blob identity changed: %+v", file)
	}
	tools, ix := buildSourceLineFixture(t, raw, file.Content)
	source := decodeSourceResult(t, callTool(t, tools, "read_source", `{"path":"sample.txt"}`))
	if source.Content != content || source.Lines != 2 || source.EndLine != 2 || source.BlobSHA != file.SHA {
		t.Fatalf("read_source changed indexed BOM policy or blob identity: %+v", source)
	}
	assertSearchSourceLineAgreement(t, tools, ix, content, 2)
}

func TestReadSourceLineLimitRetainsTerminator(t *testing.T) {
	content := strings.Repeat("line\r\n", maxReadSourceLines+1)
	tools, _ := buildSourceLineFixture(t, []byte(content), []byte(content))
	source := decodeSourceResult(t, callTool(t, tools, "read_source", `{"path":"sample.txt"}`))
	if source.Lines != maxReadSourceLines+1 || source.EndLine != maxReadSourceLines || !source.Truncated || source.Content != strings.Repeat("line\r\n", maxReadSourceLines) {
		t.Fatalf("incorrect tool-bound source bytes or lines: %+v", source)
	}
	last := decodeSourceResult(t, callTool(t, tools, "read_source", fmt.Sprintf(`{"path":"sample.txt","start_line":%d}`, maxReadSourceLines+1)))
	if last.EndLine != maxReadSourceLines+1 || last.Content != "line\r\n" || last.Truncated {
		t.Fatalf("incorrect final page: %+v", last)
	}
}

// ---------------------------------------------------------------------------
// graph_neighbors
// ---------------------------------------------------------------------------

func TestGraphNeighborsReturnsMultipleEdgeTypes(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	result := callTool(t, tools, "graph_neighbors", `{"symbol":"AccountAPI","min_confidence":"Candidate"}`)
	if result["isError"] == true {
		t.Fatalf("unexpected error: %v", result)
	}
	raw, _ := json.Marshal(result["structuredContent"])
	var qr GraphQueryResult
	json.Unmarshal(raw, &qr)
	if qr.Tool != "graph_neighbors" {
		t.Fatalf("tool = %q, want graph_neighbors", qr.Tool)
	}
	edgeTypes := make(map[string]bool)
	for _, e := range qr.Edges {
		edgeTypes[e.Type] = true
	}
	if !edgeTypes["calls"] {
		t.Fatalf("expected calls edge, got types: %v", edgeTypes)
	}
	if !edgeTypes["imports"] {
		t.Fatalf("expected imports edge, got types: %v", edgeTypes)
	}
}

// ---------------------------------------------------------------------------
// list_symbols
// ---------------------------------------------------------------------------

func TestListSymbolsByQuery(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	result := callTool(t, tools, "list_symbols", `{"query":"Account"}`)
	if result["isError"] == true {
		t.Fatalf("unexpected error: %v", result)
	}
	raw, _ := json.Marshal(result["structuredContent"])
	var parsed struct {
		Symbols []symbolEntry `json:"symbols"`
		Total   int           `json:"total"`
	}
	json.Unmarshal(raw, &parsed)
	if parsed.Total == 0 {
		t.Fatal("expected symbols matching 'Account', got 0")
	}
	for _, s := range parsed.Symbols {
		if !strings.Contains(strings.ToLower(s.Name), "account") {
			t.Fatalf("symbol %q does not match query 'Account'", s.Name)
		}
	}
}

func TestListSymbolsRequiresFilter(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	result := callTool(t, tools, "list_symbols", `{}`)
	if result["isError"] != true {
		t.Fatalf("expected error for no filter, got: %v", result)
	}
}

func TestListSymbolsByRepo(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	result := callTool(t, tools, "list_symbols", `{"repo":"billing"}`)
	if result["isError"] == true {
		t.Fatalf("unexpected error: %v", result)
	}
	raw, _ := json.Marshal(result["structuredContent"])
	var parsed struct {
		Symbols []symbolEntry `json:"symbols"`
	}
	json.Unmarshal(raw, &parsed)
	for _, s := range parsed.Symbols {
		if s.Repo != "billing" {
			t.Fatalf("symbol %q in repo %q, want billing", s.Name, s.Repo)
		}
	}
}

// ---------------------------------------------------------------------------
// file_tree
// ---------------------------------------------------------------------------

func TestFileTreeReturnsFiles(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	result := callTool(t, tools, "file_tree", `{"repo":"accounts"}`)
	if result["isError"] == true {
		t.Fatalf("unexpected error: %v", result)
	}
	raw, _ := json.Marshal(result["structuredContent"])
	var ft fileTreeResult
	json.Unmarshal(raw, &ft)
	if ft.Total != 3 {
		t.Fatalf("total = %d, want 3 (api.go, store.go, model.go)", ft.Total)
	}
	if ft.Repo != "accounts" {
		t.Fatalf("repo = %q, want accounts", ft.Repo)
	}
}

func TestFileTreeUnknownRepo(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	result := callTool(t, tools, "file_tree", `{"repo":"nonexistent"}`)
	if result["isError"] != true {
		t.Fatalf("expected error for unknown repo, got: %v", result)
	}
}

func TestFileTreePrefix(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	// The fixture files have no subdirectory, so a prefix that doesn't match
	// should return 0 files.
	result := callTool(t, tools, "file_tree", `{"repo":"accounts","prefix":"src/"}`)
	raw, _ := json.Marshal(result["structuredContent"])
	var ft fileTreeResult
	json.Unmarshal(raw, &ft)
	if ft.Total != 0 {
		t.Fatalf("total = %d, want 0 for prefix 'src/' on flat repo", ft.Total)
	}
}

// ---------------------------------------------------------------------------
// Tool registration
// ---------------------------------------------------------------------------

func TestDiscoveryToolsRegistered(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	want := map[string]bool{
		"list_repos":      false,
		"graph_schema":    false,
		"read_source":     false,
		"graph_neighbors": false,
		"list_symbols":    false,
		"file_tree":       false,
	}
	for _, tool := range tools.Tools() {
		if _, ok := want[tool.Name()]; ok {
			want[tool.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("discovery tool %q not registered", name)
		}
	}
}
