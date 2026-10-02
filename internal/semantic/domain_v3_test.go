package semantic

import (
	"strings"
	"testing"
)

func TestContextRegistrationShapeAndAPI(t *testing.T) {
	id := "symbol:" + strings.Repeat("a", 64)
	f := DomainFact{Kind: "storage_context_registration", Rule: "csharp-framework-v3", EvidenceScope: "compile_time", Lifetime: "scoped", Targets: []DomainTarget{{Role: "service", SymbolID: id}, {Role: "implementation", SymbolID: id}}}
	api := domainFixture().Symbols[1]
	api.Key.Namespace = "Microsoft.EntityFrameworkCore, Version=8.0.4.0"
	api.Key.Descriptor = "M:Microsoft.Extensions.DependencyInjection.EntityFrameworkServiceCollectionExtensions.AddDbContext``1(Microsoft.Extensions.DependencyInjection.IServiceCollection,System.Action{Microsoft.EntityFrameworkCore.DbContextOptionsBuilder},Microsoft.Extensions.DependencyInjection.ServiceLifetime,Microsoft.Extensions.DependencyInjection.ServiceLifetime)"
	for _, lifetime := range []string{"scoped", "singleton", "transient"} {
		f.Lifetime = lifetime
		if err := ValidateDomainFacts([]DomainFact{f}); err != nil {
			t.Fatal(err)
		}
		if err := ValidateDomainAPI(f, api); err != nil {
			t.Fatal(err)
		}
	}
	for _, mutate := range []func(*DomainFact){
		func(f *DomainFact) { f.Rule = "csharp-framework-v2" },
		func(f *DomainFact) { f.Lifetime = "" },
		func(f *DomainFact) { f.Lifetime = "unknown" },
		func(f *DomainFact) { f.Table = "contexts" },
		func(f *DomainFact) { f.EvidenceScope = "runtime" },
		func(f *DomainFact) { f.Targets[1].SymbolID = "symbol:" + strings.Repeat("b", 64) },
		func(f *DomainFact) { f.Targets[0].Role = "entity" },
	} {
		bad := f
		bad.Targets = append([]DomainTarget(nil), f.Targets...)
		mutate(&bad)
		if ValidateDomainFacts([]DomainFact{bad}) == nil {
			t.Fatalf("accepted invalid fact %+v", bad)
		}
	}
	for _, mutate := range []func(*Symbol){
		func(s *Symbol) { s.Key.NamespaceKind = "project" },
		func(s *Symbol) { s.Key.Namespace = "Lookalike" },
		func(s *Symbol) { s.Key.Descriptor = strings.Replace(s.Key.Descriptor, "``1(", "``2(", 1) },
		func(s *Symbol) {
			s.Key.Descriptor = strings.Replace(s.Key.Descriptor, "System.Action{Microsoft.EntityFrameworkCore.DbContextOptionsBuilder}", "System.Action{System.IServiceProvider,Microsoft.EntityFrameworkCore.DbContextOptionsBuilder}", 1)
		},
	} {
		bad := api
		mutate(&bad)
		if ValidateDomainAPI(f, bad) == nil {
			t.Fatalf("accepted unsupported API %+v", bad)
		}
	}
}
