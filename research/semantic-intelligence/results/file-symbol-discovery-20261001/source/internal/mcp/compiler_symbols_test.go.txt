package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"moedex/internal/semantic"
	"moedex/internal/semanticindex"
)

type symbolsReaderFixture struct {
	compilerReaderFixture
	discovery semanticindex.SymbolDiscoveryResult
	err       error
}

func (f *symbolsReaderFixture) FileSymbols(ctx context.Context, repo, path, query string, limit int) (semanticindex.SymbolDiscoveryResult, error) {
	return f.discovery, f.err
}
func symbolsPayload(t *testing.T, tool ToolHandler, args string) CompilerSymbolsResult {
	t.Helper()
	value, err := tool.Call(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	validateFixture(t, tool.Specification().OutputSchema, value["structuredContent"])
	raw, _ := json.Marshal(value["structuredContent"])
	var out CompilerSymbolsResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestCompilerSymbolsValidationAvailabilityAndLease(t *testing.T) {
	reader := &symbolsReaderFixture{}
	p := &compilerProviderFixture{reader: reader}
	tool := CompilerTools(p)[6]
	for _, args := range []string{`null`, `{}`, `{"repo":"repo","path":"../x"}`, `{"repo":"repo","path":"x","limit":null}`, `{"repo":"repo","path":"x","query":null}`, `{"repo":"repo","path":"x","context_id":"c"}`, `{"repo":"repo","path":"x","limit":101}`, `{"repo":"repo","path":"x"} {}`} {
		if out := symbolsPayload(t, tool, args); out.Status != "invalid_arguments" {
			t.Fatal(out)
		}
	}
	if p.acquired != 0 {
		t.Fatal("invalid input acquired session")
	}
	args := `{"repo":"repo","path":"x"}`
	if out := symbolsPayload(t, CompilerTools(nil)[6], args); out.Status != "semantic_unavailable" {
		t.Fatal(out)
	}
	if out := symbolsPayload(t, CompilerTools(&compilerProviderFixture{reader: &compilerReaderFixture{}})[6], args); out.Status != "capability_unavailable" {
		t.Fatal(out)
	}
	reader.discovery.Truncated = true
	reader.discovery.RowsVisited = semanticindex.ContractWorkLimit
	out := symbolsPayload(t, tool, args)
	if out.Status != "no_recorded_declaration" || !out.Truncated || out.RowsVisited != out.WorkLimit {
		t.Fatal(out)
	}
	reader.err = errors.New("read failed")
	if _, err := tool.Call(context.Background(), json.RawMessage(args)); err == nil {
		t.Fatal("error swallowed")
	}
	if p.acquired != p.released {
		t.Fatal("lease leak")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Call(ctx, json.RawMessage(args)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestCompilerSymbolsPreservesAlternativesAndRejectsScopeLeak(t *testing.T) {
	reader := &symbolsReaderFixture{}
	for _, id := range []string{"one", "two"} {
		reader.discovery.Results = append(reader.discovery.Results, semanticindex.Result{Snapshot: semantic.SourceSnapshot{Repo: "repo"}, Source: semantic.Source{ID: "source", Path: "x", RawSHA256: "raw"}, Context: semantic.BuildContext{ID: id, Project: "p"}, Occurrence: semantic.Occurrence{Role: "declaration"}, Binding: semantic.Binding{Status: "resolved", SymbolID: "symbol"}, Symbol: &semantic.Symbol{ID: "symbol", Key: semantic.SymbolKey{Descriptor: "T:A"}}})
	}
	reader.discovery.RowsVisited = 2
	p := &compilerProviderFixture{reader: reader}
	tool := CompilerTools(p)[6]
	args := `{"repo":"repo","path":"x","query":"A"}`
	out := symbolsPayload(t, tool, args)
	if out.Status != "ok" || len(out.Contexts) != 2 || len(out.Results) != 2 || out.SnapshotID != "generation" {
		t.Fatal(out)
	}
	reader.discovery.Results[1].Source.Path = "other"
	if _, err := tool.Call(context.Background(), json.RawMessage(args)); err == nil {
		t.Fatal("scope leak accepted")
	}
	if p.acquired != p.released {
		t.Fatal("lease leak")
	}
}
