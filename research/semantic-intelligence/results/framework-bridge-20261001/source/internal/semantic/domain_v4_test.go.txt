package semantic

import (
	"strings"
	"testing"
)

func TestMessageConfigurationShapeAPIAndArtifact(t *testing.T) {
	for _, tc := range []struct{ kind, descriptor string }{
		{"message_publish_configuration", "M:MassTransit.PublishExtensions.Publish``3(MassTransit.EventActivityBinder{``0,``1},MassTransit.EventMessageFactory{``0,``1,``2},System.Action{MassTransit.PublishContext{``2}})"},
		{"message_event_configuration", "M:MassTransit.MassTransitStateMachine`1.Event``1(System.Linq.Expressions.Expression{System.Func{MassTransit.Event{``0}}},System.Action{MassTransit.IEventCorrelationConfigurator{`0,``0}})"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			f := DomainFact{Kind: tc.kind, Rule: "csharp-framework-v4", EvidenceScope: "compile_time", Targets: []DomainTarget{{Role: "message", SymbolID: "symbol:" + strings.Repeat("a", 64)}}}
			api := domainFixture().Symbols[1]
			api.Key.Namespace = "MassTransit, Version=8.2.4.0"
			api.Key.Descriptor = tc.descriptor
			if err := ValidateDomainFacts([]DomainFact{f}); err != nil {
				t.Fatal(err)
			}
			if err := ValidateDomainAPI(f, api); err != nil {
				t.Fatal(err)
			}
			for _, mutate := range []func(*DomainFact){
				func(f *DomainFact) { f.Rule = "csharp-framework-v3" },
				func(f *DomainFact) { f.Kind = "message_consumer" },
				func(f *DomainFact) { f.EvidenceScope = "runtime" },
				func(f *DomainFact) { f.Lifetime = "scoped" },
				func(f *DomainFact) { f.Table = "messages" },
				func(f *DomainFact) { f.Schema = "public" },
				func(f *DomainFact) { f.Targets[0].Role = "service" },
				func(f *DomainFact) { f.Targets = nil },
				func(f *DomainFact) { f.Targets = append(f.Targets, f.Targets[0]) },
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
				func(s *Symbol) { s.Key.Namespace = "MassTransit.Abstractions, Version=8.2.4.0" },
				func(s *Symbol) { s.Key.Language = "visualbasic" },
				func(s *Symbol) { s.Key.Descriptor += "extra" },
				func(s *Symbol) {
					s.Key.Descriptor = strings.ReplaceAll(s.Key.Descriptor, "System.Action", "System.Func")
				},
			} {
				bad := api
				mutate(&bad)
				if ValidateDomainAPI(f, bad) == nil {
					t.Fatalf("accepted wrong API %+v", bad)
				}
			}
			for _, version := range []string{"2", "3", "4", "5", "6", "7"} {
				a := domainFixture()
				c := &a.Contexts[0]
				c.ExtractorVersion = version
				c.ID = c.ComputeID()
				o := &a.Occurrences[0]
				o.ContextID = c.ID
				o.ID = o.ComputeID()
				a.Symbols[1] = api
				a.Symbols[1].ID = api.ComputeID()
				fact := f
				fact.Targets = []DomainTarget{{Role: "message", SymbolID: a.Symbols[0].ID}}
				b := &a.Bindings[0]
				b.ExtractorVersion = version
				b.OccurrenceID = o.ID
				b.SymbolID = a.Symbols[1].ID
				b.DomainFacts = []DomainFact{fact}
				b.ID = b.ComputeID()
				if err := a.Validate(); (err == nil) != (version == "5" || version == "6" || version == "7") {
					t.Fatalf("worker %s: %v", version, err)
				}
			}
		})
	}
}
