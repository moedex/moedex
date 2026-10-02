package semanticindex

import (
	"context"
	"crypto/sha256"
	"errors"
	"moedex/internal/semantic"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func wrapperFixture() *semantic.Artifact {
	a := fixture(0)
	a.Sources[0].ByteSize = 1000000
	a.Sources[0].ID = a.Sources[0].ComputeID()
	c := &a.Contexts[0]
	c.SourceIDs = []string{a.Sources[0].ID}
	c.Extractor = "msbuild-roslyn"
	c.ExtractorVersion = "6"
	c.ID = c.ComputeID()
	mk := func(descriptor string) semantic.Symbol {
		s := a.Symbols[0]
		s.Key.Descriptor = descriptor
		s.ID = s.ComputeID()
		return s
	}
	a.Symbols = []semantic.Symbol{mk("T:Message"), mk("M:Service.Send"), mk("T:Service"), mk("M:IService.Send"), mk("M:Controller.Submit"), mk("M:Direct.Submit")}
	api := mk("M:MassTransit.IPublishEndpoint.Publish``1(``0,System.Threading.CancellationToken)")
	api.Key.NamespaceKind = "assembly"
	api.Key.Namespace = "MassTransit.Abstractions, Version=8.2.4.0"
	api.ID = api.ComputeID()
	a.Symbols = append(a.Symbols, api)
	add := func(offset uint64, role, kind string, symbol, owner string, ds []semantic.DomainFact, is []semantic.ImplementationFact) {
		o := semantic.Occurrence{SourceID: a.Sources[0].ID, ContextID: c.ID, Role: role, Kind: kind, Offset: offset, Length: 1}
		o.ID = o.ComputeID()
		b := semantic.Binding{OccurrenceID: o.ID, Status: "resolved", SymbolID: symbol, EnclosingSymbolID: owner, Method: "roslyn-semantic-model", Extractor: "msbuild-roslyn", ExtractorVersion: "6", DomainFacts: ds, ImplementationFacts: is}
		b.ID = b.ComputeID()
		a.Occurrences = append(a.Occurrences, o)
		a.Bindings = append(a.Bindings, b)
	}
	add(0, "declaration", "declaration", a.Symbols[1].ID, "", nil, []semantic.ImplementationFact{{Kind: "interface_method_implementation", Rule: "csharp-interface-v1", EvidenceScope: "compile_time", InterfaceSymbolID: a.Symbols[3].ID, ImplementingTypeSymbolID: a.Symbols[2].ID}})
	add(10, "reference", "invocation", api.ID, a.Symbols[1].ID, []semantic.DomainFact{{Kind: "message_publish", Rule: "csharp-framework-v1", EvidenceScope: "compile_time", Targets: []semantic.DomainTarget{{Role: "message", SymbolID: a.Symbols[0].ID}}}}, nil)
	add(20, "reference", "invocation", a.Symbols[3].ID, a.Symbols[4].ID, nil, nil)
	add(30, "reference", "invocation", a.Symbols[1].ID, a.Symbols[5].ID, nil, nil)
	return a
}
func TestContractPathsExactHopsBoundsAndOwnership(t *testing.T) {
	a := wrapperFixture()
	x, _ := openFixture(t, a)
	ids := []string{a.Contexts[0].ID}
	got, err := x.ContractPaths(context.Background(), a.Symbols[0].ID, ids, 100)
	if err != nil || !got.Supported || len(got.Seeds) != 1 || len(got.Paths) != 2 || got.Records != 6 || got.SeedsTruncated || got.PathsTruncated {
		t.Fatalf("paths %+v %v", got, err)
	}
	if got.Paths[0].Implementation != nil || got.Paths[0].Caller.Binding.SymbolID != a.Symbols[1].ID || got.Paths[1].Implementation == nil || got.Paths[1].Caller.Binding.SymbolID != a.Symbols[3].ID {
		t.Fatal("wrong joins")
	}
	if !reflect.DeepEqual(got.Paths[1].Implementation.Binding.ImplementationFacts, a.Bindings[0].ImplementationFacts) {
		t.Fatal("relationship lost")
	}
	for _, limit := range []int{1, 2, 3, 5} {
		q, e := x.ContractPaths(context.Background(), a.Symbols[0].ID, ids, limit)
		if e != nil || q.Records > limit || !q.PathsTruncated {
			t.Fatalf("limit%d %+v %v", limit, q, e)
		}
	}
	if _, err := x.ContractPaths(context.Background(), a.Symbols[0].ID, nil, 100); !errors.Is(err, ErrContractContextSelection) {
		t.Fatal("implicit scope admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := x.ContractPaths(ctx, a.Symbols[0].ID, ids, 100); err != context.Canceled {
		t.Fatal(err)
	}
	x.Close()
	if got.Paths[1].Implementation.ImplementationSymbols[0].ID == "" || got.Paths[1].Seed.Owner.Key.Descriptor != "M:Service.Send" {
		t.Fatal("unowned results")
	}
}
func TestContractPathsDoesNotInventPublisherOrInterfaceJoin(t *testing.T) {
	a := wrapperFixture()
	a.Bindings[0].ImplementationFacts = nil
	a.Bindings[0].ID = a.Bindings[0].ComputeID()
	x, _ := openFixture(t, a)
	q, e := x.ContractPaths(context.Background(), a.Symbols[0].ID, []string{a.Contexts[0].ID}, 100)
	if e != nil || len(q.Paths) != 1 || q.Paths[0].Implementation != nil {
		t.Fatalf("invented interface %+v %v", q, e)
	}
	q, e = x.ContractPaths(context.Background(), a.Symbols[3].ID, []string{a.Contexts[0].ID}, 100)
	if e != nil || len(q.Paths) != 0 || len(q.Seeds) != 0 {
		t.Fatal("invented publisher")
	}
}
func TestImplementationIndexRejectsCorruption(t *testing.T) {
	a := wrapperFixture()
	x, path := openFixture(t, a)
	original, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func([]byte){
		func(b []byte) { le.PutUint64(b[x.sections[implementationPostings].off:], x.sections[symbols].n) },
		func(b []byte) { le.PutUint64(b[x.sections[implementationPostings].off+16:], 32) },
		func(b []byte) {
			le.PutUint64(b[x.sections[invocationPostings].off:], x.field(implementationFacts, 0, 0))
		},
		func(b []byte) { le.PutUint64(b[x.sections[implementationFacts].off+8:], 0) },
	} {
		b := append([]byte(nil), original...)
		mutate(b)
		sum := sha256.Sum256(b[headerSize:])
		copy(b[24:56], sum[:])
		p := filepath.Join(t.TempDir(), "bad")
		if e := os.WriteFile(p, b, 0600); e != nil {
			t.Fatal(e)
		}
		bad, e := Open(p, provenance(), Limits{})
		if e == nil {
			bad.Close()
			t.Fatal("accepted corrupt relationship/posting")
		}
	}
}
func TestContractPathsReadsV3WithoutCapability(t *testing.T) {
	a := domainFixture()
	x, path := openFixture(t, a)
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	end := x.sections[contractPostings].off + x.sections[contractPostings].n*widths[contractPostings]
	old := append(append([]byte(nil), raw[:contractHeaderSize]...), raw[headerSize:end]...)
	le.PutUint64(old[8:], 3)
	le.PutUint64(old[16:], uint64(len(old)))
	clear(old[120+contractSectionCount*16 : contractHeaderSize])
	for i := 0; i < contractSectionCount; i++ {
		at := 120 + i*16
		le.PutUint64(old[at:], le.Uint64(old[at:])-uint64(headerSize-contractHeaderSize))
	}
	sum := sha256.Sum256(old[contractHeaderSize:])
	copy(old[24:56], sum[:])
	p := filepath.Join(t.TempDir(), "v3")
	if e := os.WriteFile(p, old, 0600); e != nil {
		t.Fatal(e)
	}
	legacy, e := Open(p, provenance(), Limits{})
	if e != nil {
		t.Fatal(e)
	}
	defer legacy.Close()
	q, e := legacy.ContractPaths(context.Background(), a.Symbols[0].ID, []string{a.Contexts[0].ID}, 100)
	if e != nil || q.Supported {
		t.Fatal("legacy paths fabricated", e)
	}
	impact, e := legacy.ContractImpact(context.Background(), a.Symbols[0].ID, []string{a.Contexts[0].ID}, 100)
	if e != nil || len(impact.Matches) != 1 {
		t.Fatal("legacy domain read regressed", e)
	}
}

func TestImplementationQueryByteBudget(t *testing.T) {
	a := wrapperFixture()
	old := a.Symbols[3].ID
	a.Symbols[3].Key.Descriptor = "M:IService." + strings.Repeat("A", MaxQueryBytes)
	a.Symbols[3].ID = a.Symbols[3].ComputeID()
	for i := range a.Bindings {
		b := &a.Bindings[i]
		if b.SymbolID == old {
			b.SymbolID = a.Symbols[3].ID
		}
		for j := range b.ImplementationFacts {
			if b.ImplementationFacts[j].InterfaceSymbolID == old {
				b.ImplementationFacts[j].InterfaceSymbolID = a.Symbols[3].ID
			}
		}
		b.ID = b.ComputeID()
	}
	x, _ := openFixture(t, a)
	if cost := x.implementationResultCost(x.field(implementationFacts, 0, 0)); cost <= MaxQueryBytes {
		t.Fatal("oversized descriptor uncharged")
	}
	if _, e := x.Implementations(context.Background(), a.Symbols[3].ID, nil, 100); e == nil || !strings.Contains(e.Error(), "byte budget") {
		t.Fatal("reverse discovery materialized oversized interface", e)
	}
	if _, e := x.Bindings(context.Background(), Position{Repo: "repo", Path: "src/A.cs", Offset: 0}, 1); e == nil || !strings.Contains(e.Error(), "byte budget") {
		t.Fatal("declaration materialized oversized relationship", e)
	}
	if _, e := x.ContractPaths(context.Background(), a.Symbols[0].ID, []string{a.Contexts[0].ID}, 100); e == nil || !strings.Contains(e.Error(), "byte budget") {
		t.Fatal("path materialized oversized relationship", e)
	}
}

func TestContractPathsChargesSkippedSeedRows(t *testing.T) {
	a := wrapperFixture()
	api := a.Symbols[6]
	api.Key.Namespace = "Microsoft.EntityFrameworkCore, Version=8.0.4.0"
	api.Key.Descriptor = "M:Microsoft.EntityFrameworkCore.DbContext.Set``1"
	api.ID = api.ComputeID()
	a.Symbols = append(a.Symbols, api)
	for i := 0; i < ContractWorkLimit+1; i++ {
		o := a.Occurrences[1]
		o.Offset = uint64(100 + i*2)
		o.ID = o.ComputeID()
		b := a.Bindings[1]
		b.OccurrenceID = o.ID
		b.SymbolID = api.ID
		b.DomainFacts = []semantic.DomainFact{{Kind: "storage_entity_use", Rule: "csharp-framework-v2", EvidenceScope: "compile_time", Targets: []semantic.DomainTarget{{Role: "entity", SymbolID: a.Symbols[0].ID}}}}
		b.ID = b.ComputeID()
		a.Occurrences = append(a.Occurrences, o)
		a.Bindings = append(a.Bindings, b)
	}
	x, _ := openFixture(t, a)
	q, e := x.ContractPaths(context.Background(), a.Symbols[0].ID, []string{a.Contexts[0].ID}, 100)
	if e != nil || q.RowsVisited != ContractWorkLimit || !q.SeedsTruncated || !q.PathsTruncated || len(q.Paths) != 0 {
		t.Fatalf("skipped rows escaped bound: %+v %v", q, e)
	}
}

func TestImplementationCachedPayloadCannotDowngradeWorker(t *testing.T) {
	a := wrapperFixture()
	second := a.Contexts[0]
	second.Project = "B.csproj"
	second.ID = second.ComputeID()
	legacy := a.Contexts[0]
	legacy.Project = "Legacy.csproj"
	legacy.ExtractorVersion = "5"
	legacy.ID = legacy.ComputeID()
	a.Contexts = append(a.Contexts, second, legacy)
	o, b := a.Occurrences[0], a.Bindings[0]
	o.ContextID = second.ID
	o.ID = o.ComputeID()
	b.OccurrenceID = o.ID
	b.ID = b.ComputeID()
	a.Occurrences = append(a.Occurrences, o)
	a.Bindings = append(a.Bindings, b)
	x, path := openFixture(t, a)
	if x.sections[implementationFacts].n != 2 || x.field(implementationFacts, 0, 1) != x.field(implementationFacts, 1, 1) {
		t.Fatal("payload not shared")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	version, ok := x.strID("5")
	if !ok {
		t.Fatal("legacy worker string missing")
	}
	fact := x.field(implementationFacts, 1, 0)
	contextRow := x.field(facts, fact, 3)
	for row := uint64(0); row < x.sections[facts].n; row++ {
		if x.field(facts, row, 3) == contextRow {
			le.PutUint64(raw[x.sections[facts].off+row*widths[facts]+15*8:], version)
		}
	}
	le.PutUint64(raw[x.sections[contexts].off+contextRow*widths[contexts]+5*8:], version)
	sum := sha256.Sum256(raw[headerSize:])
	copy(raw[24:56], sum[:])
	p := filepath.Join(t.TempDir(), "downgrade")
	if e := os.WriteFile(p, raw, 0600); e != nil {
		t.Fatal(e)
	}
	bad, e := Open(p, provenance(), Limits{})
	if e == nil {
		bad.Close()
		t.Fatal("cached payload bypassed worker provenance")
	}
}

func TestImplementationRejectsConstructorDeclarationMutation(t *testing.T) {
	a := wrapperFixture()
	o, b := a.Occurrences[0], a.Bindings[0]
	o.Offset = 99
	o.Kind = "constructor_declaration"
	o.ID = o.ComputeID()
	b.OccurrenceID = o.ID
	b.ImplementationFacts = nil
	b.ID = b.ComputeID()
	a.Occurrences = append(a.Occurrences, o)
	a.Bindings = append(a.Bindings, b)
	x, path := openFixture(t, a)
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	kind, ok := x.strID("constructor_declaration")
	if !ok {
		t.Fatal("missing constructor kind")
	}
	fact := x.field(implementationFacts, 0, 0)
	le.PutUint64(raw[x.sections[facts].off+fact*widths[facts]+7*8:], kind)
	sum := sha256.Sum256(raw[headerSize:])
	copy(raw[24:56], sum[:])
	p := filepath.Join(t.TempDir(), "constructor")
	if e := os.WriteFile(p, raw, 0600); e != nil {
		t.Fatal(e)
	}
	bad, e := Open(p, provenance(), Limits{})
	if e == nil {
		bad.Close()
		t.Fatal("constructor admitted implementation fact")
	}
}

func TestContractPathsDoesNotPromoteDefaultTemplateToClassHop(t *testing.T) {
	a := wrapperFixture()
	remap := map[string]string{}
	for i, d := range map[int]string{1: "M:Service`1.IService#Send", 2: "T:Service`1"} {
		old := a.Symbols[i].ID
		a.Symbols[i].Key.Descriptor = d
		a.Symbols[i].ID = a.Symbols[i].ComputeID()
		remap[old] = a.Symbols[i].ID
	}
	a.Contexts[0].ExtractorVersion = "7"
	a.Contexts[0].ID = a.Contexts[0].ComputeID()
	for i := range a.Occurrences {
		a.Occurrences[i].ContextID = a.Contexts[0].ID
		a.Occurrences[i].ID = a.Occurrences[i].ComputeID()
		b := &a.Bindings[i]
		b.OccurrenceID = a.Occurrences[i].ID
		b.ExtractorVersion = "7"
		if id := remap[b.SymbolID]; id != "" {
			b.SymbolID = id
		}
		if id := remap[b.EnclosingSymbolID]; id != "" {
			b.EnclosingSymbolID = id
		}
		for j := range b.ImplementationFacts {
			b.ImplementationFacts[j].Rule = "csharp-interface-default-v1"
			b.ImplementationFacts[j].ImplementingTypeSymbolID = a.Symbols[2].ID
		}
		b.ID = b.ComputeID()
	}
	x, _ := openFixture(t, a)
	q, err := x.ContractPaths(context.Background(), a.Symbols[0].ID, []string{a.Contexts[0].ID}, 100)
	if err != nil || len(q.Seeds) != 1 || len(q.Paths) != 1 || q.Paths[0].Implementation != nil {
		t.Fatalf("default template promoted into interface hop: %+v %v", q, err)
	}
	reverse, err := x.Implementations(context.Background(), a.Symbols[3].ID, []string{a.Contexts[0].ID}, 100)
	if err != nil || len(reverse.Matches) != 1 {
		t.Fatalf("default template lost from explicit reverse evidence: %+v %v", reverse, err)
	}
}
