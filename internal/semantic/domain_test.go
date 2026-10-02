package semantic

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func domainFixture() *Artifact {
	a := fixture()
	c := &a.Contexts[0]
	c.Extractor = "msbuild-roslyn"
	c.ExtractorVersion = "2"
	c.ID = c.ComputeID()
	o := &a.Occurrences[0]
	o.ContextID = c.ID
	o.ID = o.ComputeID()
	target := &a.Symbols[0]
	target.Key.Descriptor = "T:Target"
	target.ID = target.ComputeID()
	api := Symbol{Key: SymbolKey{Language: "csharp", NamespaceKind: "assembly", Namespace: "Microsoft.Extensions.DependencyInjection.Abstractions, Version=10.0.0.0, Culture=neutral, PublicKeyToken=adb9793829ddae60", Descriptor: "M:Microsoft.Extensions.DependencyInjection.ServiceCollectionServiceExtensions.AddScoped``2(Microsoft.Extensions.DependencyInjection.IServiceCollection)", DescriptorKind: "documentation_comment_id"}}
	api.ID = api.ComputeID()
	a.Symbols = append(a.Symbols, api)
	b := &a.Bindings[0]
	b.OccurrenceID = o.ID
	b.Extractor = c.Extractor
	b.ExtractorVersion = c.ExtractorVersion
	b.Method = "roslyn-semantic-model"
	b.SymbolID = api.ID
	b.DomainFacts = []DomainFact{{Kind: "di_registration", Rule: "csharp-framework-v1", EvidenceScope: "compile_time", Lifetime: "scoped", Targets: []DomainTarget{{Role: "service", SymbolID: target.ID}, {Role: "implementation", SymbolID: target.ID}}}}
	b.ID = b.ComputeID()
	return a
}
func TestDomainArtifactRoundTripAndIdentity(t *testing.T) {
	a := domainFixture()
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "artifact")
	if err := Write(p, a); err != nil {
		t.Fatal(err)
	}
	got, err := Read(p, Limits{})
	if err != nil || !reflect.DeepEqual(a, got) {
		t.Fatalf("roundtrip: %v", err)
	}
	id := a.Bindings[0].ID
	a.Bindings[0].DomainFacts[0].Lifetime = "transient"
	if a.Bindings[0].ComputeID() == id {
		t.Fatal("facts not identity-bound")
	}
	legacy := fixture()
	raw, _ := json.Marshal(legacy.Bindings[0])
	if strings.Contains(string(raw), "domain_facts") {
		t.Fatal("legacy binding wire identity changed")
	}
}
func TestDomainRejectsUnsupportedEvidence(t *testing.T) {
	changes := map[string]func(*Artifact){
		"unknown target": func(a *Artifact) {
			a.Bindings[0].DomainFacts[0].Targets[0].SymbolID = "symbol:" + strings.Repeat("a", 64)
		},
		"unresolved":      func(a *Artifact) { a.Bindings[0].Status = "unresolved"; a.Bindings[0].SymbolID = "" },
		"wrong rule":      func(a *Artifact) { a.Bindings[0].DomainFacts[0].Rule = "guess" },
		"runtime claim":   func(a *Artifact) { a.Bindings[0].DomainFacts[0].EvidenceScope = "runtime" },
		"wrong lifetime":  func(a *Artifact) { a.Bindings[0].DomainFacts[0].Lifetime = "singleton" },
		"wrong roles":     func(a *Artifact) { a.Bindings[0].DomainFacts[0].Targets[0].Role = "message" },
		"oversized":       func(a *Artifact) { a.Bindings[0].DomainFacts[0].Table = strings.Repeat("x", 1025) },
		"extra attribute": func(a *Artifact) { a.Bindings[0].DomainFacts[0].Table = "table" },
		"duplicate": func(a *Artifact) {
			a.Bindings[0].DomainFacts = append(a.Bindings[0].DomainFacts, a.Bindings[0].DomainFacts[0])
		},
		"too many": func(a *Artifact) {
			f := a.Bindings[0].DomainFacts[0]
			a.Bindings[0].DomainFacts = make([]DomainFact, 9)
			for i := range a.Bindings[0].DomainFacts {
				a.Bindings[0].DomainFacts[i] = f
			}
		},
	}
	for name, mutate := range changes {
		t.Run(name, func(t *testing.T) {
			a := domainFixture()
			mutate(a)
			a.Bindings[0].ID = a.Bindings[0].ComputeID()
			if a.Validate() == nil {
				t.Fatal("accepted invalid domain evidence")
			}
		})
	}
	for _, mutation := range []string{"source API", "generic target", "wrong compiler", "incomplete"} {
		t.Run(mutation, func(t *testing.T) {
			a := domainFixture()
			b := &a.Bindings[0]
			switch mutation {
			case "source API":
				a.Symbols[1].Key.NamespaceKind = "project"
				a.Symbols[1].ID = a.Symbols[1].ComputeID()
				b.SymbolID = a.Symbols[1].ID
			case "generic target":
				a.Symbols[0].Key.Descriptor = "T:Target`1"
				a.Symbols[0].ID = a.Symbols[0].ComputeID()
				for i := range b.DomainFacts[0].Targets {
					b.DomainFacts[0].Targets[i].SymbolID = a.Symbols[0].ID
				}
			case "wrong compiler", "incomplete":
				c := &a.Contexts[0]
				if mutation == "incomplete" {
					c.Status = "incomplete"
					c.Issues = []string{"errors"}
				} else {
					c.ExtractorVersion = "1"
					b.ExtractorVersion = "1"
				}
				c.ID = c.ComputeID()
				o := &a.Occurrences[0]
				o.ContextID = c.ID
				o.ID = o.ComputeID()
				b.OccurrenceID = o.ID
			}
			b.ID = b.ComputeID()
			if a.Validate() == nil {
				t.Fatal("accepted unsupported domain provenance")
			}
		})
	}
}

func TestDomainAPIRejectsUnsupportedOverloads(t *testing.T) {
	a := domainFixture()
	api := a.Symbols[1]
	fact := a.Bindings[0].DomainFacts[0]
	api.Key.Descriptor = "M:Microsoft.Extensions.DependencyInjection.ServiceCollectionServiceExtensions.AddScoped``2(Microsoft.Extensions.DependencyInjection.IServiceCollection,System.Func{System.IServiceProvider,``1})"
	if ValidateDomainAPI(fact, api) == nil {
		t.Fatal("factory overload accepted")
	}
	api.Key.Namespace = "Microsoft.EntityFrameworkCore.Relational, Version=10.0.0.0"
	prefix := "M:Microsoft.EntityFrameworkCore.RelationalEntityTypeBuilderExtensions.ToTable``1(Microsoft.EntityFrameworkCore.Metadata.Builders.EntityTypeBuilder{``0},System.String"
	fact = DomainFact{Kind: "storage_table", Table: "events"}
	for _, suffix := range []string{")", ",System.String)"} {
		api.Key.Descriptor = prefix + suffix
		if err := ValidateDomainAPI(fact, api); err != nil {
			t.Fatal(err)
		}
	}
	api.Key.Descriptor = prefix + ",System.Action{Microsoft.EntityFrameworkCore.Metadata.Builders.TableBuilder{``0}})"
	if ValidateDomainAPI(fact, api) == nil {
		t.Fatal("table callback overload accepted")
	}
}
