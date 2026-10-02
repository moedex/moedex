package semanticindex

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"moedex/internal/semantic"
)

func TestDomainV2RoundTripAndCachedPayloadVersionDowngrade(t *testing.T) {
	testDomainVersionRoundTripAndDowngrade(t, "3", "2", "Microsoft.EntityFrameworkCore, Version=8.0.4.0", "M:Microsoft.EntityFrameworkCore.DbContext.Set``1", semantic.DomainFact{Kind: "storage_entity_use", Rule: "csharp-framework-v2", EvidenceScope: "compile_time", Targets: []semantic.DomainTarget{{Role: "entity"}}})
}

func TestDomainV3RoundTripAndCachedPayloadVersionDowngrade(t *testing.T) {
	testDomainVersionRoundTripAndDowngrade(t, "4", "3", "Microsoft.EntityFrameworkCore, Version=8.0.4.0", "M:Microsoft.Extensions.DependencyInjection.EntityFrameworkServiceCollectionExtensions.AddDbContext``1(Microsoft.Extensions.DependencyInjection.IServiceCollection,System.Action{Microsoft.EntityFrameworkCore.DbContextOptionsBuilder},Microsoft.Extensions.DependencyInjection.ServiceLifetime,Microsoft.Extensions.DependencyInjection.ServiceLifetime)", semantic.DomainFact{Kind: "storage_context_registration", Rule: "csharp-framework-v3", EvidenceScope: "compile_time", Lifetime: "scoped", Targets: []semantic.DomainTarget{{Role: "service"}, {Role: "implementation"}}})
}

func TestDomainV4RoundTripAndCachedPayloadVersionDowngrade(t *testing.T) {
	for _, tc := range []struct{ kind, descriptor string }{
		{"message_publish_configuration", "M:MassTransit.PublishExtensions.Publish``3(MassTransit.EventActivityBinder{``0,``1},MassTransit.EventMessageFactory{``0,``1,``2},System.Action{MassTransit.PublishContext{``2}})"},
		{"message_event_configuration", "M:MassTransit.MassTransitStateMachine`1.Event``1(System.Linq.Expressions.Expression{System.Func{MassTransit.Event{``0}}},System.Action{MassTransit.IEventCorrelationConfigurator{`0,``0}})"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			testDomainVersionRoundTripAndDowngrade(t, "5", "4", "MassTransit, Version=8.2.4.0", tc.descriptor, semantic.DomainFact{Kind: tc.kind, Rule: "csharp-framework-v4", EvidenceScope: "compile_time", Targets: []semantic.DomainTarget{{Role: "message"}}})
		})
	}
}

func testDomainVersionRoundTripAndDowngrade(t *testing.T, version, downgrade, assembly, descriptor string, fact semantic.DomainFact) {
	t.Helper()
	a := domainFixture()
	var roles []string
	for i := range fact.Targets {
		fact.Targets[i].SymbolID = a.Symbols[0].ID
		roles = append(roles, fact.Targets[i].Role)
	}
	c := &a.Contexts[0]
	c.ExtractorVersion = version
	c.ID = c.ComputeID()
	api := &a.Symbols[1]
	api.Key.Namespace = assembly
	api.Key.Descriptor = descriptor
	api.ID = api.ComputeID()
	for i := range a.Occurrences {
		o, b := &a.Occurrences[i], &a.Bindings[i]
		o.ContextID = c.ID
		o.ID = o.ComputeID()
		b.OccurrenceID = o.ID
		b.ExtractorVersion = version
		if i == 1 {
			b.SymbolID = api.ID
			b.DomainFacts = []semantic.DomainFact{fact}
		}
		b.ID = b.ComputeID()
	}
	// Two contexts deliberately share one interned domain payload. Include a
	// valid older context so corruption can reference its existing version ID.
	second := *c
	second.Project = "B.csproj"
	second.ID = second.ComputeID()
	legacy := *c
	legacy.Project = "Legacy.csproj"
	legacy.ExtractorVersion = downgrade
	legacy.ID = legacy.ComputeID()
	a.Contexts = append(a.Contexts, second, legacy)
	o, b := a.Occurrences[1], a.Bindings[1]
	o.ContextID = second.ID
	o.ID = o.ComputeID()
	b.OccurrenceID = o.ID
	b.ID = b.ComputeID()
	a.Occurrences = append(a.Occurrences, o)
	a.Bindings = append(a.Bindings, b)
	x, path := openFixture(t, a)
	for _, id := range []string{a.Contexts[0].ID, second.ID} {
		got, err := x.Bindings(context.Background(), Position{Repo: "repo", Path: "src/A.cs", Offset: 10, BuildContextID: id}, 1)
		if err != nil || len(got.Results) != 1 || got.Results[0].Binding.ExtractorVersion != version || !reflect.DeepEqual(got.Results[0].Binding.DomainFacts, []semantic.DomainFact{fact}) {
			t.Fatalf("%s roundtrip: %+v %v", fact.Rule, got, err)
		}
	}
	// A registration's service and implementation roles point to one symbol;
	// retrieval must coalesce both postings into one observation per context.
	combined, err := x.ContractContext(context.Background(), a.Symbols[0].ID, []string{a.Contexts[0].ID, second.ID}, 2, 20)
	if err != nil || len(combined.Matches) != 2 || combined.EvidenceTruncated || combined.DefinitionsTruncated || combined.PostingsVisited != 2*len(roles) {
		t.Fatalf("combined role coalescing: %+v %v", combined, err)
	}
	for _, match := range combined.Matches {
		if !reflect.DeepEqual(match.TargetRoles, roles) || !reflect.DeepEqual(match.Result.Binding.DomainFacts[match.DomainFactIndex], fact) {
			t.Fatalf("coalesced observation lost roles/fact: %+v", match)
		}
	}
	if x.sections[domainFacts].n != 2 || x.field(domainFacts, 0, 1) != x.field(domainFacts, 1, 1) {
		t.Fatal("fixture does not exercise reused domain payload")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var oldVersion uint64
	for row := uint64(0); row < x.sections[contexts].n; row++ {
		if x.text(x.field(contexts, row, 5)) == downgrade {
			oldVersion = x.field(contexts, row, 5)
		}
	}
	if oldVersion == 0 {
		t.Fatal("missing old version string")
	}
	// Downgrade the SECOND fact sharing the decoded payload, together with its
	// context. Core column consistency remains valid; rule validation must fail.
	factRow := x.field(domainFacts, 1, 0)
	contextRow := x.field(facts, factRow, 3)
	for row := uint64(0); row < x.sections[facts].n; row++ {
		if x.field(facts, row, 3) == contextRow {
			le.PutUint64(data[x.sections[facts].off+row*widths[facts]+15*8:], oldVersion)
		}
	}
	le.PutUint64(data[x.sections[contexts].off+contextRow*widths[contexts]+5*8:], oldVersion)
	sum := sha256.Sum256(data[headerSize:])
	copy(data[24:56], sum[:])
	bad := filepath.Join(t.TempDir(), "downgraded")
	if err := os.WriteFile(bad, data, 0600); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(bad, provenance(), Limits{})
	if err == nil {
		opened.Close()
		t.Fatalf("accepted %s payload reused by downgraded worker%s fact", fact.Rule, downgrade)
	}
}
