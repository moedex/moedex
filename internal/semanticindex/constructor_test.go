package semanticindex

import (
	"context"
	"path/filepath"
	"testing"

	"moedex/internal/semantic"
)

func TestOverlappingPrimaryConstructorDeclarations(t *testing.T) {
	a := fixture(1)
	constructor := a.Symbols[0]
	constructor.Key.Descriptor = "M:A.#ctor(System.Int32)"
	constructor.ID = constructor.ComputeID()
	a.Symbols = append(a.Symbols, constructor)
	o := a.Occurrences[0]
	o.Kind = "constructor_declaration"
	o.ID = o.ComputeID()
	if o.ID == a.Occurrences[0].ID {
		t.Fatal("primary constructor erased overlapping type occurrence")
	}
	b := a.Bindings[0]
	b.OccurrenceID, b.SymbolID = o.ID, constructor.ID
	b.ID = b.ComputeID()
	a.Occurrences = append(a.Occurrences, o)
	a.Bindings = append(a.Bindings, b)
	p := filepath.Join(t.TempDir(), "artifact")
	if err := semantic.Write(p, a); err != nil {
		t.Fatal(err)
	}
	a, err := semantic.Read(p, semantic.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	x, _ := openFixture(t, a)
	got, err := x.Bindings(context.Background(), Position{Repo: "repo", Path: "src/A.cs", Offset: 0}, 100)
	if err != nil || len(got.Results) != 2 {
		t.Fatalf("overlapping declarations: %#v %v", got, err)
	}
	defs, err := x.Definitions(context.Background(), constructor.ID, Filter{}, 100)
	if err != nil || len(defs.Results) != 1 || defs.Results[0].Occurrence.Kind != "constructor_declaration" {
		t.Fatalf("constructor definitions: %#v %v", defs, err)
	}
	for _, pair := range [][2]string{{"reference", "constructor_declaration"}, {"declaration", "constructor"}} {
		bad := fixture(1)
		bad.Occurrences[0].Role, bad.Occurrences[0].Kind = pair[0], pair[1]
		bad.Occurrences[0].ID = bad.Occurrences[0].ComputeID()
		bad.Bindings[0].OccurrenceID = bad.Occurrences[0].ID
		bad.Bindings[0].ID = bad.Bindings[0].ComputeID()
		if bad.Validate() == nil {
			t.Fatalf("invalid role/kind accepted: %v", pair)
		}
	}
}
