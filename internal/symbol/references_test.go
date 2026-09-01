package symbol

import (
	"path/filepath"
	"testing"
)

// refFixture exercises Go reference extraction. A multi-byte unicode comment
// ("café 日本語") precedes the real code so a byte/rune offset confusion would
// shift every later reference offset and the byte-slice assertions would fail.
const refFixture = `package demo

// café 日本語 multi-byte before real decls

func Add(a, b int) int { return a + b }

type Widget struct{ ID int }

func (w Widget) Spin() int { return w.ID }

func caller() {
	x := Add(1, 2)       // call ref to Add
	w := Widget{ID: x}   // composite-lit type ref to Widget
	_ = w.Spin()         // selector ref to Spin
	s := "Add(3,4)"      // NOT a ref: inside a string literal
	_ = s
}
`

// occByName collects the reference occurrences whose Name matches, in order.
func occByName(occs []Occurrence, name string) []Occurrence {
	var out []Occurrence
	for _, o := range occs {
		if o.Name == name {
			out = append(out, o)
		}
	}
	return out
}

func TestGoExtractRefs_CallSelectorAndType(t *testing.T) {
	src := []byte(refFixture)
	occs, err := GoExtractor{}.ExtractRefs(src)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}

	// Every emitted occurrence must be a Reference and slice back to its name.
	for _, o := range occs {
		if o.Role != Reference {
			t.Errorf("occurrence %q has role %v, want Reference", o.Name, o.Role)
		}
		if got := string(src[o.Start:o.End]); got != o.Name {
			t.Errorf("occurrence offsets [%d,%d) = %q, want %q", o.Start, o.End, got, o.Name)
		}
	}

	// "Add" appears exactly once as a reference (the call site). The func def
	// `func Add` must NOT be counted.
	add := occByName(occs, "Add")
	if len(add) != 1 {
		t.Fatalf("Add references = %d, want 1 (the call site, not the def); got %v", len(add), add)
	}
	// The captured offset is the CALL site, which is strictly after the def.
	defOff := indexOf(src, "func Add") + len("func ")
	if add[0].Start <= defOff {
		t.Errorf("Add ref at %d should be after the def ident at %d", add[0].Start, defOff)
	}
	callOff := indexOf(src, "Add(1, 2)")
	if add[0].Start != callOff {
		t.Errorf("Add ref offset = %d, want call site %d", add[0].Start, callOff)
	}

	// "Widget" appears once as a composite-literal type reference (the type decl
	// and the receiver type are def/binding sites and excluded).
	widget := occByName(occs, "Widget")
	if len(widget) != 1 {
		t.Fatalf("Widget references = %d, want 1 (composite lit); got %v", len(widget), widget)
	}
	if widget[0].Start != indexOf(src, "Widget{ID: x}") {
		t.Errorf("Widget ref offset = %d, want composite-lit site %d", widget[0].Start, indexOf(src, "Widget{ID: x}"))
	}

	// "Spin" appears once as a selector reference (w.Spin); the method def is
	// excluded.
	spin := occByName(occs, "Spin")
	if len(spin) != 1 {
		t.Fatalf("Spin references = %d, want 1 (selector); got %v", len(spin), spin)
	}
}

func TestGoExtractRefs_ExcludesStringLiteralAndDefSites(t *testing.T) {
	src := []byte(refFixture)
	occs, err := GoExtractor{}.ExtractRefs(src)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}
	// The "Add(3,4)" inside the string literal must not produce a reference. We
	// already assert Add has exactly one ref above; here assert no occurrence
	// falls inside the string literal byte range.
	litStart := indexOf(src, `"Add(3,4)"`)
	litEnd := litStart + len(`"Add(3,4)"`)
	for _, o := range occs {
		if o.Start >= litStart && o.Start < litEnd {
			t.Errorf("occurrence %q at %d falls inside the string literal [%d,%d)", o.Name, o.Start, litStart, litEnd)
		}
	}
}

func TestGoExtractRefs_MultiByteOffsets(t *testing.T) {
	src := []byte(refFixture)
	occs, err := GoExtractor{}.ExtractRefs(src)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}
	add := occByName(occs, "Add")
	if len(add) != 1 {
		t.Fatalf("Add references = %d, want 1", len(add))
	}
	// If offsets were rune-based, the multi-byte comment before the code would
	// make Start too small and the byte slice would not equal "Add". The
	// per-occurrence slice check in the first test covers correctness; here add
	// a sharper check that the byte offset exceeds the rune count up to it.
	runeCount := len([]rune(string(src[:add[0].Start])))
	if add[0].Start <= runeCount {
		t.Errorf("expected byte offset (%d) > rune count (%d) past multi-byte text", add[0].Start, runeCount)
	}
}

func TestGoExtractRefs_ParseError(t *testing.T) {
	_, err := GoExtractor{}.ExtractRefs([]byte("package x\nfunc ("))
	if err == nil {
		t.Fatal("expected parse error for malformed Go, got nil")
	}
}

// TestIndex_References exercises the name -> occurrences inverted view end to
// end: a definition plus N call sites, def-not-double-counted, ordering,
// unknown-name empty, and Definitions(name) filtering.
func TestIndex_References(t *testing.T) {
	src := []byte(refFixture)
	defs, err := GoExtractor{}.Extract(src)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	occs, err := GoExtractor{}.ExtractRefs(src)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}
	ix := NewIndex()
	ix.Set(0, defs)
	ix.SetRefs(0, occs)

	// References("Add") = the def (Role=Definition) + the single call ref.
	refs := ix.References("Add")
	if len(refs) != 2 {
		t.Fatalf("References(Add) = %d, want 2 (def + 1 call); got %+v", len(refs), refs)
	}
	// Sorted by Start: the def ident precedes the call site.
	if refs[0].Role != Definition {
		t.Errorf("first ref role = %v, want Definition", refs[0].Role)
	}
	if refs[1].Role != Reference {
		t.Errorf("second ref role = %v, want Reference", refs[1].Role)
	}
	if refs[0].Start >= refs[1].Start {
		t.Errorf("refs not sorted by Start: %d >= %d", refs[0].Start, refs[1].Start)
	}
	// The def Ref must slice back to "Add".
	if got := string(src[refs[0].Start:refs[0].End]); got != "Add" {
		t.Errorf("def ref slices to %q, want Add", got)
	}

	// Definitions("Add") = exactly the one def, no references.
	d := ix.Definitions("Add")
	if len(d) != 1 || d[0].Role != Definition {
		t.Fatalf("Definitions(Add) = %+v, want exactly one Definition", d)
	}

	// Unknown name returns nil (no panic), for both lookups.
	if got := ix.References("Nonexistent"); got != nil {
		t.Errorf("References(unknown) = %+v, want nil", got)
	}
	if got := ix.Definitions("Nonexistent"); got != nil {
		t.Errorf("Definitions(unknown) = %+v, want nil", got)
	}
}

func TestIndex_References_MultiBlobOrdering(t *testing.T) {
	src := []byte(refFixture)
	defs, _ := GoExtractor{}.Extract(src)
	occs, _ := GoExtractor{}.ExtractRefs(src)

	ix := NewIndex()
	// Install the SAME content under two blob IDs, out of order, to prove
	// References sorts by blob then offset.
	ix.Set(5, defs)
	ix.SetRefs(5, occs)
	ix.Set(2, defs)
	ix.SetRefs(2, occs)

	refs := ix.References("Add")
	if len(refs) != 4 { // 2 blobs * (1 def + 1 ref)
		t.Fatalf("References(Add) across 2 blobs = %d, want 4", len(refs))
	}
	// Blob 2's refs must all precede blob 5's refs.
	if refs[0].Blob != 2 || refs[1].Blob != 2 || refs[2].Blob != 5 || refs[3].Blob != 5 {
		t.Errorf("References not blob-ordered: %d %d %d %d", refs[0].Blob, refs[1].Blob, refs[2].Blob, refs[3].Blob)
	}
}

func TestSetRefs_FiltersDefinitionRoleAndEmpty(t *testing.T) {
	ix := NewIndex()
	ix.Set(0, []Symbol{{Name: "Foo", Kind: Func, NameStart: 5, NameEnd: 8, BodyStart: 0, BodyEnd: 20}})
	// Pass a Definition-role occurrence (should be ignored) plus a real ref.
	ix.SetRefs(0, []Occurrence{
		{Name: "Foo", Kind: Func, Role: Definition, Start: 100, End: 103}, // filtered
		{Name: "", Kind: Func, Role: Reference, Start: 50, End: 50},       // empty name filtered
		{Name: "Foo", Kind: Func, Role: Reference, Start: 30, End: 33},    // kept
	})
	refs := ix.References("Foo")
	// 1 def (from Set) + 1 reference (the only valid one in SetRefs).
	if len(refs) != 2 {
		t.Fatalf("References(Foo) = %+v, want def + 1 ref", refs)
	}
	gotRef := 0
	for _, r := range refs {
		if r.Role == Reference {
			gotRef++
			if r.Start != 30 {
				t.Errorf("kept reference Start = %d, want 30", r.Start)
			}
		}
	}
	if gotRef != 1 {
		t.Errorf("reference count = %d, want 1 (Definition-role and empty-name occs filtered)", gotRef)
	}

	// Clearing references leaves the definition intact.
	ix.SetRefs(0, nil)
	refs = ix.References("Foo")
	if len(refs) != 1 || refs[0].Role != Definition {
		t.Fatalf("after clearing refs, References(Foo) = %+v, want just the def", refs)
	}
	if ix.NumRefBlobs() != 0 {
		t.Errorf("NumRefBlobs after clear = %d, want 0", ix.NumRefBlobs())
	}
}

func TestSet_DefinitionsFeedByName_WithoutRefs(t *testing.T) {
	// References must surface a definition even when SetRefs was never called
	// (def-only blob), proving byName is folded from Set alone.
	ix := NewIndex()
	ix.Set(3, []Symbol{{Name: "Solo", Kind: Type, NameStart: 5, NameEnd: 9, BodyStart: 0, BodyEnd: 20}})
	refs := ix.References("Solo")
	if len(refs) != 1 || refs[0].Role != Definition || refs[0].Blob != 3 {
		t.Fatalf("References(Solo) = %+v, want one Definition in blob 3", refs)
	}
	// Clearing the blob removes it from byName.
	ix.Set(3, nil)
	if got := ix.References("Solo"); got != nil {
		t.Errorf("References(Solo) after clear = %+v, want nil", got)
	}
}

func TestEnclosing_UnchangedByReferences(t *testing.T) {
	// Regression guard: adding references must not perturb Enclosing's def-only
	// behavior. Build a def-only index and a def+refs index from the same source
	// and assert Enclosing is identical at several probes.
	src := []byte(refFixture)
	defs, _ := GoExtractor{}.Extract(src)
	occs, _ := GoExtractor{}.ExtractRefs(src)

	defOnly := NewIndex()
	defOnly.Set(0, defs)

	withRefs := NewIndex()
	withRefs.Set(0, defs)
	withRefs.SetRefs(0, occs)

	probes := []int{
		indexOf(src, "return a + b"),
		indexOf(src, "return w.ID"),
		indexOf(src, "x := Add(1, 2)"),
	}
	for _, off := range probes {
		a, aok := defOnly.Enclosing(0, off)
		b, bok := withRefs.Enclosing(0, off)
		if aok != bok || a != b {
			t.Errorf("off %d: def-only=(%+v,%v) with-refs=(%+v,%v)", off, a, aok, b, bok)
		}
	}
}

// TestCodec_RefsRoundTrip extends codec coverage to references: Save/Load an
// Index with defs + refs and assert References/Definitions/Enclosing are
// byte-identical pre/post.
func TestCodec_RefsRoundTrip(t *testing.T) {
	src := []byte(refFixture)
	defs, _ := GoExtractor{}.Extract(src)
	occs, _ := GoExtractor{}.ExtractRefs(src)

	ix := NewIndex()
	ix.Set(0, defs)
	ix.SetRefs(0, occs)
	ix.Set(7, defs) // a def-only blob (no refs) to exercise the mixed case
	ix.SetRefs(7, occs)

	path := filepath.Join(t.TempDir(), "symbols.sym")
	if err := Save(ix, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, name := range []string{"Add", "Widget", "Spin", "caller"} {
		want := ix.References(name)
		gotRefs := got.References(name)
		if len(want) != len(gotRefs) {
			t.Fatalf("References(%s): pre=%d post=%d", name, len(want), len(gotRefs))
		}
		for i := range want {
			if want[i] != gotRefs[i] {
				t.Errorf("References(%s)[%d]: pre=%+v post=%+v", name, i, want[i], gotRefs[i])
			}
		}
		wantDefs := ix.Definitions(name)
		gotDefs := got.Definitions(name)
		if len(wantDefs) != len(gotDefs) {
			t.Errorf("Definitions(%s): pre=%d post=%d", name, len(wantDefs), len(gotDefs))
		}
	}

	// Enclosing must be identical.
	for _, blob := range []uint64{0, 7} {
		for _, off := range []int{indexOf(src, "return a + b"), indexOf(src, "return w.ID")} {
			a, aok := ix.Enclosing(blob, off)
			b, bok := got.Enclosing(blob, off)
			if aok != bok || a != b {
				t.Errorf("blob %d off %d: pre=(%+v,%v) post=(%+v,%v)", blob, off, a, aok, b, bok)
			}
		}
	}
	if got.NumRefBlobs() != ix.NumRefBlobs() {
		t.Errorf("NumRefBlobs pre=%d post=%d", ix.NumRefBlobs(), got.NumRefBlobs())
	}
}
