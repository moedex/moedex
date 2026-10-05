package semanticimport_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/mcp"
	"moedex/internal/semantic"
	"moedex/internal/semanticindex"
)

func TestPublicCompilerCallTracing(t *testing.T) {
	path := os.Getenv("MOEDEX_CALL_ARTIFACT")
	if path == "" {
		t.Skip("set MOEDEX_CALL_ARTIFACT using tools/semantic-dotnet/test_call_tracing.py")
	}
	a, err := semantic.Read(path, semantic.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sha := fmt.Sprintf("%x", sha256.Sum256(raw))
	p := semanticindex.Provenance{ArtifactSHA256: sha, CorpusFingerprint: strings.Repeat("b", 64)}
	ip := filepath.Join(t.TempDir(), "index")
	if err := semanticindex.Build(ip, a, p); err != nil {
		t.Fatal(err)
	}
	x, err := semanticindex.Open(ip, p, semanticindex.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	tools := map[string]mcp.ToolHandler{}
	for _, tool := range mcp.CompilerTools(&domainGoldProvider{index: x, artifact: sha}) {
		tools[tool.Name()] = tool
	}
	call := func(name string, args any, into any) {
		t.Helper()
		raw, _ := json.Marshal(args)
		out, err := tools[name].Call(context.Background(), raw)
		if err != nil || out["isError"] == true {
			t.Fatal(out, err)
		}
		wire, _ := json.Marshal(out["structuredContent"])
		if err := json.Unmarshal(wire, into); err != nil {
			t.Fatal(err)
		}
	}
	var declarations mcp.CompilerSymbolsResult
	call("compiler_symbols", map[string]any{"repo": "Fixture", "path": "Flow.cs", "limit": 100}, &declarations)
	if declarations.Status != "ok" || declarations.Truncated {
		t.Fatal(declarations)
	}
	symbols := map[string]string{}
	var contextID string
	for _, r := range declarations.Results {
		symbols[r.Symbol.Descriptor] = r.Symbol.ID
		contextID = r.ContextID
	}
	trace := func(descriptor, direction string, depth int) mcp.CompilerCallTraceResult {
		t.Helper()
		id := symbols[descriptor]
		if id == "" {
			t.Fatal("missing declaration", descriptor)
		}
		var out mcp.CompilerCallTraceResult
		call("compiler_trace_calls", map[string]any{"symbol_id": id, "context_ids": []string{contextID}, "direction": direction, "depth": depth, "limit": 100}, &out)
		if out.Truncated || out.ArtifactSHA256 != sha {
			t.Fatal(out)
		}
		return out
	}
	entry := trace("M:Controller.Entry(IService)", "outbound", 8)
	if len(entry.Edges) != 1 || entry.Edges[0].Witness.Symbol.Descriptor != "M:IService.Send" {
		t.Fatal("interface target inferred dispatch", entry)
	}
	impact := trace("M:IService.Send", "inbound", 8)
	if len(impact.Edges) != 1 || impact.Edges[0].Caller.Descriptor != "M:Controller.Entry(IService)" {
		t.Fatal("wrong static callers", impact)
	}
	one := trace("M:One.Send", "both", 8)
	if len(one.Edges) != 2 {
		t.Fatal("cycle or method-group joined", one)
	}
	for _, e := range one.Edges {
		if !strings.HasPrefix(e.Caller.Descriptor, "M:One.") || !strings.HasPrefix(e.Witness.Symbol.Descriptor, "M:One.") || e.Witness.BindingStatus != "resolved" || e.Witness.RawSHA256 == "" || e.Witness.Method != "roslyn-semantic-model" || e.Commit != a.Snapshots[0].Commit {
			t.Fatal("lost provenance or homonym", e)
		}
	}
	two := trace("M:Two.Send", "inbound", 8)
	if len(two.Edges) != 1 || two.Edges[0].Caller.Descriptor != "M:Controller.Other(Two)" {
		t.Fatal("same-name caller joined", two)
	}
	overload := trace("M:One.Send(System.Int32)", "inbound", 8)
	if len(overload.Edges) != 0 {
		t.Fatal("overload joined", overload)
	}
	references := trace("M:Controller.References(One)", "outbound", 8)
	if len(references.Edges) != 0 {
		t.Fatal("method group or constructor promoted", references)
	}
}
