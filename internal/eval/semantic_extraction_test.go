package eval_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"moedex/internal/diskstore"
	evaluation "moedex/internal/eval"
	"moedex/internal/graph"
	graphbuild "moedex/internal/graph/build"
	graphserve "moedex/internal/graph/serve"
	"moedex/internal/index"
	"moedex/internal/mcp"
)

const semanticFixture = "testdata/semantic-intelligence/v1"

type semanticWireNode struct {
	ID        string                     `json:"id"`
	Symbol    string                     `json:"symbol"`
	Locations []graphserve.GraphLocation `json:"locations"`
}
type semanticWireEdge struct {
	Source     string `json:"source"`
	Target     string `json:"target"`
	Type       string `json:"type"`
	Confidence struct {
		Tier string `json:"tier"`
	} `json:"confidence"`
	Evidence graph.Evidence `json:"evidence"`
}
type semanticWireResult struct {
	Nodes []semanticWireNode `json:"nodes"`
	Edges []semanticWireEdge `json:"edges"`
}

// This supplements the hand-built adjacency gate: no graph edge is inserted by
// this test. Both fixtures and mutations pass through the production extractor,
// persisted graph, online toolset, and HTTP MCP dispatch.
func TestSemanticExtractionThroughMCP(t *testing.T) {
	suite, err := evaluation.LoadSemanticSuite(os.DirFS(semanticFixture))
	if err != nil {
		t.Fatal(err)
	}
	contents := map[string][]byte{}
	for _, name := range suite.Sources {
		contents[name], err = os.ReadFile(filepath.Join(semanticFixture, name))
		if err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	writeSemanticShard(t, dir, contents)
	if _, _, err := graphbuild.BuildGraph(dir); err != nil {
		t.Fatal(err)
	}
	tools, err := graphserve.OpenGraphTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer tools.Close()
	handler := mcp.NewServer(nil, mcp.WithTools(tools.Tools()...)).HTTPHandler()
	for _, task := range suite.Tasks {
		t.Run(task.ID, func(t *testing.T) {
			result, snapshot := callSemanticTask(t, handler, task, "Pattern")
			if !snapshot.Cacheable || snapshot.GraphBuildID == "" || snapshot.GraphGeneration == 0 {
				t.Fatalf("unanchored graph result: %+v", snapshot)
			}
			checkSemanticFacts(t, task, result, contents)
			verified, _ := callSemanticTask(t, handler, task, "Verified")
			// This fixture has syntax evidence, not compiler/manifest resolution.
			for _, edge := range verified.Edges {
				if edge.Type == "extends" || edge.Type == "implements" || edge.Type == "injects" {
					t.Errorf("syntax-only edge passed Verified floor: %+v", edge)
				}
			}
			candidates, _ := callSemanticTask(t, handler, task, "Candidate")
			keys := semanticEdgeKeys(candidates)
			for _, forbidden := range task.Forbidden {
				if len(keys[factKey(forbidden)]) == 0 {
					t.Errorf("missing explicit diagnostic Candidate: %s", factKey(forbidden))
				}
				for _, edge := range keys[factKey(forbidden)] {
					if edge.Confidence.Tier != "Candidate" {
						t.Errorf("foreign collision overstated: %+v", edge)
					}
				}
			}
		})
	}

	// Delete a declaration and remove the registration, preserving the query
	// roots. Refresh must agree with a clean rebuild, including absence of stale
	// inheritance and injection edges. File deletion is covered by the distractor.
	before, oldSnapshot := callSemanticTask(t, handler, suite.Tasks[0], "Pattern")
	if len(before.Edges) == 0 {
		t.Fatal("vacuous refresh fixture")
	}
	contents["orders/Contracts.cs"] = []byte("namespace Orders;\npublic interface IOrderStore { }\npublic class OrderStore : IOrderStore { }\npublic class OrderHandler { }\n")
	contents["orders/Startup.cs"] = []byte("namespace Orders;\npublic class Startup {\n    public void Configure() { }\n}\n")
	delete(contents, "unrelated/Contracts.cs")
	writeSemanticShard(t, dir, contents)
	if _, _, err := graphbuild.RefreshGraph(dir); err != nil {
		t.Fatal(err)
	}
	if openErr, closeErr := tools.Reload(dir); openErr != nil || closeErr != nil {
		t.Fatalf("reload: %v / %v", openErr, closeErr)
	}
	cleanDir := t.TempDir()
	writeSemanticShard(t, cleanDir, contents)
	if _, _, err := graphbuild.BuildGraph(cleanDir); err != nil {
		t.Fatal(err)
	}
	cleanTools, err := graphserve.OpenGraphTools(cleanDir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanTools.Close()
	cleanHandler := mcp.NewServer(nil, mcp.WithTools(cleanTools.Tools()...)).HTTPHandler()
	for _, task := range suite.Tasks {
		refreshed, snapshot := callSemanticTask(t, handler, task, "Candidate")
		clean, _ := callSemanticTask(t, cleanHandler, task, "Candidate")
		if snapshot.GraphBuildID == oldSnapshot.GraphBuildID || snapshot.GraphGeneration <= oldSnapshot.GraphGeneration {
			t.Fatalf("refresh did not advance identity: %+v", snapshot)
		}
		if !reflect.DeepEqual(normalizedSemanticEdges(refreshed), normalizedSemanticEdges(clean)) {
			t.Fatalf("refresh differs from clean build: %+v / %+v", refreshed, clean)
		}
		for _, edge := range refreshed.Edges {
			if edge.Type == "extends" || edge.Type == "injects" {
				t.Errorf("removed relationship survived refresh: %+v", edge)
			}
		}
	}
	// A clean rebuild and refresh must also preserve an independently labeled
	// positive, so two equally empty or over-deleted results cannot pass parity.
	surviving := evaluation.SemanticTask{Operation: "hierarchy", Symbol: "OrderStore", Required: []evaluation.SemanticFact{{
		Source: "orders/Contracts.cs#OrderStore", Relation: "implements", Target: "orders/Contracts.cs#IOrderStore",
		Evidence: &evaluation.SemanticEvidence{File: "orders/Contracts.cs", Line: 3, Text: "IOrderStore"},
	}}}
	for _, h := range []http.Handler{handler, cleanHandler} {
		result, _ := callSemanticTask(t, h, surviving, "Pattern")
		checkSemanticFacts(t, surviving, result, contents)
	}
}

func writeSemanticShard(t testing.TB, dir string, contents map[string][]byte) {
	t.Helper()
	ix := index.New()
	paths := make([]string, 0, len(contents))
	for path := range contents {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		repo, rel, _ := strings.Cut(path, "/")
		data := contents[path]
		ix.AddFile(repo, rel, filepath.Join(dir, path), diskstore.GitBlobSHA1(data), data)
	}
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}
}

func callSemanticTask(t testing.TB, handler http.Handler, task evaluation.SemanticTask, floor string) (semanticWireResult, mcp.SnapshotIdentity) {
	t.Helper()
	tool := "graph_neighbors"
	if task.Operation == "hierarchy" {
		tool = "trace_hierarchy"
	}
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": map[string]any{"symbol": task.Symbol, "hops": 1, "min_confidence": floor}}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(request))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var response struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError    bool                       `json:"isError"`
			Structured semanticWireResult         `json:"structuredContent"`
			Meta       map[string]json.RawMessage `json:"_meta"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != 200 || response.Result.IsError || len(response.Error) > 0 {
		t.Fatalf("MCP %s: HTTP %d, decode %v: %s", tool, rec.Code, err, rec.Body.String())
	}
	var snapshot mcp.SnapshotIdentity
	if err := json.Unmarshal(response.Result.Meta[mcp.SnapshotMetaKey], &snapshot); err != nil {
		t.Fatal(err)
	}
	return response.Result.Structured, snapshot
}

func semanticEdgeKeys(result semanticWireResult) map[string][]semanticWireEdge {
	nodes := map[string][]string{}
	for _, node := range result.Nodes {
		for _, location := range node.Locations {
			nodes[node.ID] = append(nodes[node.ID], location.Repo+"/"+location.Path+"#"+node.Symbol)
		}
	}
	out := map[string][]semanticWireEdge{}
	for _, edge := range result.Edges {
		for _, source := range nodes[edge.Source] {
			for _, target := range nodes[edge.Target] {
				key := source + "|" + edge.Type + "|" + target
				out[key] = append(out[key], edge)
			}
		}
	}
	return out
}

func factKey(f evaluation.SemanticFact) string { return f.Source + "|" + f.Relation + "|" + f.Target }

func checkSemanticFacts(t testing.TB, task evaluation.SemanticTask, result semanticWireResult, contents map[string][]byte) {
	t.Helper()
	keys := semanticEdgeKeys(result)
	for _, fact := range task.Forbidden {
		if len(keys[factKey(fact)]) != 0 {
			t.Errorf("distractor returned above Candidate: %s", factKey(fact))
		}
	}
	for _, fact := range task.Required {
		edges := keys[factKey(fact)]
		if len(edges) == 0 {
			t.Errorf("missing source-authored fact %s; got %+v", factKey(fact), keys)
			continue
		}
		evidence := fact.Evidence
		content := contents[evidence.File]
		lines := bytes.Split(content, []byte("\n"))
		offset := 0
		for i := 0; i < evidence.Line-1; i++ {
			offset += len(lines[i]) + 1
		}
		offset += bytes.Index(lines[evidence.Line-1], []byte(evidence.Text))
		matched := false
		for _, edge := range edges {
			if edge.Confidence.Tier != "Pattern" {
				t.Errorf("syntax fact tier = %s, want Pattern", edge.Confidence.Tier)
			}
			if edge.Evidence.BlobSHA == diskstore.GitBlobSHA1(content) && edge.Evidence.ByteOffset == uint64(offset) && edge.Evidence.ByteLength == uint64(len(evidence.Text)) {
				matched = true
			}
		}
		if !matched {
			t.Errorf("fact %s lacks exact labeled evidence at byte %d: %+v", factKey(fact), offset, edges)
		}
	}
}

func normalizedSemanticEdges(result semanticWireResult) []string {
	out := []string{}
	for key, edges := range semanticEdgeKeys(result) {
		for _, edge := range edges {
			evidence, _ := json.Marshal(edge.Evidence)
			out = append(out, key+"|"+edge.Confidence.Tier+"|"+string(evidence))
		}
	}
	sort.Strings(out)
	return out
}

func TestSemanticTaskSchemaRejectsInvalidLabels(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(semanticFixture, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(string) string{
		"version":        func(s string) string { return strings.Replace(s, `"version": 1`, `"version": 2`, 1) },
		"unknown-field":  func(s string) string { return strings.Replace(s, `"version": 1`, `"surprise": true, "version": 1`, 1) },
		"stale-evidence": func(s string) string { return strings.Replace(s, `"line": 5`, `"line": 999`, 1) },
		"missing-target": func(s string) string {
			return strings.Replace(s, `orders/Contracts.cs#OrderBase`, `missing/Contracts.cs#OrderBase`, 1)
		},
		"duplicate-id": func(s string) string {
			return strings.Replace(s, `di-local-with-foreign-collision`, `inheritance-local-with-foreign-collision`, 1)
		},
		"trailing-object": func(s string) string { return s + `{}` },
	} {
		t.Run(name, func(t *testing.T) {
			fixture := fstest.MapFS{"tasks.json": {Data: []byte(mutate(string(data)))}}
			for _, path := range []string{"orders/Contracts.cs", "orders/Startup.cs", "unrelated/Contracts.cs"} {
				content, err := os.ReadFile(filepath.Join(semanticFixture, path))
				if err != nil {
					t.Fatal(err)
				}
				fixture[path] = &fstest.MapFile{Data: content}
			}
			if _, err := evaluation.LoadSemanticSuite(fixture); err == nil {
				t.Fatal("invalid labels accepted")
			}
		})
	}
}

// Measures a complete full graph build/open plus both MCP queries over an
// already ingested source shard. This tiny fixture is a regression workload,
// not a throughput or competitor-performance claim.
func BenchmarkSemanticExtractionMCP(b *testing.B) {
	suite, err := evaluation.LoadSemanticSuite(os.DirFS(semanticFixture))
	if err != nil {
		b.Fatal(err)
	}
	contents := map[string][]byte{}
	for _, name := range suite.Sources {
		contents[name], err = os.ReadFile(filepath.Join(semanticFixture, name))
		if err != nil {
			b.Fatal(err)
		}
	}
	dir := b.TempDir()
	writeSemanticShard(b, dir, contents)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := graphbuild.BuildGraph(dir); err != nil {
			b.Fatal(err)
		}
		tools, err := graphserve.OpenGraphTools(dir)
		if err != nil {
			b.Fatal(err)
		}
		handler := mcp.NewServer(nil, mcp.WithTools(tools.Tools()...)).HTTPHandler()
		for _, task := range suite.Tasks {
			result, _ := callSemanticTask(b, handler, task, "Pattern")
			checkSemanticFacts(b, task, result, contents)
		}
		if err := tools.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
