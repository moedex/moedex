package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"moedex/internal/semanticindex"
)

type contractContextReaderFixture struct {
	compilerReaderFixture
	result semanticindex.ContractContextResult
	err    error
	calls  int
	onCall func()
}

func (r *contractContextReaderFixture) ContractContext(context.Context, string, []string, int, int) (semanticindex.ContractContextResult, error) {
	r.calls++
	if r.onCall != nil {
		r.onCall()
	}
	return r.result, r.err
}
func compactFixture() *contractContextReaderFixture {
	legacy := contractFixture().result
	id := "context:" + strings.Repeat("c", 64)
	legacy.Contexts[0].Context.ID = id
	legacy.Matches[0].Result.Context.ID = id
	definition := legacy.Matches[0].Result
	definition.Symbol = legacy.Contract
	definition.Occurrence.Role = "declaration"
	definition.Occurrence.Kind = "declaration"
	definition.Occurrence.ID = "declaration"
	definition.Binding.DomainFacts = nil
	return &contractContextReaderFixture{result: semanticindex.ContractContextResult{Supported: true, Contract: legacy.Contract, Contexts: legacy.Contexts, Matches: legacy.Matches, Definitions: []semanticindex.Result{definition}, PostingsVisited: 1, DefinitionRowsVisited: 1, WorkLimit: 10000}}
}
func compactArgs() string {
	return fmt.Sprintf(`{"symbol_id":"symbol:%s","context_ids":["context:%s"]}`, strings.Repeat("a", 64), strings.Repeat("c", 64))
}
func compactPayload(t *testing.T, tool ToolHandler, args string) CompilerContractContextResult {
	t.Helper()
	response, err := tool.Call(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	wire := response["structuredContent"]
	validateFixture(t, tool.Specification().OutputSchema, wire)
	data, _ := json.Marshal(wire)
	var out CompilerContractContextResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestCompilerContractContextGroupsAndDefinitionEnrichment(t *testing.T) {
	reader := compactFixture()
	provider := &compilerProviderFixture{reader: reader}
	tool := CompilerTools(provider)[3]
	if tool.Name() != "compiler_contract_context" {
		t.Fatal("registration")
	}
	out := compactPayload(t, tool, compactArgs())
	if out.Status != "ok" || out.Count != 1 || len(out.Groups) != 1 || len(out.Definitions) != 1 || !out.TotalIsExact || len(out.Symbols) != 3 || provider.released != 1 || reader.calls != 1 {
		t.Fatalf("result %+v released%d", out, provider.released)
	}
	e := out.Groups[0].Evidence[0]
	if e.Source.RawSHA256 == "" || e.Source.ByteOffset != 12 || e.APISymbolID != "symbol:api" || out.Groups[0].OwnerID != "symbol:owner" || e.Fact.Targets[0].SymbolID != out.ContractID || e.Source.ExtractorVersion != "2" || len(out.Limitations) != 3 {
		t.Fatal("source/identity lost")
	}
	reader.result.EvidenceTruncated = true
	reader.result.DefinitionsTruncated = true
	out = compactPayload(t, tool, compactArgs())
	if out.TotalIsExact || !out.EvidenceTruncated || !out.DefinitionsTruncated {
		t.Fatal("truncation hidden")
	}
}
func TestCompilerContractContextDiscoveryAndUnavailable(t *testing.T) {
	reader := compactFixture()
	reader.result.Matches = nil
	reader.result.Definitions = nil
	reader.result.ContextsTruncated = true
	provider := &compilerProviderFixture{reader: reader}
	args := `{"symbol_id":"symbol:` + strings.Repeat("a", 64) + `"}`
	out := compactPayload(t, CompilerTools(provider)[3], args)
	if out.Status != "context_required" || len(out.Groups) != 0 || len(out.Definitions) != 0 || len(out.Contexts) != 1 || !out.ContextsTruncated || out.TotalIsExact {
		t.Fatalf("discovery %+v", out)
	}
	out = compactPayload(t, CompilerTools(nil)[3], args)
	if out.Status != "semantic_unavailable" {
		t.Fatal(out.Status)
	}
	legacy := &compilerProviderFixture{reader: &compilerReaderFixture{}}
	out = compactPayload(t, CompilerTools(legacy)[3], args)
	if out.Status != "capability_unavailable" || legacy.released != 1 {
		t.Fatal(out.Status)
	}
	reader.result.Supported = false
	out = compactPayload(t, CompilerTools(provider)[3], args)
	if out.Status != "index_upgrade_required" {
		t.Fatal(out.Status)
	}
}
func TestCompilerContractContextInvalidAndLeaseErrors(t *testing.T) {
	reader := compactFixture()
	provider := &compilerProviderFixture{reader: reader}
	tool := CompilerTools(provider)[3]
	for _, args := range []string{`null`, `{}`, `{"symbol_id":"symbol:bad"}`, `{"symbol_id":"symbol:` + strings.Repeat("a", 64) + `","context_ids":[]}`, strings.TrimSuffix(compactArgs(), "}") + `,"evidence_limit":81}`, strings.TrimSuffix(compactArgs(), "}") + `,"definition_limit":null}`, compactArgs() + ` {}`, strings.TrimSuffix(compactArgs(), "}") + `,"typo":1}`} {
		if out := compactPayload(t, tool, args); out.Status != "invalid_arguments" {
			t.Fatal(args, out.Status)
		}
	}
	if reader.calls != 0 || provider.released != 0 {
		t.Fatal("invalid input acquired")
	}
	reader.err = semanticindex.ErrContractContextSelection
	if out := compactPayload(t, tool, compactArgs()); out.Status != "invalid_context_selection" || provider.released != 1 {
		t.Fatal(out.Status)
	}
	reader.err = errors.New("query failed")
	if _, err := tool.Call(context.Background(), json.RawMessage(compactArgs())); err == nil || provider.released != 2 {
		t.Fatal("failed query leaked lease")
	}
	ctx, cancel := context.WithCancel(context.Background())
	reader.err = nil
	reader.onCall = cancel
	if _, err := tool.Call(ctx, json.RawMessage(compactArgs())); err != context.Canceled || provider.released != 3 {
		t.Fatal("cancelled query leaked lease", err)
	}
	failed := &contractFailedProvider{}
	if _, err := CompilerTools(failed)[3].Call(context.Background(), json.RawMessage(compactArgs())); err == nil || failed.released != 1 {
		t.Fatal("acquire failure leaked lease")
	}
}
func TestCompilerContractContextRejectsInconsistentReader(t *testing.T) {
	for _, mutate := range []func(*contractContextReaderFixture){
		func(r *contractContextReaderFixture) { r.result.Matches[0].TargetRoles = []string{"wrong"} },
		func(r *contractContextReaderFixture) { r.result.Matches[0].Result.Context.ID = "unselected" },
		func(r *contractContextReaderFixture) { r.result.Matches[0].Owner = nil },
		func(r *contractContextReaderFixture) { r.result.Contract = nil },
		func(r *contractContextReaderFixture) { r.result.Definitions[0].Occurrence.Role = "reference" },
		func(r *contractContextReaderFixture) { r.result.DefinitionRowsVisited = 10000 },
	} {
		r := compactFixture()
		mutate(r)
		p := &compilerProviderFixture{reader: r}
		if _, err := CompilerTools(p)[3].Call(context.Background(), json.RawMessage(compactArgs())); err == nil || p.released != 1 {
			t.Fatal("inconsistent result admitted")
		}
	}
}
