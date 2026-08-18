package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
)

// buildDiscoveryFixture creates a temp shard dir with two repos, a few files,
// and a small graph — enough to exercise all six discovery tools.
func buildDiscoveryFixture(t *testing.T) *GraphToolset {
	t.Helper()
	dir := t.TempDir()
	ix := index.New()

	type blob struct {
		repo, file, sha string
		content          []byte
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

// ---------------------------------------------------------------------------
// graph_neighbors
// ---------------------------------------------------------------------------

func TestGraphNeighborsReturnsMultipleEdgeTypes(t *testing.T) {
	tools := buildDiscoveryFixture(t)
	result := callTool(t, tools, "graph_neighbors", `{"symbol":"AccountAPI"}`)
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
