package semanticindex

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"moedex/internal/semantic"
)

func callFixture() *semantic.Artifact {
	a := wrapperFixture()
	a.Occurrences = nil
	a.Bindings = nil
	// Same method name in separate qualified owners must stay separate.
	for _, d := range []string{"M:Other.Send", "M:Chain.Step"} {
		s := a.Symbols[1]
		s.Key.Descriptor = d
		s.ID = s.ComputeID()
		a.Symbols = append(a.Symbols, s)
	}
	addCall(a, 10, 4, 1, "invocation")  // Controller -> Service
	addCall(a, 20, 1, 8, "invocation")  // Service -> Chain
	addCall(a, 30, 8, 4, "invocation")  // Chain -> Controller cycle
	addCall(a, 40, 5, 7, "invocation")  // unrelated caller of Other.Send
	addCall(a, 50, 4, 7, "name")        // method group, not a call
	addCall(a, 60, 4, 7, "constructor") // object creation, separate evidence
	addCall(a, 70, 2, 1, "invocation")  // non-method owner, excluded
	return a
}
func addCall(a *semantic.Artifact, offset uint64, owner, target int, kind string) {
	c := a.Contexts[0]
	o := semantic.Occurrence{SourceID: a.Sources[0].ID, ContextID: c.ID, Role: "reference", Kind: kind, Offset: offset, Length: 1}
	o.ID = o.ComputeID()
	b := semantic.Binding{OccurrenceID: o.ID, Status: "resolved", SymbolID: a.Symbols[target].ID, EnclosingSymbolID: a.Symbols[owner].ID, Method: "roslyn-semantic-model", Extractor: c.Extractor, ExtractorVersion: c.ExtractorVersion}
	b.ID = b.ComputeID()
	a.Occurrences = append(a.Occurrences, o)
	a.Bindings = append(a.Bindings, b)
}
func TestCallTraceExactHomonymsCyclesAndOwnedWitnesses(t *testing.T) {
	a := callFixture()
	x, _ := openFixture(t, a)
	ids := []string{a.Contexts[0].ID}
	for _, direction := range []string{"inbound", "outbound", "both"} {
		q, err := x.TraceCalls(context.Background(), a.Symbols[1].ID, ids, direction, 8, 100)
		if err != nil || !q.Supported || q.Truncated || len(q.Edges) != 3 {
			t.Fatalf("%s %+v %v", direction, q, err)
		}
		for _, e := range q.Edges {
			if e.Caller.ID == a.Symbols[7].ID || e.Target.ID == a.Symbols[7].ID || e.Depth > 3 {
				t.Fatal("homonym or depth joined")
			}
		}
	}
	q, err := x.TraceCalls(context.Background(), a.Symbols[1].ID, ids, "outbound", 1, 100)
	if err != nil || len(q.Edges) != 1 || q.Edges[0].Target.ID != a.Symbols[8].ID {
		t.Fatal(q, err)
	}
	in, err := x.TraceCalls(context.Background(), a.Symbols[1].ID, ids, "inbound", 1, 100)
	if err != nil || len(in.Edges) != 1 || in.Edges[0].Caller.ID != a.Symbols[4].ID {
		t.Fatal(in, err)
	}
	other, err := x.TraceCalls(context.Background(), a.Symbols[7].ID, ids, "inbound", 8, 100)
	if err != nil || len(other.Edges) != 1 || other.Edges[0].Caller.ID != a.Symbols[5].ID {
		t.Fatal(other, err)
	}
	x.Close()
	if q.Root.Key.Descriptor != "M:Service.Send" || q.Edges[0].Witness.Source.Path != "src/A.cs" || q.Edges[0].Target.Key.Descriptor != "M:Chain.Step" {
		t.Fatal("unowned result")
	}
}

func TestCallTraceExcludesNamelessAnonymousDocumentationIDs(t *testing.T) {
	a := callFixture()
	s := a.Symbols[4]
	s.Key.Descriptor = "M:Controller."
	s.ID = s.ComputeID()
	a.Symbols = append(a.Symbols, s)
	addCall(a, 99, len(a.Symbols)-1, 1, "invocation")
	x, _ := openFixture(t, a)
	q, err := x.TraceCalls(context.Background(), a.Symbols[1].ID, []string{a.Contexts[0].ID}, "inbound", 1, 100)
	if err != nil || len(q.Edges) != 1 || q.Edges[0].Caller.ID != a.Symbols[4].ID {
		t.Fatal(q, err)
	}
	if _, err := x.TraceCalls(context.Background(), s.ID, []string{a.Contexts[0].ID}, "both", 1, 100); err == nil {
		t.Fatal("nameless root admitted")
	}
}
func TestCallTraceSelectionExclusionsAndBounds(t *testing.T) {
	a := callFixture()
	addCall(a, 80, 4, 7, "invocation")
	b := &a.Bindings[len(a.Bindings)-1]
	b.Status = "unresolved"
	b.SymbolID = ""
	b.ID = b.ComputeID()
	second := a.Contexts[0]
	second.Project = "Other.csproj"
	second.ID = second.ComputeID()
	a.Contexts = append(a.Contexts, second)
	o, binding := a.Occurrences[3], a.Bindings[3]
	o.ContextID = second.ID
	o.Offset = 90
	o.ID = o.ComputeID()
	binding.OccurrenceID = o.ID
	binding.SymbolID = a.Symbols[1].ID
	binding.ID = binding.ComputeID()
	a.Occurrences = append(a.Occurrences, o)
	a.Bindings = append(a.Bindings, binding)
	x, _ := openFixture(t, a)
	ids := []string{a.Contexts[0].ID}
	q, e := x.TraceCalls(context.Background(), a.Symbols[1].ID, ids, "inbound", 1, 100)
	if e != nil || len(q.Edges) != 1 {
		t.Fatal(q, e)
	}
	q, e = x.TraceCalls(context.Background(), a.Symbols[1].ID, []string{second.ID}, "inbound", 1, 100)
	if e != nil || len(q.Edges) != 1 || q.Edges[0].Witness.Context.ID != second.ID {
		t.Fatal(q, e)
	}
	q, e = x.TraceCalls(context.Background(), a.Symbols[1].ID, ids, "outbound", 8, 1)
	if e != nil || len(q.Edges) != 1 || !q.Truncated {
		t.Fatal(q, e)
	}
	for _, contexts := range [][]string{nil, {a.Contexts[0].ID, a.Contexts[0].ID}, {"context:" + hash("unknown")}} {
		if _, e := x.TraceCalls(context.Background(), a.Symbols[1].ID, contexts, "outbound", 1, 10); !errors.Is(e, ErrContractContextSelection) {
			t.Fatal(e)
		}
	}
	for _, args := range []struct {
		dir          string
		depth, limit int
	}{{"sideways", 1, 20}, {"both", 0, 20}, {"both", 9, 20}, {"both", 1, 0}} {
		if _, e := x.TraceCalls(context.Background(), a.Symbols[1].ID, ids, args.dir, args.depth, args.limit); e == nil {
			t.Fatal("invalid arguments admitted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := x.TraceCalls(ctx, a.Symbols[1].ID, ids, "both", 1, 20); e != context.Canceled {
		t.Fatal(e)
	}
}
func TestCallerPostingsCorruptionAndV5Compatibility(t *testing.T) {
	a := callFixture()
	x, path := openFixture(t, a)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func([]byte){
		func(b []byte) { le.PutUint64(b[x.sections[callerPostings].off:], x.sections[facts].n) },
		func(b []byte) {
			copy(b[x.sections[callerPostings].off:], b[x.sections[callerPostings].off+8:x.sections[callerPostings].off+16])
		},
	} {
		b := append([]byte(nil), raw...)
		mutate(b)
		sum := sha256.Sum256(b[headerSize:])
		copy(b[24:56], sum[:])
		p := filepath.Join(t.TempDir(), "bad")
		os.WriteFile(p, b, 0600)
		if bad, e := Open(p, provenance(), Limits{}); e == nil {
			bad.Close()
			t.Fatal("corrupt directory admitted")
		}
	}
	old := append(append([]byte(nil), raw[:interfaceHeaderSize]...), raw[headerSize:x.sections[callerPostings].off]...)
	le.PutUint64(old[8:], 5)
	le.PutUint64(old[16:], uint64(len(old)))
	clear(old[120+interfaceSectionCount*16 : interfaceHeaderSize])
	for i := 0; i < interfaceSectionCount; i++ {
		at := 120 + i*16
		le.PutUint64(old[at:], le.Uint64(old[at:])-uint64(headerSize-interfaceHeaderSize))
	}
	sum := sha256.Sum256(old[interfaceHeaderSize:])
	copy(old[24:56], sum[:])
	p := filepath.Join(t.TempDir(), "v5")
	os.WriteFile(p, old, 0600)
	legacy, e := Open(p, provenance(), Limits{})
	if e != nil {
		t.Fatal(e)
	}
	defer legacy.Close()
	q, e := legacy.TraceCalls(context.Background(), a.Symbols[1].ID, []string{a.Contexts[0].ID}, "inbound", 1, 100)
	if e != nil || q.Supported {
		t.Fatal("legacy calls fabricated", e)
	}
	pos := Position{Repo: "repo", Path: "src/A.cs", Offset: 10}
	before, e := x.Bindings(context.Background(), pos, 1)
	if e != nil {
		t.Fatal(e)
	}
	after, e := legacy.Bindings(context.Background(), pos, 1)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("legacy bindings changed", e)
	}
}

func TestCallTraceWorkCeilingChargesSkippedOwners(t *testing.T) {
	a := callFixture()
	a.Occurrences = nil
	a.Bindings = nil
	for i := 0; i <= ContractWorkLimit; i++ {
		addCall(a, uint64(i*2), 2, 1, "invocation")
	}
	x, _ := openFixture(t, a)
	q, e := x.TraceCalls(context.Background(), a.Symbols[1].ID, []string{a.Contexts[0].ID}, "inbound", 8, 100)
	if e != nil || q.RowsVisited != ContractWorkLimit || !q.Truncated || len(q.Edges) != 0 {
		t.Fatal(q, e)
	}
}

func TestCallTraceMaterializationCeilingStopsBeforeLargeDescriptor(t *testing.T) {
	a := callFixture()
	old := a.Symbols[8].ID
	a.Symbols[8].Key.Descriptor = "M:Chain." + strings.Repeat("A", MaxQueryBytes)
	a.Symbols[8].ID = a.Symbols[8].ComputeID()
	for i := range a.Bindings {
		b := &a.Bindings[i]
		if b.SymbolID == old {
			b.SymbolID = a.Symbols[8].ID
		}
		if b.EnclosingSymbolID == old {
			b.EnclosingSymbolID = a.Symbols[8].ID
		}
		b.ID = b.ComputeID()
	}
	x, _ := openFixture(t, a)
	q, err := x.TraceCalls(context.Background(), a.Symbols[1].ID, []string{a.Contexts[0].ID}, "outbound", 8, 100)
	if err != nil || !q.Truncated || len(q.Edges) != 0 || q.RowsVisited != 1 {
		t.Fatal(q, err)
	}
	if _, err := x.TraceCalls(context.Background(), a.Symbols[8].ID, []string{a.Contexts[0].ID}, "both", 1, 100); err == nil {
		t.Fatal("oversized root admitted")
	}
}
