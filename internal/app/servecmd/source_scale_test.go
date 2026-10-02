package servecmd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"moedex/internal/embed"
	"moedex/internal/mcp"
)

// This opt-in resource probe opens the same rank/source holder as the HTTP
// daemon. Run its compiled test binary under the external memory/time guard;
// Go heap readings alone do not measure mapped or total process memory.
func TestPinnedRoslynSourceScale(t *testing.T) {
	dir, output := os.Getenv("MOEDEX_SOURCE_SCALE_DIR"), os.Getenv("MOEDEX_SOURCE_SCALE_REPORT")
	if dir == "" || output == "" {
		t.Skip("set MOEDEX_SOURCE_SCALE_DIR and MOEDEX_SOURCE_SCALE_REPORT for the pinned resource gate")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("report must be a fresh path")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct{ Heads []struct{ Head string } }
	if err := json.Unmarshal(raw, &manifest); err != nil || len(manifest.Heads) != 1 || manifest.Heads[0].Head != "36d26c5466e4d25940657ccb8d5b9557ccaf7be1" {
		t.Fatal("not the pinned Roslyn corpus")
	}
	if _, err := os.Stat(filepath.Join(dir, "corpus-graph.graph")); !os.IsNotExist(err) {
		t.Fatal("source-only gate requires absent graph")
	}
	heap := func() uint64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	baseHeap := heap()
	started := time.Now()
	rank, _, err := openRankCorpus(context.Background(), dir, 20, "none", "", embed.ONNXOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rankTime := time.Since(started)
	rankHeap := heap()
	started = time.Now()
	sources, err := openServingGraph(dir)
	if err != nil {
		rank.Close()
		t.Fatal(err)
	}
	sourceTime := time.Since(started)
	holder, err := newServingHolder(rank, sources)
	if err != nil {
		closeServingComponents(sources, rank)
		t.Fatal(err)
	}
	defer holder.Close()
	openedHeap := heap()
	handler := mcp.NewServer(holder, mcp.WithTools(holder.Tools()...)).HTTPHandler()
	var calls []map[string]any
	call := func(name string, args map[string]any, unavailable bool) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": len(calls) + 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		start := time.Now()
		handler.ServeHTTP(rec, req)
		elapsed := time.Since(start)
		var wire struct {
			Error  any `json:"error"`
			Result struct {
				IsError    bool           `json:"isError"`
				Structured map[string]any `json:"structuredContent"`
			} `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil || rec.Code != 200 || wire.Error != nil || wire.Result.IsError != unavailable {
			t.Fatalf("%s failed: %s", name, rec.Body.String())
		}
		if unavailable && !strings.Contains(rec.Body.String(), "graph_unavailable") {
			t.Fatalf("wrong unavailable reason: %s", rec.Body.String())
		}
		calls = append(calls, map[string]any{"tool": name, "arguments": args, "seconds": elapsed.Seconds(), "bytes": rec.Body.Len(), "sha256": fmt.Sprintf("%x", sha256.Sum256(rec.Body.Bytes())), "expected_unavailable": unavailable})
		if err := os.WriteFile(output+fmt.Sprintf(".%02d.json", len(calls)), rec.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		return wire.Result.Structured
	}
	repos := call("list_repos", map[string]any{"filter": "roslyn"}, false)
	entries, ok := repos["repos"].([]any)
	if !ok || len(entries) != 1 || entries[0].(map[string]any)["files"].(float64) < 10000 {
		t.Fatalf("not a large corpus: %v", repos)
	}
	call("file_tree", map[string]any{"repo": "roslyn", "prefix": "src/Compilers/CSharp/Portable/Compilation/"}, false)
	source := call("read_source", map[string]any{"repo": "roslyn", "path": "src/Compilers/CSharp/Portable/Compilation/CSharpCompilation.cs", "start_line": 1, "end_line": 30}, false)
	if !strings.Contains(source["content"].(string), "Microsoft.CodeAnalysis") {
		t.Fatal("wrong source excerpt")
	}
	syms := call("list_symbols", map[string]any{"repo": "roslyn", "query": "CSharpCompilation"}, false)
	if syms["total"].(float64) == 0 {
		t.Fatal("missing CSharpCompilation symbols")
	}
	call("list_symbols", map[string]any{"repo": "roslyn", "query": "__moedex_absent_symbol_7dc894__"}, false)
	call("search_context", map[string]any{"repo": "roslyn", "query": "CSharpCompilation", "top_k": 2, "token_budget": 1500}, false)
	call("graph_schema", map[string]any{}, true)
	call("trace_calls", map[string]any{"symbol": "CSharpCompilation"}, true)
	finalHeap := heap()
	report := map[string]any{"classification": "pinned_source_only_serving_resource_gate_not_agent_evaluation", "rank_open_seconds": rankTime.Seconds(), "source_open_seconds": sourceTime.Seconds(), "base_heap_bytes": baseHeap, "rank_heap_bytes": rankHeap, "opened_heap_bytes": openedHeap, "queried_heap_bytes": finalHeap, "blobs": rank.NumBlobs(), "calls": calls}
	data, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(output, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
