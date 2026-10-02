package semantic

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func implementationFixture() *Artifact {
	a := fixture()
	c := &a.Contexts[0]
	c.Extractor, c.ExtractorVersion = "msbuild-roslyn", "6"
	c.ID = c.ComputeID()
	a.Symbols = nil
	for _, d := range []string{"M:Service.Run(System.String)", "M:IService.Run(System.String)", "T:Service"} {
		s := Symbol{Key: SymbolKey{Language: "csharp", NamespaceKind: "project", Namespace: "repo/A", Descriptor: d, DescriptorKind: "documentation_comment_id"}}
		s.ID = s.ComputeID()
		a.Symbols = append(a.Symbols, s)
	}
	o := &a.Occurrences[0]
	o.ContextID, o.Role, o.Kind = c.ID, "declaration", "declaration"
	o.ID = o.ComputeID()
	b := &a.Bindings[0]
	b.OccurrenceID, b.SymbolID = o.ID, a.Symbols[0].ID
	b.Method, b.Extractor, b.ExtractorVersion = "roslyn-semantic-model", c.Extractor, c.ExtractorVersion
	b.ImplementationFacts = []ImplementationFact{{Kind: "interface_method_implementation", Rule: "csharp-interface-v1", EvidenceScope: "compile_time", InterfaceSymbolID: a.Symbols[1].ID, ImplementingTypeSymbolID: a.Symbols[2].ID}}
	b.ID = b.ComputeID()
	return a
}

func TestImplementationRoundTripIdentityAndCompositionOwnership(t *testing.T) {
	a := implementationFixture()
	p := filepath.Join(t.TempDir(), "artifact")
	if err := Write(p, a); err != nil {
		t.Fatal(err)
	}
	b, err := Read(p, Limits{})
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("roundtrip: %v", err)
	}
	out, err := Compose(context.Background(), []*Artifact{a}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	id := a.Bindings[0].ID
	a.Bindings[0].ImplementationFacts[0].Rule = "changed"
	if a.Bindings[0].ComputeID() == id || out.Bindings[0].ImplementationFacts[0].Rule != "csharp-interface-v1" {
		t.Fatal("relation identity or composition ownership lost")
	}
	raw, _ := json.Marshal(fixture().Bindings[0])
	if strings.Contains(string(raw), "implementation_facts") {
		t.Fatal("legacy binding encoding changed")
	}
}

func TestImplementationRejectsForgedEvidence(t *testing.T) {
	for name, mutate := range map[string]func(*Artifact){
		"old worker":   func(a *Artifact) { a.Contexts[0].ExtractorVersion = "5"; a.Bindings[0].ExtractorVersion = "5" },
		"incomplete":   func(a *Artifact) { a.Contexts[0].Status = "incomplete" },
		"reference":    func(a *Artifact) { a.Occurrences[0].Role = "reference"; a.Occurrences[0].Kind = "invocation" },
		"runtime":      func(a *Artifact) { a.Bindings[0].ImplementationFacts[0].EvidenceScope = "runtime" },
		"unknown rule": func(a *Artifact) { a.Bindings[0].ImplementationFacts[0].Rule = "guessed" },
		"missing target": func(a *Artifact) {
			a.Bindings[0].ImplementationFacts[0].InterfaceSymbolID = "symbol:" + strings.Repeat("a", 64)
		},
		"wrong type": func(a *Artifact) { a.Bindings[0].ImplementationFacts[0].ImplementingTypeSymbolID = a.Symbols[1].ID },
		"self":       func(a *Artifact) { a.Bindings[0].ImplementationFacts[0].InterfaceSymbolID = a.Symbols[0].ID },
		"duplicate": func(a *Artifact) {
			a.Bindings[0].ImplementationFacts = append(a.Bindings[0].ImplementationFacts, a.Bindings[0].ImplementationFacts[0])
		},
		"oversized": func(a *Artifact) {
			for len(a.Bindings[0].ImplementationFacts) <= MaxImplementationFacts {
				a.Bindings[0].ImplementationFacts = append(a.Bindings[0].ImplementationFacts, a.Bindings[0].ImplementationFacts[0])
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := implementationFixture()
			mutate(a)
			a.Contexts[0].ID = a.Contexts[0].ComputeID()
			a.Occurrences[0].ContextID = a.Contexts[0].ID
			a.Occurrences[0].ID = a.Occurrences[0].ComputeID()
			a.Bindings[0].OccurrenceID = a.Occurrences[0].ID
			a.Bindings[0].ID = a.Bindings[0].ComputeID()
			if a.Validate() == nil {
				t.Fatal("accepted invalid relationship")
			}
		})
	}
}

func TestImplementationExactDeclaringTypeAndExplicitMethod(t *testing.T) {
	a := implementationFixture()
	f := a.Bindings[0].ImplementationFacts[0]
	for _, tc := range []struct {
		descriptor string
		valid      bool
	}{
		{"M:Service.Run(System.String)", true},
		{"M:Service.IService#Run(System.String)", true},
		{"M:Service.Nested.Run(System.String)", false},
		{"M:Service.Run``1(``0)", false},
		{"M:Service.#ctor", false},
	} {
		s := a.Symbols[0]
		s.Key.Descriptor = tc.descriptor
		s.ID = s.ComputeID()
		if got := ValidateImplementationSymbols(f, s, a.Symbols[1], a.Symbols[2]) == nil; got != tc.valid {
			t.Errorf("%s accepted=%v", tc.descriptor, got)
		}
	}
}
