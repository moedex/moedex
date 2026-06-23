package symbol

import (
	"path/filepath"
	"testing"

	"moedex/internal/index"
)

// fixture is a Go source file exercising: a top-level func, a method with a
// receiver, a type decl, a const, a var, AND a nested func literal — plus a
// multi-byte unicode comment ("café", "日本語") BEFORE the offsets we assert, so a
// byte/rune confusion in offset mapping would shift every later range.
const fixture = `package demo

// greet returns a salutation. // café 日本語 multi-byte before real decls
func greet(name string) string {
	hello := func(n string) string { // nested literal
		return "hi " + n
	}
	return hello(name)
}

type Widget struct {
	ID int
}

func (w Widget) Spin() int {
	return w.ID
}

const Limit = 10

var counter int
`

func extract(t *testing.T) ([]Symbol, []byte) {
	t.Helper()
	syms, err := GoExtractor{}.Extract([]byte(fixture))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return syms, []byte(fixture)
}

// find returns the first symbol with the given name and kind.
func find(syms []Symbol, name string, kind Kind) (Symbol, bool) {
	for _, s := range syms {
		if s.Name == name && s.Kind == kind {
			return s, true
		}
	}
	return Symbol{}, false
}

func TestExtract_KindsAndByteRanges(t *testing.T) {
	syms, src := extract(t)

	cases := []struct {
		name string
		kind Kind
	}{
		{"greet", Func},
		{"Spin", Method},
		{"Widget", Type},
		{"Limit", Const},
		{"counter", Var},
	}
	for _, c := range cases {
		s, ok := find(syms, c.name, c.kind)
		if !ok {
			t.Fatalf("missing symbol %s (%s)", c.name, c.kind)
		}
		// Name range must map back to the exact identifier text.
		if got := string(src[s.NameStart:s.NameEnd]); got != c.name {
			t.Errorf("%s: NameStart:NameEnd = %q, want %q", c.name, got, c.name)
		}
		// Body range must be sane and contain the name.
		if s.BodyStart > s.NameStart || s.NameEnd > s.BodyEnd {
			t.Errorf("%s: name [%d,%d) not inside body [%d,%d)", c.name, s.NameStart, s.NameEnd, s.BodyStart, s.BodyEnd)
		}
	}

	// The method body must map back to source that mentions the receiver work.
	spin, _ := find(syms, "Spin", Method)
	body := string(src[spin.BodyStart:spin.BodyEnd])
	if !contains(body, "func (w Widget) Spin()") || !contains(body, "return w.ID") {
		t.Errorf("Spin body did not map to its source span:\n%s", body)
	}
}

func TestExtract_NestedFuncLit(t *testing.T) {
	syms, src := extract(t)

	// The nested literal has empty name; find it by locating the source token.
	var lit Symbol
	found := false
	for _, s := range syms {
		if s.Name == "" && s.Kind == Func {
			if contains(string(src[s.BodyStart:s.BodyEnd]), `return "hi " + n`) {
				lit = s
				found = true
			}
		}
	}
	if !found {
		t.Fatal("nested func literal not extracted")
	}

	// The literal must be strictly inside greet.
	greet, _ := find(syms, "greet", Func)
	if !(lit.BodyStart > greet.BodyStart && lit.BodyEnd < greet.BodyEnd) {
		t.Errorf("literal [%d,%d) not nested inside greet [%d,%d)", lit.BodyStart, lit.BodyEnd, greet.BodyStart, greet.BodyEnd)
	}
}

func TestEnclosing_Innermost(t *testing.T) {
	syms, src := extract(t)
	ix := NewIndex()
	ix.Set(0, syms)

	greet, _ := find(syms, "greet", Func)

	// An offset inside the nested literal body resolves to the literal (the
	// innermost), not greet.
	litBodyOff := indexOf(src, `return "hi " + n`)
	if litBodyOff < 0 {
		t.Fatal("could not locate literal body in fixture")
	}
	inner, ok := ix.Enclosing(0, litBodyOff)
	if !ok {
		t.Fatal("Enclosing returned no symbol inside literal")
	}
	if inner.Name != "" || inner.BodyStart <= greet.BodyStart || inner.BodyEnd >= greet.BodyEnd {
		t.Errorf("expected innermost literal, got %q [%d,%d)", inner.Name, inner.BodyStart, inner.BodyEnd)
	}

	// An offset inside greet but BETWEEN the literal and greet's end (the final
	// "return hello(name)") resolves to greet, not the literal.
	betweenOff := indexOf(src, "return hello(name)")
	if betweenOff < 0 {
		t.Fatal("could not locate greet tail in fixture")
	}
	outer, ok := ix.Enclosing(0, betweenOff)
	if !ok {
		t.Fatal("Enclosing returned no symbol for greet tail")
	}
	if outer.Name != "greet" {
		t.Errorf("expected greet for tail offset, got %q", outer.Name)
	}

	// An offset in the top-level whitespace between decls resolves to nothing.
	gapOff := indexOf(src, "type Widget") - 1 // the blank line before Widget
	if _, ok := ix.Enclosing(0, gapOff); ok {
		t.Errorf("expected no enclosing symbol in top-level gap at %d", gapOff)
	}
}

func TestExtract_UnicodeOffsetsAreBytes(t *testing.T) {
	syms, src := extract(t)
	// "café 日本語" appears in a comment before all real decls. If offsets were
	// rune-based, greet's NameStart would be too small (the multi-byte chars
	// would each count as 1). Assert the name maps to the literal bytes — the
	// only way it can match "greet" is if offsets are byte offsets.
	greet, _ := find(syms, "greet", Func)
	if string(src[greet.NameStart:greet.NameEnd]) != "greet" {
		t.Errorf("unicode byte-offset hazard: greet name mapped to %q", string(src[greet.NameStart:greet.NameEnd]))
	}
	// Sanity: the byte offset is larger than the rune count up to it would be.
	runeCount := len([]rune(string(src[:greet.NameStart])))
	if greet.NameStart <= runeCount {
		t.Errorf("expected byte offset (%d) > rune count (%d) past multi-byte text", greet.NameStart, runeCount)
	}
}

// docFixture has a doc comment on greet, a doc comment on the Widget type, and
// an UNdocumented func (bare) so we can assert the bare func's BodyStart is its
// own keyword (unaffected by the doc-inclusion rule).
const docFixture = `package demo

// greet returns a salutation.
// Second line of the doc comment.
func greet(name string) string {
	return "hi " + name
}

// Widget is a thing.
type Widget struct {
	ID int
}

func bare() int {
	return 0
}
`

// TestExtract_DocCommentInRange asserts the doc-comment fix: a documented decl's
// BodyStart now points at the first byte of its doc comment group, so an offset
// landing inside the doc comment resolves via Enclosing to that symbol. Before
// the fix BodyStart was the func/type keyword and the doc-comment offset fell
// before BodyStart, so Enclosing missed.
func TestExtract_DocCommentInRange(t *testing.T) {
	src := []byte(docFixture)
	syms, err := GoExtractor{}.Extract(src)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	ix := NewIndex()
	ix.Set(0, syms)

	greet, ok := find(syms, "greet", Func)
	if !ok {
		t.Fatal("missing greet")
	}
	// BodyStart must now be the start of the doc comment, i.e. the range begins
	// with "// greet", not "func greet".
	gotStart := string(src[greet.BodyStart:greet.NameStart])
	if !contains(gotStart, "// greet returns a salutation.") {
		t.Errorf("greet BodyStart did not include doc comment; prefix before name = %q", gotStart)
	}
	// The whole body must still extend to the closing brace.
	if !contains(string(src[greet.BodyStart:greet.BodyEnd]), "return \"hi \" + name") {
		t.Errorf("greet body lost its tail: %q", string(src[greet.BodyStart:greet.BodyEnd]))
	}

	// The key behavior: an offset on a doc-comment line resolves to greet.
	docOff := indexOf(src, "Second line of the doc comment.")
	if docOff < 0 {
		t.Fatal("could not locate doc comment line")
	}
	s, ok := ix.Enclosing(0, docOff)
	if !ok {
		t.Fatal("Enclosing missed the doc-comment offset (the bug this fix addresses)")
	}
	if s.Name != "greet" {
		t.Errorf("doc-comment offset resolved to %q, want greet", s.Name)
	}

	// A documented type behaves the same way.
	widgetDocOff := indexOf(src, "Widget is a thing.")
	if w, ok := ix.Enclosing(0, widgetDocOff); !ok || w.Name != "Widget" {
		t.Errorf("Widget doc-comment offset resolved to (%+v,%v), want Widget", w, ok)
	}

	// The UNdocumented func's BodyStart must be its own keyword, not widened.
	bare, ok := find(syms, "bare", Func)
	if !ok {
		t.Fatal("missing bare")
	}
	if got := string(src[bare.BodyStart : bare.BodyStart+len("func bare")]); got != "func bare" {
		t.Errorf("bare BodyStart should be the func keyword, got %q", got)
	}
}

func TestExtract_ParseErrorReturnsErr(t *testing.T) {
	_, err := GoExtractor{}.Extract([]byte("package x\nfunc ("))
	if err == nil {
		t.Fatal("expected parse error for malformed Go, got nil")
	}
}

func TestCodec_RoundTrip(t *testing.T) {
	syms, src := extract(t)
	ix := NewIndex()
	ix.Set(0, syms)
	ix.Set(7, syms) // a second blob ID to exercise multi-blob serialization

	path := filepath.Join(t.TempDir(), "symbols.sym")
	if err := Save(ix, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Enclosing must return identical results at a range of offsets before/after.
	probes := []int{
		indexOf(src, `return "hi " + n`),
		indexOf(src, "return hello(name)"),
		indexOf(src, "return w.ID"),
		indexOf(src, "Limit = 10"),
	}
	for _, blob := range []uint64{0, 7} {
		for _, off := range probes {
			a, aok := ix.Enclosing(blob, off)
			b, bok := got.Enclosing(blob, off)
			if aok != bok || a != b {
				t.Errorf("blob %d off %d: before=(%+v,%v) after=(%+v,%v)", blob, off, a, aok, b, bok)
			}
		}
	}
}

func TestBuild_SkipsUnparseableBlobs(t *testing.T) {
	ix := index.New()
	ix.AddFile("r", "good.go", "/good.go", "sha-good", []byte(fixture))
	ix.AddFile("r", "bad.go", "/bad.go", "sha-bad", []byte("package x\nfunc ("))
	ix.AddFile("r", "notgo.txt", "/notgo.txt", "sha-txt", []byte("just some prose, not code at all"))

	sym := Build(ix, GoExtractor{})

	if _, ok := sym.Enclosing(0, indexOf([]byte(fixture), "return w.ID")); !ok {
		t.Error("expected symbols for the good blob")
	}
	if n := len(sym.Symbols(1)); n != 0 {
		t.Errorf("expected no symbols for unparseable blob, got %d", n)
	}
	if n := len(sym.Symbols(2)); n != 0 {
		t.Errorf("expected no symbols for non-Go blob, got %d", n)
	}
}

func TestEnclosingBytesFunc(t *testing.T) {
	syms, src := extract(t)
	ix := NewIndex()
	ix.Set(3, syms)
	fn := ix.EnclosingBytesFunc()

	off := indexOf(src, "return w.ID")
	bs, be, ok := fn(3, off)
	if !ok {
		t.Fatal("EnclosingBytesFunc: no hit inside Spin")
	}
	spin, _ := find(syms, "Spin", Method)
	if bs != spin.BodyStart || be != spin.BodyEnd {
		t.Errorf("closure range [%d,%d), want Spin [%d,%d)", bs, be, spin.BodyStart, spin.BodyEnd)
	}
	if _, _, ok := fn(999, off); ok {
		t.Error("expected no hit for unknown blob")
	}
}

func contains(s, sub string) bool { return indexOfStr(s, sub) >= 0 }

func indexOf(b []byte, sub string) int { return indexOfStr(string(b), sub) }

func indexOfStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
