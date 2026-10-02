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

type implementationsReaderFixture struct {
	compilerReaderFixture
	result semanticindex.ImplementationsResult
	err    error
	calls  int
	onCall func()
}

func (r *implementationsReaderFixture) Implementations(ctx context.Context, symbol string, contexts []string, limit int) (semanticindex.ImplementationsResult, error) {
	r.calls++
	if r.onCall != nil {
		r.onCall()
	}
	return r.result, r.err
}
func reverseImplementationFixture() *implementationsReaderFixture {
	r := implementationMCPFixture()
	r.Occurrence.ID = "occurrence:" + strings.Repeat("b", 64)
	r.Binding.OccurrenceID = r.Occurrence.ID
	r.Context.ID = "context:" + strings.Repeat("c", 64)
	r.Occurrence.ContextID = r.Context.ID
	r.Source.ID = "source:" + strings.Repeat("d", 64)
	r.Occurrence.SourceID = r.Source.ID
	r.Snapshot.ID = "snapshot:" + strings.Repeat("e", 64)
	r.Source.SnapshotID = r.Snapshot.ID
	r.Context.SnapshotID = r.Snapshot.ID
	iface := r.ImplementationSymbols[0]
	return &implementationsReaderFixture{result: semanticindex.ImplementationsResult{Supported: true, Interface: &iface, Contexts: []semanticindex.SourceContext{{Snapshot: r.Snapshot, Source: r.Source, Context: r.Context}}, Matches: []semanticindex.ImplementationMatch{{Result: r, ImplementationFactIndex: 0}}, RowsVisited: 1, WorkLimit: semanticindex.ContractWorkLimit}}
}
func implementationReverseArgs(r *implementationsReaderFixture, selected bool) string {
	if selected {
		return fmt.Sprintf(`{"symbol_id":%q,"context_ids":[%q]}`, r.result.Interface.ID, r.result.Contexts[0].Context.ID)
	}
	return fmt.Sprintf(`{"symbol_id":%q}`, r.result.Interface.ID)
}
func implementationReversePayload(t *testing.T, tool ToolHandler, args string) CompilerImplementationsResult {
	t.Helper()
	raw, err := tool.Call(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	validateFixture(t, tool.Specification().OutputSchema, raw["structuredContent"])
	b, _ := json.Marshal(raw["structuredContent"])
	var out CompilerImplementationsResult
	if json.Unmarshal(b, &out) != nil {
		t.Fatal("decode")
	}
	return out
}
func TestCompilerImplementationsDiscoveryAndSelectedEvidence(t *testing.T) {
	r := reverseImplementationFixture()
	p := &compilerProviderFixture{reader: r}
	tool := CompilerTools(p)[5]
	if tool.Name() != "compiler_implementations" {
		t.Fatal("tool catalog")
	}
	saved := r.result.Matches
	r.result.Matches = nil
	discovery := implementationReversePayload(t, tool, implementationReverseArgs(r, false))
	if discovery.Status != "context_required" || len(discovery.Contexts) != 1 || len(discovery.Matches) != 0 || discovery.TotalIsExact {
		t.Fatalf("discovery %+v", discovery)
	}
	r.result.Matches = saved
	got := implementationReversePayload(t, tool, implementationReverseArgs(r, true))
	if got.Status != "ok" || got.Count != 1 || !got.TotalIsExact || got.RowsVisited != 1 || got.WorkLimit != 10000 || got.MaterializationByteLimit != 2<<20 || len(got.Matches[0].Source.ImplementationSymbols) != 2 || p.released != 2 {
		t.Fatalf("selected %+v leases%d", got, p.released)
	}
	match := got.Matches[0]
	if match.Source.ImplementationFacts[match.ImplementationFactIndex].InterfaceSymbolID != got.Interface.ID || match.Source.RawSHA256 == "" {
		t.Fatal("lost source relationship")
	}
	r.result.Truncated = true
	got = implementationReversePayload(t, tool, implementationReverseArgs(r, true))
	if got.TotalIsExact || !got.Truncated {
		t.Fatal("hidden truncation")
	}
	r.result.Matches = nil
	r.result.Truncated = false
	got = implementationReversePayload(t, tool, implementationReverseArgs(r, true))
	if got.Status != "no_recorded_evidence" || got.Count != 0 || !got.TotalIsExact {
		t.Fatal("empty recorded subset")
	}
}
func TestCompilerImplementationsArgumentValidationBeforeLease(t *testing.T) {
	r := reverseImplementationFixture()
	p := &compilerProviderFixture{reader: r}
	tool := CompilerTools(p)[5]
	id := r.result.Interface.ID
	cases := []string{`null`, `{}`, `{"symbol_id":"Name"}`, fmt.Sprintf(`{"symbol_id":%q,"context_ids":[]}`, id), fmt.Sprintf(`{"symbol_id":%q,"context_ids":null}`, id), fmt.Sprintf(`{"symbol_id":%q,"context_ids":["x","x"]}`, id), fmt.Sprintf(`{"symbol_id":%q,"limit":null}`, id), fmt.Sprintf(`{"symbol_id":%q,"limit":101}`, id), fmt.Sprintf(`{"symbol_id":%q,"extra":1}`, id), fmt.Sprintf(`{"symbol_id":%q} {}`, id)}
	for _, args := range cases {
		out := implementationReversePayload(t, tool, args)
		if out.Status != "invalid_arguments" {
			t.Fatal(args, out)
		}
	}
	if p.acquired != 0 || r.calls != 0 {
		t.Fatal("invalid arguments acquired lease")
	}
}
func TestCompilerImplementationsLegacySelectionCancellationAndLease(t *testing.T) {
	r := reverseImplementationFixture()
	args := implementationReverseArgs(r, true)
	p := &compilerProviderFixture{reader: &compilerReaderFixture{}}
	out := implementationReversePayload(t, CompilerTools(p)[5], args)
	if out.Status != "capability_unavailable" || p.released != 1 {
		t.Fatal(out)
	}
	p = &compilerProviderFixture{reader: r}
	r.result.Supported = false
	out = implementationReversePayload(t, CompilerTools(p)[5], args)
	if out.Status != "index_upgrade_required" {
		t.Fatal(out)
	}
	r.result.Supported = true
	r.err = semanticindex.ErrContractContextSelection
	out = implementationReversePayload(t, CompilerTools(p)[5], args)
	if out.Status != "invalid_context_selection" {
		t.Fatal(out)
	}
	r.err = errors.New("reader failed")
	if _, err := CompilerTools(p)[5].Call(context.Background(), json.RawMessage(args)); err == nil {
		t.Fatal("reader error swallowed")
	}
	r.err = nil
	ctx, cancel := context.WithCancel(context.Background())
	r.onCall = cancel
	if _, err := CompilerTools(p)[5].Call(ctx, json.RawMessage(args)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if p.acquired != p.released {
		t.Fatal("lease leak")
	}
}
func TestCompilerImplementationsRejectMalformedReaderEvidence(t *testing.T) {
	cases := map[string]func(*implementationsReaderFixture){
		"nil discovery interface": func(r *implementationsReaderFixture) { r.result.Interface = nil },
		"wrong interface": func(r *implementationsReaderFixture) {
			s := *r.result.Interface
			s.Key.Descriptor = "M:IWrong.Run"
			s.ID = s.ComputeID()
			r.result.Interface = &s
		},
		"forged interface key":    func(r *implementationsReaderFixture) { r.result.Interface.Key.Descriptor = "M:IWrong.Run" },
		"unselected context":      func(r *implementationsReaderFixture) { r.result.Matches[0].Result.Context.ID = "elsewhere" },
		"wrong ordinal":           func(r *implementationsReaderFixture) { r.result.Matches[0].ImplementationFactIndex = 1 },
		"missing target":          func(r *implementationsReaderFixture) { r.result.Matches[0].Result.ImplementationSymbols = nil },
		"selected malformed hash": func(r *implementationsReaderFixture) { r.result.Matches[0].Result.Source.RawSHA256 = "bad" },
		"selected malformed source ID": func(r *implementationsReaderFixture) {
			r.result.Matches[0].Result.Source.ID = "bad"
			r.result.Matches[0].Result.Occurrence.SourceID = "bad"
		},
		"selected malformed occurrence ID": func(r *implementationsReaderFixture) {
			r.result.Matches[0].Result.Occurrence.ID = "bad"
			r.result.Matches[0].Result.Binding.OccurrenceID = "bad"
		},
		"selected without context provenance": func(r *implementationsReaderFixture) { r.result.Contexts = nil },
		"wrong project":                       func(r *implementationsReaderFixture) { r.result.Matches[0].Result.Context.Project = "Other.csproj" },
		"duplicate evidence": func(r *implementationsReaderFixture) {
			r.result.Matches = append(r.result.Matches, r.result.Matches[0])
		},
		"duplicate contexts": func(r *implementationsReaderFixture) {
			r.result.Contexts = append(r.result.Contexts, r.result.Contexts[0])
		},
		"bad discovery hash": func(r *implementationsReaderFixture) { r.result.Contexts[0].Source.RawSHA256 = "bad" },
		"excessive work":     func(r *implementationsReaderFixture) { r.result.RowsVisited = 10001 },
		"false work bound":   func(r *implementationsReaderFixture) { r.result.WorkLimit = 20000 },
		"excessive matches": func(r *implementationsReaderFixture) {
			m := r.result.Matches[0]
			r.result.Matches = make([]semanticindex.ImplementationMatch, 101)
			for i := range r.result.Matches {
				r.result.Matches[i] = m
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := reverseImplementationFixture()
			args := implementationReverseArgs(r, true)
			change(r)
			p := &compilerProviderFixture{reader: r}
			if _, err := CompilerTools(p)[5].Call(context.Background(), json.RawMessage(args)); err == nil || p.released != 1 {
				t.Fatal("malformed evidence accepted or lease leaked", err)
			}
		})
	}
	r := reverseImplementationFixture()
	p := &compilerProviderFixture{reader: r}
	if _, err := CompilerTools(p)[5].Call(context.Background(), json.RawMessage(implementationReverseArgs(r, false))); err == nil || !strings.Contains(err.Error(), "discovery") {
		t.Fatal("discovery exposed matches", err)
	}
}

func TestCompilerImplementationsContextVariantsDiscoveryIsNotSelection(t *testing.T) {
	r := reverseImplementationFixture()
	second := r.result.Contexts[0]
	second.Context.ID = "context:" + strings.Repeat("f", 64)
	r.result.Contexts = append(r.result.Contexts, second)
	r.result.Matches = nil
	p := &compilerProviderFixture{reader: r}
	tool := CompilerTools(p)[5]
	discovery := implementationReversePayload(t, tool, implementationReverseArgs(r, false))
	if len(discovery.Contexts) != 2 || discovery.Status != "context_required" {
		t.Fatal("variants not discoverable", discovery)
	}
	args := fmt.Sprintf(`{"symbol_id":%q,"context_ids":[%q,%q]}`, r.result.Interface.ID, r.result.Contexts[0].Context.ID, second.Context.ID)
	if _, err := tool.Call(context.Background(), json.RawMessage(args)); err == nil || p.released != 2 {
		t.Fatal("combined incompatible variants or leaked lease", err)
	}
}

func TestCompilerImplementationsWorkerVersionCompatibility(t *testing.T) {
	for _, version := range []string{"6", "18", "19", "20", "21", "22"} {
		t.Run(version, func(t *testing.T) {
			reader := reverseImplementationFixture()
			reader.result.Contexts[0].Context.ExtractorVersion = version
			reader.result.Matches[0].Result.Context.ExtractorVersion = version
			reader.result.Matches[0].Result.Binding.ExtractorVersion = version
			tool := CompilerTools(&compilerProviderFixture{reader: reader})[5]
			raw, err := tool.Call(context.Background(), json.RawMessage(implementationReverseArgs(reader, true)))
			if version == "22" {
				if err == nil {
					t.Fatal("future worker admitted", raw)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			validateFixture(t, tool.Specification().OutputSchema, raw["structuredContent"])
		})
	}
}
