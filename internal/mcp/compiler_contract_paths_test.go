package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"moedex/internal/semantic"
	"moedex/internal/semanticindex"
	"strings"
	"testing"
)

type pathsReaderFixture struct {
	compilerReaderFixture
	result semanticindex.ContractPathsResult
	err    error
	onCall func()
}

func (r *pathsReaderFixture) ContractPaths(context.Context, string, []string, int) (semanticindex.ContractPathsResult, error) {
	if r.onCall != nil {
		r.onCall()
	}
	return r.result, r.err
}
func wrapperReaderFixture() *pathsReaderFixture {
	old := compactFixture().result
	seed := old.Matches[0]
	seed.Owner.ID = seed.Owner.ComputeID()
	seed.Result.Binding.EnclosingSymbolID = seed.Owner.ID
	iface := *seed.Owner
	iface.Key.Descriptor = "M:IProducer.Send"
	iface.ID = iface.ComputeID()
	typ := *seed.Owner
	typ.Key.Descriptor = "T:Producer"
	typ.ID = typ.ComputeID()
	impl := seed.Result
	impl.Symbol = seed.Owner
	impl.Occurrence.Role = "declaration"
	impl.Occurrence.Kind = "declaration"
	impl.Binding.SymbolID = impl.Symbol.ID
	impl.Binding.ExtractorVersion = "6"
	impl.Context.Extractor = impl.Binding.Extractor
	impl.Context.ExtractorVersion = "6"
	impl.Context.Status = "complete"
	impl.Context.SnapshotID = impl.Snapshot.ID
	impl.Source.SnapshotID = impl.Snapshot.ID
	impl.Occurrence.SourceID = impl.Source.ID
	impl.Occurrence.ContextID = impl.Context.ID
	impl.Binding.OccurrenceID = impl.Occurrence.ID
	impl.Binding.DomainFacts = nil
	impl.DomainSymbols = nil
	impl.Binding.ImplementationFacts = []semantic.ImplementationFact{{Kind: "interface_method_implementation", Rule: "csharp-interface-v1", EvidenceScope: "compile_time", InterfaceSymbolID: iface.ID, ImplementingTypeSymbolID: typ.ID}}
	impl.ImplementationSymbols = []semantic.Symbol{iface, typ}
	caller := seed.Result
	caller.Symbol = &iface
	caller.Binding.DomainFacts = nil
	caller.DomainSymbols = nil
	caller.Binding.EnclosingSymbolID = "caller-owner"
	owner := iface
	owner.ID = "caller-owner"
	owner.Key.Descriptor = "M:Controller.Submit"
	return &pathsReaderFixture{result: semanticindex.ContractPathsResult{Supported: true, Contract: old.Contract, Seeds: []semanticindex.ContractImpactMatch{seed}, Paths: []semanticindex.ContractPath{{Seed: seed, Caller: caller, CallerOwner: owner, Implementation: &impl, ImplementationFactIndex: 0}}, Records: 4, RowsVisited: 3, WorkLimit: 10000}}
}
func pathPayload(t *testing.T, tool ToolHandler, args string) CompilerContractPathsResult {
	t.Helper()
	raw, e := tool.Call(context.Background(), json.RawMessage(args))
	if e != nil {
		t.Fatal(e)
	}
	validateFixture(t, tool.Specification().OutputSchema, raw["structuredContent"])
	b, _ := json.Marshal(raw["structuredContent"])
	var out CompilerContractPathsResult
	if e := json.Unmarshal(b, &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func TestCompilerContractPathsEvidenceAndLease(t *testing.T) {
	r := wrapperReaderFixture()
	p := &compilerProviderFixture{reader: r}
	tool := CompilerTools(p)[4]
	got := pathPayload(t, tool, compactArgs())
	if got.Status != "ok" || got.PathSemantics != "candidate_static" || len(got.Paths) != 1 || len(got.Seeds) != 1 || got.Records != 4 || !got.TotalIsExact || p.released != 1 {
		t.Fatalf("response %+v", got)
	}
	path := got.Paths[0]
	if path.Implementation == nil || path.Caller.Symbol.ID != path.Implementation.InterfaceMember.ID || path.Seed.Owner.ID != path.Implementation.Source.Symbol.ID || path.Caller.RawSHA256 == "" || len(path.Caller.DomainFacts) != 0 {
		t.Fatal("lost provenance or invented publisher")
	}
	r.result.PathsTruncated = true
	got = pathPayload(t, tool, compactArgs())
	if got.TotalIsExact || !got.PathsTruncated {
		t.Fatal("hidden truncation")
	}
	r.result.Supported = false
	got = pathPayload(t, tool, compactArgs())
	if got.Status != "index_upgrade_required" {
		t.Fatal(got.Status)
	}
}
func TestCompilerContractPathsInputsCapabilityAndErrors(t *testing.T) {
	r := wrapperReaderFixture()
	p := &compilerProviderFixture{reader: r}
	tool := CompilerTools(p)[4]
	for _, raw := range []string{`null`, `{}`, `{"symbol_id":"symbol:` + strings.Repeat("a", 64) + `"}`, strings.TrimSuffix(compactArgs(), "}") + `,"limit":null}`, strings.TrimSuffix(compactArgs(), "}") + `,"limit":101}`, strings.TrimSuffix(compactArgs(), "}") + `,"typo":1}`, compactArgs() + ` {}`} {
		if got := pathPayload(t, tool, raw); got.Status != "invalid_arguments" {
			t.Fatal(got.Status)
		}
	}
	if p.released != 0 {
		t.Fatal("invalid arguments acquired session")
	}
	if got := pathPayload(t, CompilerTools(nil)[4], compactArgs()); got.Status != "semantic_unavailable" {
		t.Fatal(got.Status)
	}
	if got := pathPayload(t, CompilerTools(&compilerProviderFixture{reader: &compilerReaderFixture{}})[4], compactArgs()); got.Status != "capability_unavailable" {
		t.Fatal(got.Status)
	}
	r.err = semanticindex.ErrContractContextSelection
	if got := pathPayload(t, tool, compactArgs()); got.Status != "invalid_context_selection" {
		t.Fatal(got.Status)
	}
	r.err = errors.New("failed")
	if _, e := tool.Call(context.Background(), json.RawMessage(compactArgs())); e == nil || p.released != 2 {
		t.Fatal("query failure leaked lease")
	}
	r.err = nil
	ctx, cancel := context.WithCancel(context.Background())
	r.onCall = cancel
	if _, e := tool.Call(ctx, json.RawMessage(compactArgs())); e != context.Canceled || p.released != 3 {
		t.Fatal("cancel failure leaked lease", e)
	}
}
func TestCompilerContractPathsRejectsInvalidJoins(t *testing.T) {
	for _, mutate := range []func(*pathsReaderFixture){
		func(r *pathsReaderFixture) { r.result.Paths[0].Caller.Context.ID = "unselected" },
		func(r *pathsReaderFixture) { r.result.Paths[0].Implementation.Context.ID = "other" },
		func(r *pathsReaderFixture) { r.result.Paths[0].Implementation = nil },
		func(r *pathsReaderFixture) { r.result.Paths[0].ImplementationFactIndex = 9 },
		func(r *pathsReaderFixture) { r.result.Paths[0].CallerOwner.ID = "wrong" },
		func(r *pathsReaderFixture) { r.result.Records = 2 },
		func(r *pathsReaderFixture) { r.result.RowsVisited = 10001 },
	} {
		r := wrapperReaderFixture()
		mutate(r)
		p := &compilerProviderFixture{reader: r}
		if _, e := CompilerTools(p)[4].Call(context.Background(), json.RawMessage(compactArgs())); e == nil || p.released != 1 {
			t.Fatal("invalid path admitted/lease leaked")
		}
	}
}
