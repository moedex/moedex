package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"moedex/internal/semantic"
	"moedex/internal/semanticindex"
)

type contractReaderFixture struct {
	compilerReaderFixture
	result   semanticindex.ContractImpactResult
	err      error
	calls    int
	symbol   string
	contexts []string
	limit    int
	onCall   func()
}

type contractFailedProvider struct{ released int }

func (p *contractFailedProvider) AcquireCompilerSession(context.Context) (CompilerSession, error) {
	return CompilerSession{Release: func() { p.released++ }}, errors.New("acquire failed")
}

func (r *contractReaderFixture) ContractImpact(_ context.Context, symbol string, contexts []string, limit int) (semanticindex.ContractImpactResult, error) {
	r.calls++
	r.symbol, r.contexts, r.limit = symbol, append([]string(nil), contexts...), limit
	if r.onCall != nil {
		r.onCall()
	}
	return r.result, r.err
}
func contractFixture() *contractReaderFixture {
	target := semantic.Symbol{ID: "symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Key: semantic.SymbolKey{Language: "csharp", NamespaceKind: "project", Namespace: "repo/Contracts.csproj", DescriptorKind: "documentation_comment_id", Descriptor: "T:Contracts.Notice"}}
	api := semantic.Symbol{ID: "symbol:api", Key: semantic.SymbolKey{Language: "csharp", NamespaceKind: "assembly", Namespace: "MassTransit.Abstractions", DescriptorKind: "documentation_comment_id", Descriptor: "M:MassTransit.IPublishEndpoint.Publish"}}
	owner := semantic.Symbol{ID: "symbol:owner", Key: semantic.SymbolKey{Language: "csharp", NamespaceKind: "project", Namespace: "repo/App.csproj", DescriptorKind: "documentation_comment_id", Descriptor: "M:Producer.Send"}}
	source := semanticindex.SourceContext{Snapshot: semantic.SourceSnapshot{ID: "snapshot", Repo: "repo"}, Context: semantic.BuildContext{ID: "context", Project: "App.csproj"}, Source: semantic.Source{ID: "source", Path: "Producer.cs", RawSHA256: strings.Repeat("a", 64), Generated: true}}
	fact := semantic.DomainFact{Kind: "message_publish", Rule: "csharp-framework-v1", EvidenceScope: "compile_time", Targets: []semantic.DomainTarget{{Role: "message", SymbolID: target.ID}}}
	result := semanticindex.Result{Snapshot: source.Snapshot, Context: source.Context, Source: source.Source, Occurrence: semantic.Occurrence{ID: "occurrence", Role: "reference", Kind: "invocation", Offset: 12, Length: 7}, Binding: semantic.Binding{Status: "resolved", EnclosingSymbolID: owner.ID, Method: "roslyn-semantic-model", Extractor: "msbuild-roslyn", ExtractorVersion: "2", DomainFacts: []semantic.DomainFact{fact}}, Symbol: &api, DomainSymbols: []semantic.Symbol{target}}
	return &contractReaderFixture{result: semanticindex.ContractImpactResult{Supported: true, Contract: &target, Contexts: []semanticindex.SourceContext{source}, Matches: []semanticindex.ContractImpactMatch{{Result: result, DomainFactIndex: 0, TargetRoles: []string{"message"}, Owner: &owner}}, PostingsVisited: 1, WorkLimit: 10000}}
}

func contractPayload(t *testing.T, tool ToolHandler, args string) CompilerContractResult {
	t.Helper()
	response, err := tool.Call(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	wire := response["structuredContent"]
	validateFixture(t, tool.Specification().OutputSchema, wire)
	data, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var out CompilerContractResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCompilerContractDiscoveryRequiresSelection(t *testing.T) {
	reader := contractFixture()
	reader.result.Truncated = true
	provider := &compilerProviderFixture{reader: reader}
	tools := CompilerTools(provider)
	if len(tools) != 8 || tools[2].Name() != "compiler_contract_impact" {
		t.Fatal("tool registration missing")
	}
	out := contractPayload(t, tools[2], `{"symbol_id":"symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","limit":1}`)
	if out.Status != "context_required" || !out.Truncated || out.TotalIsExact || len(out.Contexts) != 1 || len(out.Paths) != 0 || out.Count != 0 {
		t.Fatalf("discovery leaked unscoped evidence: %+v", out)
	}
	if reader.limit != 1 || reader.symbol != "symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || len(reader.contexts) != 0 {
		t.Fatal("query arguments changed")
	}
	if out.Contexts[0].RawSHA256 == "" || out.Contract.Descriptor != "T:Contracts.Notice" || out.ArtifactSHA256 != "audit" || out.SnapshotID != "generation" {
		t.Fatal("discovery lost identity")
	}
	reader.result.Contexts = nil
	out = contractPayload(t, tools[2], `{"symbol_id":"symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	if out.Status != "context_required" || len(out.Paths) != 0 {
		t.Fatal("missing coverage interpreted as impact")
	}
	if provider.acquired != provider.released {
		t.Fatal("discovery leaked lease")
	}
}

func TestCompilerContractPathsRetainProvenanceAndBounds(t *testing.T) {
	reader := contractFixture()
	provider := &compilerProviderFixture{reader: reader}
	tool := CompilerTools(provider)[2]
	out := contractPayload(t, tool, `{"symbol_id":"symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","context_ids":["context"],"limit":1}`)
	if out.Status != "ok" || len(out.Paths) != 1 || out.Count != 1 || !out.TotalIsExact || out.EvidenceScope != "compile_time" || len(out.Contexts) != 0 {
		t.Fatalf("paths=%+v", out)
	}
	path := out.Paths[0]
	if path.Contract.Descriptor != "T:Contracts.Notice" || path.Fact.Targets[0].Symbol.ID != path.Contract.ID || !reflect.DeepEqual(path.MatchedRoles, []string{"message"}) || path.Owner.Descriptor != "M:Producer.Send" {
		t.Fatalf("path identities=%+v", path)
	}
	if path.Source.Symbol.ID != "symbol:api" || path.Source.ContextID != "context" || path.Source.RawSHA256 != strings.Repeat("a", 64) || path.Source.ByteOffset != 12 || path.Source.SourceSnapshotID != "snapshot" || !path.Source.Generated {
		t.Fatal("lost raw source/API/context identity")
	}
	if out.PostingsVisited != 1 || out.WorkLimit != 10000 || out.MaterializationByteLimit != semanticindex.MaxQueryBytes {
		t.Fatal("lost work accounting")
	}
	reader.result.Truncated, reader.result.PostingsVisited = true, 10000
	reader.result.Matches[0].Owner = nil
	out = contractPayload(t, tool, `{"symbol_id":"symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","context_ids":["context"]}`)
	if !out.Truncated || out.TotalIsExact || out.Count != 1 || out.Paths[0].Owner != nil {
		t.Fatal("bounded or absent-owner semantics changed")
	}
	reader.result.Truncated, reader.result.Matches = false, nil
	out = contractPayload(t, tool, `{"symbol_id":"symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","context_ids":["context"]}`)
	if out.Status != "no_recorded_evidence" || out.Count != 0 || !out.TotalIsExact {
		t.Fatal(out)
	}
	if provider.acquired != provider.released {
		t.Fatal("path query leaked lease")
	}
}

func TestCompilerContractCapabilityAndLegacyStatus(t *testing.T) {
	for _, tc := range []struct {
		reader CompilerReader
		status string
	}{
		{nil, "semantic_unavailable"},
		{&compilerReaderFixture{}, "capability_unavailable"},
		{&contractReaderFixture{}, "index_upgrade_required"},
	} {
		provider := &compilerProviderFixture{reader: tc.reader}
		out := contractPayload(t, CompilerTools(provider)[2], `{"symbol_id":"symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
		if out.Status != tc.status || len(out.Paths) != 0 || out.TotalIsExact {
			t.Fatal(out)
		}
		if provider.acquired != 1 || provider.released != 1 {
			t.Fatal("unavailable branch leaked lease")
		}
	}
	out := contractPayload(t, CompilerTools(nil)[2], `{"symbol_id":"symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	if out.Status != "semantic_unavailable" {
		t.Fatal(out)
	}
}

func TestCompilerContractInvalidArgumentsAndContextErrors(t *testing.T) {
	reader := contractFixture()
	provider := &compilerProviderFixture{reader: reader}
	tool := CompilerTools(provider)[2]
	args := []string{`null`, `[]`, `{}`, `{"symbol_id":"symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","context_ids":[]}`, `{"symbol_id":"symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","context_ids":null}`, `{"symbol_id":"symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","context_ids":["a","a"]}`, `{"symbol_id":"symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","context_ids":[""]}`, `{"symbol_id":"symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","limit":0}`, `{"symbol_id":"symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","limit":101}`, `{"symbol_id":"symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","limit":null}`, `{"symbol_id":"symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repo":"r"}`, `{"symbol_id":"symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}{}`}
	many := make([]string, 33)
	for i := range many {
		many[i] = fmt.Sprint(i)
	}
	data, _ := json.Marshal(map[string]any{"symbol_id": reader.result.Contract.ID, "context_ids": many})
	args = append(args, string(data))
	args = append(args, `{"symbol_id":"x"}`, `{"symbol_id":"symbol:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`)
	for _, arg := range args {
		if out := contractPayload(t, tool, arg); out.Status != "invalid_arguments" {
			t.Fatalf("%s: %+v", arg, out)
		}
	}
	if reader.calls != 0 || provider.acquired != 0 {
		t.Fatal("invalid arguments acquired snapshot")
	}
	reader.err = fmt.Errorf("%w: incompatible selected variants", semanticindex.ErrContractContextSelection)
	out := contractPayload(t, tool, `{"symbol_id":"symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","context_ids":["context"]}`)
	if out.Status != "invalid_context_selection" || out.Error == nil || len(out.Paths) != 0 || provider.released != 1 {
		t.Fatal(out)
	}
}

func TestCompilerContractCancellationErrorsAndInvalidReader(t *testing.T) {
	reader := contractFixture()
	provider := &compilerProviderFixture{reader: reader}
	tool := CompilerTools(provider)[2]
	args := json.RawMessage(`{"symbol_id":"symbol:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","context_ids":["context"]}`)
	failed := &contractFailedProvider{}
	if response, err := CompilerTools(failed)[2].Call(context.Background(), args); err == nil || response != nil || failed.released != 1 {
		t.Fatal("acquisition failure lost returned lease", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Call(canceled, args); !errors.Is(err, context.Canceled) || provider.acquired != 0 {
		t.Fatal("pre-cancel acquired a snapshot", err)
	}
	active, stop := context.WithCancel(context.Background())
	reader.onCall = stop
	if _, err := tool.Call(active, args); !errors.Is(err, context.Canceled) || provider.released != 1 {
		t.Fatal("in-query cancel lost lease", err)
	}
	reader.onCall = nil
	reader.err = errors.New("reader failure")
	if _, err := tool.Call(context.Background(), args); !errors.Is(err, reader.err) || provider.released != 2 {
		t.Fatal("query error lost lease", err)
	}
	for _, mutate := range []func(*contractReaderFixture){
		func(r *contractReaderFixture) { r.result.Matches[0].Result.Context.ID = "unselected" },
		func(r *contractReaderFixture) { r.result.Matches[0].DomainFactIndex = 8 },
		func(r *contractReaderFixture) { r.result.Matches[0].TargetRoles = []string{"implementation"} },
		func(r *contractReaderFixture) { r.result.Matches[0].Owner.ID = "other" },
	} {
		broken := contractFixture()
		mutate(broken)
		p := &compilerProviderFixture{reader: broken}
		response, err := CompilerTools(p)[2].Call(context.Background(), args)
		if err == nil || response != nil || p.released != 1 {
			t.Fatal("invalid capability result escaped or leaked lease", err)
		}
	}
}

func TestCompilerContractSchemaAndScopeDescription(t *testing.T) {
	spec := CompilerTools(nil)[2].Specification()
	props := spec.InputSchema["properties"].(map[string]interface{})
	contexts := props["context_ids"].(map[string]interface{})
	if contexts["maxItems"] != 32 || contexts["uniqueItems"] != true || contexts["minItems"] != 1 {
		t.Fatal(contexts)
	}
	for _, text := range []string{"domain target's symbol.id", "compile-time", "not a serialized response-size ceiling", "No runtime", "missing evidence is not dependency absence"} {
		if !strings.Contains(spec.Description, text) {
			t.Fatalf("description missing %q", text)
		}
	}
}
