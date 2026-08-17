package candidates

import (
	"reflect"
	"testing"
)

// TestGenerateAll_Sweep covers the corpus-wide fan-out: what it admits, what it
// leaves out, and that it says so. A candidate store that cannot distinguish "no
// name in this corpus references anything" from "the filter excluded every name"
// is not usable as a verification-pass input.
func TestGenerateAll_Sweep(t *testing.T) {
	c := build(t, orders, billing, shipping, nearby, vendor)

	set := GenerateAll(c, Options{})
	if set.Len() == 0 {
		t.Fatal("GenerateAll produced no candidates")
	}

	sweep := set.Sweep()
	if got, want := sweep.Names, c.Symbols().NumNames(); got != want {
		t.Errorf("Sweep().Names = %d, want every name in the corpus index (%d)", got, want)
	}
	if sweep.Generated+sweep.Excluded != sweep.Names {
		t.Errorf("Sweep() = %+v: generated + excluded must account for every name", sweep)
	}
	if sweep.Excluded == 0 {
		t.Error("Sweep().Excluded = 0, want the unexported names left out")
	}

	names := set.Names()
	has := func(name string) bool {
		for _, n := range names {
			if n == name {
				return true
			}
		}
		return false
	}
	if !has("ProcessOrder") {
		t.Errorf("Names() = %v, want ProcessOrder", names)
	}
	// preProcessOrder is a real definition with a real mention, so it is absent
	// only because the default policy excludes unexported names.
	if has("preProcessOrder") {
		t.Errorf("Names() = %v, want preProcessOrder excluded by the default Exported policy", names)
	}
	// Charge is exported and defined, but nothing else defines or uses it, so its
	// only occurrence is its own declaration — which is not a use of itself.
	if has("Charge") {
		t.Errorf("Names() = %v, want Charge absent (its only occurrence is its own definition)", names)
	}

	// The store must return exactly what per-name generation returns; the sweep is
	// a fan-out over GenerateCandidates, not a second implementation.
	if got, want := set.ForName("ProcessOrder"), GenerateCandidates(c, "ProcessOrder"); !reflect.DeepEqual(got, want) {
		t.Errorf("ForName(ProcessOrder) differs from GenerateCandidates:\n got %+v\nwant %+v", got, want)
	}
	total := 0
	for _, n := range names {
		total += len(set.ForName(n))
	}
	if total != set.Len() {
		t.Errorf("by-name index covers %d edges, store holds %d", total, set.Len())
	}

	// Sweeping twice must give byte-identical results: the name index iterates in
	// map order, so the sort inside GenerateAll is load-bearing.
	if again := GenerateAll(c, Options{}); !reflect.DeepEqual(again.Edges(), set.Edges()) {
		t.Error("two sweeps of the same corpus produced different candidate lists")
	}
	if got := GenerateAll(nil, Options{}); got == nil || got.Len() != 0 {
		t.Errorf("GenerateAll(nil) = %+v, want an empty store", got)
	}
}

// TestGenerateAll_Options proves both gates are real knobs and not just defaults:
// the length floor keeps a corpus-wide sweep off sub-trigram names (whose fan-out
// degenerates to a full content scan per name), and Include replaces the exported
// policy entirely.
func TestGenerateAll_Options(t *testing.T) {
	short := build(t,
		fixture{"repo-tiny", "tiny.go", "package tiny\n\nfunc Do() {}\n"},
		fixture{"repo-user", "user.go", "package user\n\nfunc Run() { Do() }\n"},
	)

	if got := GenerateAll(short, Options{}).Names(); len(got) != 0 {
		t.Errorf("default sweep produced %v, want nothing (Do is shorter than a trigram)", got)
	}
	if got := GenerateAll(short, Options{MinNameLen: 1}).Names(); !reflect.DeepEqual(got, []string{"Do"}) {
		t.Errorf("MinNameLen=1 sweep produced %v, want [Do]", got)
	}

	c := build(t, orders, nearby)
	only := GenerateAll(c, Options{Include: func(name string) bool { return name == "preProcessOrder" }})
	if got := only.Names(); !reflect.DeepEqual(got, []string{"preProcessOrder"}) {
		t.Errorf("Include predicate produced %v, want [preProcessOrder] (an unexported name the default policy drops)", got)
	}
	if got := only.Sweep().Generated; got != 1 {
		t.Errorf("Sweep().Generated = %d, want 1", got)
	}
}

// TestSet_Store covers the in-memory store on its own terms — the handoff surface
// phase 4 consumes.
func TestSet_Store(t *testing.T) {
	s := NewSet()
	if s.Len() != 0 || s.Names() != nil || s.ForName("x") != nil || s.Sweep() != (Sweep{}) {
		t.Fatalf("NewSet is not empty: len=%d names=%v", s.Len(), s.Names())
	}

	beta := Edge{Name: "Beta", Source: Site{Shard: 1, Blob: 2, Start: 3, End: 7}, Target: Site{Shard: 0}, Type: SymbolReference}
	alphaOne := Edge{Name: "Alpha", Source: Site{Shard: 2}, Target: Site{Shard: 0}, Type: TextOccurrence}
	alphaTwo := Edge{Name: "Alpha", Source: Site{Shard: 3}, Target: Site{Shard: 0}, Type: SiblingDefinition}

	s.Add()
	s.Add(beta, alphaOne)
	s.Add(alphaTwo)

	if got := s.Len(); got != 3 {
		t.Errorf("Len = %d, want 3", got)
	}
	if got := s.Names(); !reflect.DeepEqual(got, []string{"Alpha", "Beta"}) {
		t.Errorf("Names = %v, want sorted [Alpha Beta]", got)
	}
	if got := s.Edges(); !reflect.DeepEqual(got, []Edge{beta, alphaOne, alphaTwo}) {
		t.Errorf("Edges = %+v, want insertion order", got)
	}
	if got := s.ForName("Alpha"); !reflect.DeepEqual(got, []Edge{alphaOne, alphaTwo}) {
		t.Errorf("ForName(Alpha) = %+v, want both Alpha edges in insertion order", got)
	}

	// ForName hands back a copy so a verifier can annotate its work list without
	// mutating the store other verifiers are reading.
	work := s.ForName("Alpha")
	work[0].Type = TypeUnknown
	if got := s.ForName("Alpha")[0].Type; got != TextOccurrence {
		t.Errorf("mutating a ForName result changed the store: type is now %v", got)
	}
	if got := s.ForName("Gamma"); got != nil {
		t.Errorf("ForName(unknown) = %+v, want nil", got)
	}
}

// TestExported pins the cross-language exported-name convention, including the
// non-ASCII case: the check is on the first RUNE, not the first byte, so a
// multi-byte upper-case letter is not read as a lower-case one.
func TestExported(t *testing.T) {
	cases := map[string]bool{
		"ProcessOrder":  true,
		"P":             true,
		"Ünicode":       true,
		"processOrder":  false,
		"ünicode":       false,
		"_private":      false,
		"9lives":        false,
		"":              false,
		"PROCESS_ORDER": true,
	}
	for name, want := range cases {
		if got := Exported(name); got != want {
			t.Errorf("Exported(%q) = %v, want %v", name, got, want)
		}
	}
}
