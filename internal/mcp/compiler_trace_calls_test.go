package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"moedex/internal/semantic"
	"moedex/internal/semanticindex"
)

type callTraceReaderFixture struct {
	compilerReaderFixture
	result semanticindex.CallTraceResult
	err    error
	onCall func()
}

func (r *callTraceReaderFixture) TraceCalls(context.Context, string, []string, string, int, int) (semanticindex.CallTraceResult, error) {
	if r.onCall != nil {
		r.onCall()
	}
	return r.result, r.err
}
func callTraceMCPFixture() (*callTraceReaderFixture, json.RawMessage) {
	snapshot := semantic.SourceSnapshot{Repo: "fixture", Commit: strings.Repeat("1", 40), InputFingerprint: strings.Repeat("a", 64)}
	snapshot.ID = snapshot.ComputeID()
	source := semantic.Source{SnapshotID: snapshot.ID, Path: "Flow.cs", RawSHA256: strings.Repeat("b", 64), ByteSize: 100}
	source.ID = source.ComputeID()
	c := semantic.BuildContext{SnapshotID: snapshot.ID, Project: "App.csproj", Extractor: "msbuild-roslyn", ExtractorVersion: "21", Status: "complete", InputFingerprint: strings.Repeat("c", 64)}
	c.ID = c.ComputeID()
	mk := func(d string) semantic.Symbol {
		s := semantic.Symbol{Key: semantic.SymbolKey{Language: "csharp", NamespaceKind: "project", Namespace: "fixture/App.csproj", DescriptorKind: "documentation_comment_id", Descriptor: d}}
		s.ID = s.ComputeID()
		return s
	}
	caller, target := mk("M:Controller.Submit"), mk("M:IService.Send")
	o := semantic.Occurrence{SourceID: source.ID, ContextID: c.ID, Role: "reference", Kind: "invocation", Offset: 10, Length: 4}
	o.ID = o.ComputeID()
	b := semantic.Binding{OccurrenceID: o.ID, Status: "resolved", SymbolID: target.ID, EnclosingSymbolID: caller.ID, Method: "roslyn-semantic-model", Extractor: c.Extractor, ExtractorVersion: c.ExtractorVersion}
	b.ID = b.ComputeID()
	r := semanticindex.Result{Snapshot: snapshot, Context: c, Source: source, Occurrence: o, Binding: b, Symbol: &target}
	reader := &callTraceReaderFixture{result: semanticindex.CallTraceResult{Supported: true, Root: &caller, Edges: []semanticindex.CallEdge{{Caller: caller, Target: target, Witness: r, Depth: 1}}, RowsVisited: 1}}
	args, _ := json.Marshal(map[string]any{"symbol_id": caller.ID, "context_ids": []string{c.ID}})
	return reader, args
}
func TestCompilerCallTraceLeaseSchemaAndStaticTarget(t *testing.T) {
	r, args := callTraceMCPFixture()
	p := &compilerProviderFixture{reader: r}
	tool := CompilerTools(p)[8]
	got, e := tool.Call(context.Background(), args)
	if e != nil {
		t.Fatal(e)
	}
	validateFixture(t, tool.Specification().OutputSchema, got["structuredContent"])
	q := got["structuredContent"].(CompilerCallTraceResult)
	if q.Status != "ok" || q.EvidenceScope != "compile_time" || q.Edges[0].Witness.Symbol.Descriptor != "M:IService.Send" || q.Edges[0].Commit != strings.Repeat("1", 40) || p.acquired != 1 || p.released != 1 {
		t.Fatal(q, p)
	}
	r.result.Edges = nil
	r.result.RowsVisited = 0
	got, e = tool.Call(context.Background(), args)
	if e != nil || got["structuredContent"].(CompilerCallTraceResult).Status != "no_recorded_calls" {
		t.Fatal(got, e)
	}
	r.result.Supported = false
	got, e = tool.Call(context.Background(), args)
	if e != nil || got["structuredContent"].(CompilerCallTraceResult).Status != "index_upgrade_required" {
		t.Fatal(got, e)
	}
}
func TestCompilerCallTraceStrictArgumentsAndUnavailable(t *testing.T) {
	_, args := callTraceMCPFixture()
	for _, extra := range []string{`"depth":null`, `"limit":101`, `"direction":"sideways"`, `"unknown":1`, `"depth":0`} {
		bad := append(append(json.RawMessage(nil), args[:len(args)-1]...), []byte(","+extra+"}")...)
		p := &compilerProviderFixture{}
		got, e := CompilerTools(p)[8].Call(context.Background(), bad)
		if e != nil || got["structuredContent"].(CompilerCallTraceResult).Status != "invalid_arguments" || p.acquired != 0 {
			t.Fatal(got, e)
		}
	}
	for _, raw := range []string{`null`, `{}`, string(args) + " {}"} {
		got, e := CompilerTools(nil)[8].Call(context.Background(), json.RawMessage(raw))
		if e != nil || got["structuredContent"].(CompilerCallTraceResult).Status != "invalid_arguments" {
			t.Fatal(got, e)
		}
	}
	got, e := CompilerTools(nil)[8].Call(context.Background(), args)
	if e != nil || got["structuredContent"].(CompilerCallTraceResult).Status != "semantic_unavailable" {
		t.Fatal(got, e)
	}
	got, e = CompilerTools(&compilerProviderFixture{reader: &compilerReaderFixture{}})[8].Call(context.Background(), args)
	if e != nil || got["structuredContent"].(CompilerCallTraceResult).Status != "index_upgrade_required" {
		t.Fatal(got, e)
	}
}
func TestCompilerCallTraceRejectsMalformedProviderAndReleases(t *testing.T) {
	for _, change := range []func(*callTraceReaderFixture){
		func(r *callTraceReaderFixture) { r.result.Edges[0].Witness.Source.RawSHA256 = "bad" },
		func(r *callTraceReaderFixture) { r.result.Edges[0].Witness.Context.ID = "wrong" },
		func(r *callTraceReaderFixture) { r.result.Edges[0].Witness.Occurrence.Kind = "name" },
		func(r *callTraceReaderFixture) { r.result.Edges[0].Depth = 2 },
		func(r *callTraceReaderFixture) { r.result.Edges[0].Caller = r.result.Edges[0].Target },
		func(r *callTraceReaderFixture) { r.result.Edges = append(r.result.Edges, r.result.Edges[0]) },
		func(r *callTraceReaderFixture) { r.result.RowsVisited = 10001 },
		func(r *callTraceReaderFixture) { r.result.Root = nil },
		func(r *callTraceReaderFixture) { r.err = errors.New("failure") },
	} {
		r, args := callTraceMCPFixture()
		change(r)
		p := &compilerProviderFixture{reader: r}
		if _, e := CompilerTools(p)[8].Call(context.Background(), args); e == nil || p.released != 1 {
			t.Fatal("malformed evidence admitted", e)
		}
	}
	r, args := callTraceMCPFixture()
	ctx, cancel := context.WithCancel(context.Background())
	r.onCall = cancel
	p := &compilerProviderFixture{reader: r}
	if _, e := CompilerTools(p)[8].Call(ctx, args); e != context.Canceled || p.released != 1 {
		t.Fatal(e)
	}
}
func TestCompilerCallTraceSerializedCapTrimsWholeWitnesses(t *testing.T) {
	r, args := callTraceMCPFixture()
	e := &r.result.Edges[0]
	e.Target.Key.Descriptor = "M:Remote." + strings.Repeat("\\\"", 40000)
	e.Target.ID = e.Target.ComputeID()
	e.Witness.Symbol = &e.Target
	e.Witness.Binding.SymbolID = e.Target.ID
	p := &compilerProviderFixture{reader: r}
	got, err := CompilerTools(p)[8].Call(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(got)
	q := got["structuredContent"].(CompilerCallTraceResult)
	if len(wire) > CallTraceResponseBytes || !q.Truncated || len(q.Edges) != 0 {
		t.Fatal(len(wire), q)
	}
}
