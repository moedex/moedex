package semantic

import (
	"strings"
	"testing"
)

func TestResponseAndConditionalRegistration(t *testing.T) {
	for _, tc := range []struct{ kind, lifetime, assembly, descriptor string }{
		{"message_response", "", "MassTransit.Abstractions", "M:MassTransit.ConsumeContext.RespondAsync``1(System.Object)"},
		{"message_response", "", "MassTransit.Abstractions", "M:MassTransit.ConsumeContext.RespondAsync``1(``0)"},
		{"di_registration_if_absent", "singleton", "Microsoft.Extensions.DependencyInjection.Abstractions", "M:Microsoft.Extensions.DependencyInjection.Extensions.ServiceCollectionDescriptorExtensions.TryAddSingleton``2(Microsoft.Extensions.DependencyInjection.IServiceCollection)"},
		{"di_registration_if_absent", "scoped", "Microsoft.Extensions.DependencyInjection.Abstractions", "M:Microsoft.Extensions.DependencyInjection.Extensions.ServiceCollectionDescriptorExtensions.TryAddScoped``1(Microsoft.Extensions.DependencyInjection.IServiceCollection)"},
		{"di_registration_if_absent", "transient", "Microsoft.Extensions.DependencyInjection.Abstractions", "M:Microsoft.Extensions.DependencyInjection.Extensions.ServiceCollectionDescriptorExtensions.TryAddTransient``2(Microsoft.Extensions.DependencyInjection.IServiceCollection)"},
	} {
		t.Run(tc.descriptor, func(t *testing.T) {
			a := domainFixture()
			api := a.Symbols[1]
			api.Key.Namespace = tc.assembly + ", Version=8.0.0.0"
			api.Key.Descriptor = tc.descriptor
			api.ID = api.ComputeID()
			f := DomainFact{Kind: tc.kind, Rule: "csharp-framework-v5", EvidenceScope: "compile_time", Lifetime: tc.lifetime, Targets: []DomainTarget{{Role: "message", SymbolID: a.Symbols[0].ID}}}
			if tc.lifetime != "" {
				f.Targets = []DomainTarget{{Role: "service", SymbolID: a.Symbols[0].ID}, {Role: "implementation", SymbolID: a.Symbols[0].ID}}
			}
			if err := ValidateDomainFacts([]DomainFact{f}); err != nil {
				t.Fatal(err)
			}
			if err := ValidateDomainAPI(f, api); err != nil {
				t.Fatal(err)
			}
			for _, mutate := range []func(*DomainFact){
				func(f *DomainFact) { f.Rule = "csharp-framework-v4" }, func(f *DomainFact) { f.EvidenceScope = "runtime" }, func(f *DomainFact) { f.Table = "table" }, func(f *DomainFact) { f.Targets = nil }, func(f *DomainFact) { f.Lifetime = "unknown" },
			} {
				bad := f
				mutate(&bad)
				if ValidateDomainFacts([]DomainFact{bad}) == nil {
					t.Fatalf("accepted %+v", bad)
				}
			}
			for _, mutate := range []func(*Symbol){
				func(s *Symbol) { s.Key.NamespaceKind = "project" }, func(s *Symbol) { s.Key.Namespace = "Lookalike" }, func(s *Symbol) { s.Key.Descriptor += "extra" }, func(s *Symbol) { s.Key.Descriptor = strings.ReplaceAll(s.Key.Descriptor, "``", "`") },
			} {
				bad := api
				mutate(&bad)
				if ValidateDomainAPI(f, bad) == nil {
					t.Fatalf("accepted %+v", bad)
				}
			}
			for _, version := range []string{"9", "10", "11", "12", "13", "14", "15", "16", "17", "18", "19", "20", "21"} {
				c := &a.Contexts[0]
				c.ExtractorVersion = version
				c.ID = c.ComputeID()
				o := &a.Occurrences[0]
				o.ContextID = c.ID
				o.ID = o.ComputeID()
				a.Symbols[1] = api
				b := &a.Bindings[0]
				b.ExtractorVersion = version
				b.OccurrenceID = o.ID
				b.SymbolID = api.ID
				b.DomainFacts = []DomainFact{f}
				b.ID = b.ComputeID()
				if err := a.Validate(); (err == nil) != (version == "10" || (version == "11" || (version == "12" || (version == "13" || (version == "14" || (version == "15" || (version == "16" || (version == "17" || (version == "18" || (version == "19" || version == "20")))))))))) {
					t.Fatalf("version %s: %v", version, err)
				}
			}
			if strings.Contains(tc.descriptor, "``1(") && tc.lifetime != "" {
				f.Targets[1].SymbolID = "symbol:" + strings.Repeat("b", 64)
				if ValidateDomainAPI(f, api) == nil {
					t.Fatal("single-type registration with distinct implementation accepted")
				}
			}
		})
	}
}
