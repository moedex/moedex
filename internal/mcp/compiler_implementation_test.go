package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"moedex/internal/semantic"
	"moedex/internal/semanticindex"
)

func implementationMCPFixture() semanticindex.Result {
	symbol := func(desc string) semantic.Symbol {
		s := semantic.Symbol{Key: semantic.SymbolKey{Language: "csharp", NamespaceKind: "project", Namespace: "repo/App.csproj", Descriptor: desc, DescriptorKind: "documentation_comment_id"}}
		s.ID = s.ComputeID()
		return s
	}
	impl, typ, iface := symbol("M:Worker.Run"), symbol("T:Worker"), symbol("M:IWorker.Run")
	return semanticindex.Result{
		Snapshot:   semantic.SourceSnapshot{ID: "snapshot", Repo: "repo"},
		Source:     semantic.Source{ID: "source", SnapshotID: "snapshot", Path: "Worker.cs", RawSHA256: strings.Repeat("a", 64), ByteSize: 100},
		Context:    semantic.BuildContext{ID: "context", SnapshotID: "snapshot", Project: "App.csproj", Status: "complete", Extractor: "msbuild-roslyn", ExtractorVersion: "6"},
		Occurrence: semantic.Occurrence{ID: "occurrence", SourceID: "source", ContextID: "context", Role: "declaration", Kind: "declaration", Offset: 10, Length: 3},
		Binding:    semantic.Binding{OccurrenceID: "occurrence", Status: "resolved", SymbolID: impl.ID, Method: "roslyn-semantic-model", Extractor: "msbuild-roslyn", ExtractorVersion: "6", ImplementationFacts: []semantic.ImplementationFact{{Kind: "interface_method_implementation", Rule: "csharp-interface-v1", EvidenceScope: "compile_time", InterfaceSymbolID: iface.ID, ImplementingTypeSymbolID: typ.ID}}},
		Symbol:     &impl, ImplementationSymbols: []semantic.Symbol{iface, typ},
	}
}
func implementationMCPArgs(tool int, r semanticindex.Result) string {
	if tool == 1 {
		return `{"symbol_id":"` + r.Symbol.ID + `","repo":"repo","context_id":"context"}`
	}
	return `{"repo":"repo","path":"Worker.cs","byte_offset":10,"context_id":"context","raw_sha256":"` + r.Source.RawSHA256 + `"}`
}
func implementationMCPProvider(r semanticindex.Result) *compilerProviderFixture {
	return &compilerProviderFixture{reader: &compilerReaderFixture{query: semanticindex.QueryResult{Results: []semanticindex.Result{r}}, contexts: []semanticindex.SourceContext{{Snapshot: r.Snapshot, Source: r.Source, Context: r.Context}}}}
}
func TestCompilerImplementationPublicBindingsAndDefinitions(t *testing.T) {
	r := implementationMCPFixture()
	// Two interface members share one implementing type. Input ordering must not
	// leak into the descriptor table or repeat the shared descriptor.
	second := r.ImplementationSymbols[0]
	second.Key.Descriptor = "M:ISecond.Run"
	second.ID = second.ComputeID()
	f := r.Binding.ImplementationFacts[0]
	f.InterfaceSymbolID = second.ID
	r.Binding.ImplementationFacts = append(r.Binding.ImplementationFacts, f)
	sort.Slice(r.Binding.ImplementationFacts, func(i, j int) bool {
		return r.Binding.ImplementationFacts[i].InterfaceSymbolID < r.Binding.ImplementationFacts[j].InterfaceSymbolID
	})
	r.ImplementationSymbols = append(r.ImplementationSymbols, second)
	var prior CompilerBinding
	for _, toolIndex := range []int{0, 1} {
		p := implementationMCPProvider(r)
		tool := CompilerTools(p)[toolIndex]
		out := compilerPayload(t, tool, implementationMCPArgs(toolIndex, r))
		if len(out.Results) != 1 || len(out.Results[0].ImplementationFacts) != 2 || len(out.Results[0].ImplementationSymbols) != 3 || p.released != 1 {
			t.Fatalf("missing relation or leaked lease: %+v", out)
		}
		b := out.Results[0]
		if b.SourceID != r.Source.ID || b.RawSHA256 != r.Source.RawSHA256 || b.ContextID != r.Context.ID || b.Symbol.ID != r.Symbol.ID {
			t.Fatal("lost source identity")
		}
		if !sort.SliceIsSorted(b.ImplementationSymbols, func(i, j int) bool { return b.ImplementationSymbols[i].ID < b.ImplementationSymbols[j].ID }) {
			t.Fatal("nondeterministic symbols")
		}
		if toolIndex == 1 && !reflect.DeepEqual(prior, b) {
			t.Fatal("definition and binding exposure differ")
		}
		prior = b
	}
}
func TestCompilerImplementationMalformedReaderFailsClosed(t *testing.T) {
	cases := map[string]func(*semanticindex.Result){
		"missing symbol": func(r *semanticindex.Result) { r.ImplementationSymbols = r.ImplementationSymbols[:1] },
		"duplicate symbol": func(r *semanticindex.Result) {
			r.ImplementationSymbols = append(r.ImplementationSymbols, r.ImplementationSymbols[0])
		},
		"conflicting symbol": func(r *semanticindex.Result) { r.ImplementationSymbols[0].Key.Descriptor = "M:IWrong.Run" },
		"extra symbol": func(r *semanticindex.Result) {
			s := r.ImplementationSymbols[0]
			s.Key.Descriptor = "M:IExtra.Run"
			s.ID = s.ComputeID()
			r.ImplementationSymbols = append(r.ImplementationSymbols, s)
		},
		"empty interface namespace": func(r *semanticindex.Result) {
			r.ImplementationSymbols[0].Key.Namespace = ""
			r.ImplementationSymbols[0].ID = r.ImplementationSymbols[0].ComputeID()
			r.Binding.ImplementationFacts[0].InterfaceSymbolID = r.ImplementationSymbols[0].ID
		},
		"NUL interface descriptor": func(r *semanticindex.Result) {
			r.ImplementationSymbols[0].Key.Descriptor = "M:I\x00.Run"
			r.ImplementationSymbols[0].ID = r.ImplementationSymbols[0].ComputeID()
			r.Binding.ImplementationFacts[0].InterfaceSymbolID = r.ImplementationSymbols[0].ID
		},
		"NUL implementation descriptor": func(r *semanticindex.Result) {
			r.Symbol.Key.Descriptor = "M:Worker.Run\x00"
			r.Symbol.ID = r.Symbol.ComputeID()
			r.Binding.SymbolID = r.Symbol.ID
		},
		"orphan symbols": func(r *semanticindex.Result) { r.Binding.ImplementationFacts = nil },
		"duplicate facts": func(r *semanticindex.Result) {
			r.Binding.ImplementationFacts = append(r.Binding.ImplementationFacts, r.Binding.ImplementationFacts[0])
		},
		"too many facts": func(r *semanticindex.Result) {
			f := r.Binding.ImplementationFacts[0]
			r.Binding.ImplementationFacts = make([]semantic.ImplementationFact, 33)
			for i := range r.Binding.ImplementationFacts {
				r.Binding.ImplementationFacts[i] = f
			}
		},
		"invocation":         func(r *semanticindex.Result) { r.Occurrence.Role = "reference"; r.Occurrence.Kind = "invocation" },
		"incomplete":         func(r *semanticindex.Result) { r.Context.Status = "incomplete" },
		"old worker":         func(r *semanticindex.Result) { r.Binding.ExtractorVersion = "5"; r.Context.ExtractorVersion = "5" },
		"wrong occurrence":   func(r *semanticindex.Result) { r.Binding.OccurrenceID = "other" },
		"wrong context":      func(r *semanticindex.Result) { r.Occurrence.ContextID = "other" },
		"wrong source":       func(r *semanticindex.Result) { r.Occurrence.SourceID = "other" },
		"wrong snapshot":     func(r *semanticindex.Result) { r.Source.SnapshotID = "other" },
		"wrong bound symbol": func(r *semanticindex.Result) { r.Binding.SymbolID = "other" },
		"other project": func(r *semanticindex.Result) {
			r.Symbol.Key.Namespace = "repo/Other.csproj"
			r.Symbol.ID = r.Symbol.ComputeID()
			r.Binding.SymbolID = r.Symbol.ID
			r.ImplementationSymbols[1].Key.Namespace = "repo/Other.csproj"
			r.ImplementationSymbols[1].ID = r.ImplementationSymbols[1].ComputeID()
			r.Binding.ImplementationFacts[0].ImplementingTypeSymbolID = r.ImplementationSymbols[1].ID
		},
		"oversized descriptors": func(r *semanticindex.Result) {
			r.ImplementationSymbols[0].Key.Descriptor = "M:" + strings.Repeat("A", semanticindex.MaxQueryBytes) + ".Run"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			for _, toolIndex := range []int{0, 1} {
				r := implementationMCPFixture()
				mutate(&r)
				p := implementationMCPProvider(r)
				_, err := CompilerTools(p)[toolIndex].Call(context.Background(), json.RawMessage(implementationMCPArgs(toolIndex, r)))
				if err == nil || p.released != 1 {
					t.Fatalf("invalid evidence accepted or lease leaked: %v", err)
				}
			}
		})
	}
}
func TestCompilerImplementationLegacyEmptyAndOwned(t *testing.T) {
	empty, err := compilerBinding(semanticindex.Result{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(empty)
	if strings.Contains(string(b), "implementation_") {
		t.Fatal("legacy record gained optional fields")
	}
	r := implementationMCPFixture()
	out, err := compilerBinding(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Binding.ImplementationFacts[0].Rule = "mutated"
	r.ImplementationSymbols[0].Key.Descriptor = "mutated"
	if out.ImplementationFacts[0].Rule != "csharp-interface-v1" || out.ImplementationSymbols[0].Descriptor == "mutated" {
		t.Fatal("response aliases reader slices")
	}
}
