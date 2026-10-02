package mcp

import (
	"encoding/json"
	"testing"

	"moedex/internal/semantic"
	"moedex/internal/semanticindex"
)

func defaultSelectionMCPFixture() semanticindex.Result {
	r := implementationMCPFixture()
	typ := r.ImplementationSymbols[1]
	template := typ
	template.Key.Descriptor = "M:IDefault`1.IWorker#Run"
	template.ID = template.ComputeID()
	raw, _ := json.Marshal(map[string]any{"definition": template.Key.Descriptor, "arguments": []semantic.SymbolKey{typ.Key}})
	closed := template
	closed.Key.DescriptorKind = "constructed_interface_method_v1"
	closed.Key.Descriptor = string(raw)
	closed.ID = closed.ComputeID()
	r.Context.ExtractorVersion = "8"
	r.Binding.ExtractorVersion = "8"
	r.Symbol = &typ
	r.Binding.SymbolID = typ.ID
	r.Binding.ImplementationFacts[0].Kind = "interface_default_selection"
	r.Binding.ImplementationFacts[0].Rule = "csharp-interface-selection-v1"
	r.Binding.ImplementationFacts[0].SelectedDefaultSymbolID = closed.ID
	r.Binding.ImplementationFacts[0].DefaultTemplateSymbolID = template.ID
	r.ImplementationSymbols = append(r.ImplementationSymbols, closed, template)
	return r
}

func TestCompilerDefaultSelectionBoundary(t *testing.T) {
	r := defaultSelectionMCPFixture()
	b, err := compilerBinding(r)
	if err != nil || len(b.ImplementationSymbols) != 4 || b.Symbol.ID != b.ImplementationFacts[0].ImplementingTypeSymbolID {
		t.Fatalf("%+v %v", b, err)
	}
	for name, change := range map[string]func(*semanticindex.Result){
		"missing body":  func(r *semanticindex.Result) { r.ImplementationSymbols = r.ImplementationSymbols[:3] },
		"wrong context": func(r *semanticindex.Result) { r.Context.ExtractorVersion = "7"; r.Binding.ExtractorVersion = "7" },
		"method anchor": func(r *semanticindex.Result) {
			s := *r.Symbol
			s.Key.Descriptor = "M:Worker.Run"
			s.ID = s.ComputeID()
			r.Symbol = &s
			r.Binding.SymbolID = s.ID
		},
		"body namespace": func(r *semanticindex.Result) {
			s := &r.ImplementationSymbols[3]
			s.Key.Namespace = "repo/Other.csproj"
			s.ID = s.ComputeID()
			r.Binding.ImplementationFacts[0].DefaultTemplateSymbolID = s.ID
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := defaultSelectionMCPFixture()
			change(&r)
			if _, err := compilerBinding(r); err == nil {
				t.Fatal("invalid reader selection accepted")
			}
		})
	}
}
