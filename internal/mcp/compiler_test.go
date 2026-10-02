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

type compilerReaderFixture struct {
	contexts       []semanticindex.SourceContext
	query          semanticindex.QueryResult
	discoveryLimit int
}

func (f *compilerReaderFixture) Bindings(ctx context.Context, p semanticindex.Position, n int) (semanticindex.QueryResult, error) {
	return f.query, ctx.Err()
}
func (f *compilerReaderFixture) Definitions(ctx context.Context, id string, filter semanticindex.Filter, n int) (semanticindex.QueryResult, error) {
	return f.query, ctx.Err()
}
func (f *compilerReaderFixture) SourceContexts(ctx context.Context, repo, path string, n int) ([]semanticindex.SourceContext, bool, error) {
	f.discoveryLimit = n
	if len(f.contexts) > n {
		return f.contexts[:n], true, ctx.Err()
	}
	return f.contexts, false, ctx.Err()
}
func (f *compilerReaderFixture) SourceContext(ctx context.Context, repo, path, id string) (semanticindex.SourceContext, bool, error) {
	for _, c := range f.contexts {
		if c.Context.ID == id {
			return c, true, ctx.Err()
		}
	}
	return semanticindex.SourceContext{}, false, ctx.Err()
}

type compilerProviderFixture struct {
	reader             CompilerReader
	acquired, released int
}

func (f *compilerProviderFixture) AcquireCompilerSession(ctx context.Context) (CompilerSession, error) {
	f.acquired++
	return CompilerSession{Reader: f.reader, SnapshotID: "generation", ArtifactSHA256: "audit", Release: func() { f.released++ }}, ctx.Err()
}
func compilerPayload(t *testing.T, tool ToolHandler, args string) CompilerResult {
	t.Helper()
	result, err := tool.Call(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	wire := result["structuredContent"]
	validateFixture(t, tool.Specification().OutputSchema, wire)
	b, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var out CompilerResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestCompilerToolContextSelectionAndEmptyPosition(t *testing.T) {
	hash := strings.Repeat("a", 64)
	reader := &compilerReaderFixture{}
	for i := 0; i < 101; i++ {
		id := "other"
		if i == 100 {
			id = "selected"
		}
		reader.contexts = append(reader.contexts, semanticindex.SourceContext{Context: semantic.BuildContext{ID: id, Project: "Project.csproj"}, Snapshot: semantic.SourceSnapshot{Repo: "repo"}, Source: semantic.Source{Path: "file.cs", RawSHA256: hash, Generated: true}})
	}
	provider := &compilerProviderFixture{reader: reader}
	tool := CompilerTools(provider)[0]
	out := compilerPayload(t, tool, `{"repo":"repo","path":"file.cs","byte_offset":42,"limit":1}`)
	if out.Status != "context_required" || len(out.Contexts) != 1 || !out.Truncated || !out.Contexts[0].Generated || reader.discoveryLimit != 1 {
		t.Fatalf("discovery=%+v", out)
	}
	out = compilerPayload(t, tool, `{"repo":"repo","path":"file.cs","byte_offset":42,"context_id":"selected","raw_sha256":"`+hash+`"}`)
	if out.Status != "no_binding" {
		t.Fatalf("selected context beyond discovery bound=%+v", out)
	}
	out = compilerPayload(t, tool, `{"repo":"repo","path":"file.cs","byte_offset":42,"context_id":"selected","raw_sha256":"`+strings.Repeat("b", 64)+`"}`)
	if out.Status != "source_mismatch" {
		t.Fatalf("mismatch=%+v", out)
	}
	if provider.acquired != provider.released {
		t.Fatalf("lease leaked: %+v", provider)
	}
}

func TestCompilerToolMissingCoverageIsNotContextSelection(t *testing.T) {
	provider := &compilerProviderFixture{reader: &compilerReaderFixture{}}
	out := compilerPayload(t, CompilerTools(provider)[0], `{"repo":"repo","path":"not-captured.cs","byte_offset":0}`)
	if out.Status != "source_not_found" || len(out.Contexts) != 0 || len(out.Results) != 0 {
		t.Fatalf("missing coverage presented as a binding or selectable context: %+v", out)
	}
	if provider.acquired != 1 || provider.released != 1 {
		t.Fatalf("missing coverage leaked lease: %+v", provider)
	}
}
func TestCompilerToolsPreserveBindingAlternatives(t *testing.T) {
	hash := strings.Repeat("a", 64)
	c := semanticindex.SourceContext{Context: semantic.BuildContext{ID: "context"}, Snapshot: semantic.SourceSnapshot{Repo: "repo"}, Source: semantic.Source{Path: "file.cs", RawSHA256: hash, Generated: true}}
	reader := &compilerReaderFixture{contexts: []semanticindex.SourceContext{c}}
	for _, status := range []string{"resolved", "ambiguous", "unresolved", "unsupported"} {
		reader.query.Results = append(reader.query.Results, semanticindex.Result{Snapshot: c.Snapshot, Context: c.Context, Source: c.Source, Occurrence: semantic.Occurrence{Offset: 3, Length: 4}, Binding: semantic.Binding{Status: status, EnclosingSymbolID: "owner", Method: "roslyn-semantic-model"}, Candidates: []semantic.Symbol{{ID: "candidate", Key: semantic.SymbolKey{Descriptor: "M:C.F(System.Int32)"}}}})
	}
	provider := &compilerProviderFixture{reader: reader}
	out := compilerPayload(t, CompilerTools(provider)[0], `{"repo":"repo","path":"file.cs","byte_offset":3,"context_id":"context","raw_sha256":"`+hash+`"}`)
	if len(out.Results) != 4 {
		t.Fatal(out)
	}
	for i, r := range out.Results {
		if r.BindingStatus != reader.query.Results[i].Binding.Status || len(r.Candidates) != 1 || !r.Generated || r.ByteOffset != 3 || r.EnclosingSymbolID != "owner" {
			t.Fatalf("fact=%+v", r)
		}
	}
}
func TestCompilerToolsUnavailableCancellationAndInvalidArgs(t *testing.T) {
	provider := &compilerProviderFixture{}
	tools := CompilerTools(provider)
	if out := compilerPayload(t, tools[1], `{"symbol_id":"symbol"}`); out.Status != "semantic_unavailable" {
		t.Fatal(out)
	}
	if out := compilerPayload(t, tools[0], `{"repo":"repo","path":"../file.cs","byte_offset":0}`); out.Status != "invalid_arguments" {
		t.Fatal(out)
	}
	if out := compilerPayload(t, tools[0], `{"repo":"repo","path":"file.cs","byte_offset":0,"context_id":"context"}`); out.Status != "invalid_arguments" {
		t.Fatal(out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tools[1].Call(ctx, json.RawMessage(`{"symbol_id":"symbol"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if provider.acquired != 1 || provider.released != 1 {
		t.Fatalf("unexpected acquisition: %+v", provider)
	}
}
