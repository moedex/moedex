package semantic

import (
	"strings"
	"testing"
)

func TestEntityObservationRulesAndExactAPIs(t *testing.T) {
	for _, tc := range []struct{ kind, method string }{{"storage_entity_use", "DbContext.Set"}, {"storage_entity_mapping", "ModelBuilder.Entity"}} {
		t.Run(tc.kind, func(t *testing.T) {
			f := DomainFact{Kind: tc.kind, Rule: "csharp-framework-v2", EvidenceScope: "compile_time", Targets: []DomainTarget{{Role: "entity", SymbolID: "symbol:" + strings.Repeat("a", 64)}}}
			if err := ValidateDomainFacts([]DomainFact{f}); err != nil {
				t.Fatal(err)
			}
			api := domainFixture().Symbols[1]
			api.Key.Namespace = "Microsoft.EntityFrameworkCore, Version=8.0.4.0"
			api.Key.Descriptor = "M:Microsoft.EntityFrameworkCore." + tc.method + "``1"
			if err := ValidateDomainAPI(f, api); err != nil {
				t.Fatal(err)
			}
			for _, suffix := range []string{"(System.String)", "(System.Type)", "(System.Action{``0})", "``1"} {
				bad := api
				bad.Key.Descriptor += suffix
				if ValidateDomainAPI(f, bad) == nil {
					t.Fatalf("accepted overload %s", bad.Key.Descriptor)
				}
			}
			badAPI := api
			badAPI.Key.Namespace = "Application"
			if ValidateDomainAPI(f, badAPI) == nil {
				t.Fatal("accepted lookalike assembly")
			}
			for _, mutate := range []func(*DomainFact){func(f *DomainFact) { f.Rule = "csharp-framework-v1" }, func(f *DomainFact) { f.Rule = "csharp-framework-v3" }, func(f *DomainFact) { f.Table = "entities" }, func(f *DomainFact) { f.Lifetime = "scoped" }, func(f *DomainFact) { f.EvidenceScope = "runtime" }, func(f *DomainFact) { f.Targets = []DomainTarget{{Role: "message", SymbolID: f.Targets[0].SymbolID}} }} {
				bad := f
				mutate(&bad)
				if ValidateDomainFacts([]DomainFact{bad}) == nil {
					t.Fatalf("accepted invalid fact %+v", bad)
				}
			}
		})
	}
	f := domainFixture().Bindings[0].DomainFacts[0]
	f.Rule = "csharp-framework-v2"
	if ValidateDomainFacts([]DomainFact{f}) == nil {
		t.Fatal("silently relabelled old rule")
	}
}
