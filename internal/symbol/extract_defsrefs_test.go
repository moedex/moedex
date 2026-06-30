package symbol

import (
	"errors"
	"testing"

	"moedex/internal/index"
)

// combinedFake implements Extractor, RefExtractor, AND DefsRefsExtractor. It
// records how many times each method is called so tests can prove the
// combined path is used INSTEAD OF two separate parse/scan passes, not in
// addition to them.
type combinedFake struct {
	syms []Symbol
	occs []Occurrence
	err  error

	extractCalls  int
	refCalls      int
	combinedCalls int
}

func (f *combinedFake) Extract(content []byte) ([]Symbol, error) {
	f.extractCalls++
	return f.syms, f.err
}

func (f *combinedFake) ExtractRefs(content []byte) ([]Occurrence, error) {
	f.refCalls++
	return f.occs, f.err
}

func (f *combinedFake) ExtractDefsRefs(content []byte) ([]Symbol, []Occurrence, error) {
	f.combinedCalls++
	return f.syms, f.occs, f.err
}

// legacyFake implements only Extractor and RefExtractor (no ExtractDefsRefs),
// modeling an extractor that has not been given a combined pass.
type legacyFake struct {
	syms []Symbol
	occs []Occurrence
	err  error

	extractCalls int
	refCalls     int
}

func (f *legacyFake) Extract(content []byte) ([]Symbol, error) {
	f.extractCalls++
	return f.syms, f.err
}

func (f *legacyFake) ExtractRefs(content []byte) ([]Occurrence, error) {
	f.refCalls++
	return f.occs, f.err
}

func someSym() []Symbol {
	return []Symbol{{Name: "X", Kind: Func, NameStart: 0, NameEnd: 1, BodyStart: 0, BodyEnd: 2}}
}

func someOccs() []Occurrence {
	return []Occurrence{{Name: "X", Kind: Func, Role: Reference, Start: 5, End: 6}}
}

func TestExtractDefsRefs_PrefersCombinedMethod(t *testing.T) {
	f := &combinedFake{syms: someSym(), occs: someOccs()}
	syms, occs, err := extractDefsRefs(f, []byte("content"))
	if err != nil {
		t.Fatalf("extractDefsRefs: %v", err)
	}
	if len(syms) != 1 || len(occs) != 1 {
		t.Fatalf("got syms=%v occs=%v, want 1 each", syms, occs)
	}
	if f.combinedCalls != 1 {
		t.Errorf("combinedCalls = %d, want 1", f.combinedCalls)
	}
	if f.extractCalls != 0 || f.refCalls != 0 {
		t.Errorf("extractCalls=%d refCalls=%d, want 0,0 (combined method must be used exclusively, not alongside the separate calls)", f.extractCalls, f.refCalls)
	}
}

func TestExtractDefsRefs_FallsBackForLegacyExtractors(t *testing.T) {
	f := &legacyFake{syms: someSym(), occs: someOccs()}
	syms, occs, err := extractDefsRefs(f, []byte("content"))
	if err != nil {
		t.Fatalf("extractDefsRefs: %v", err)
	}
	if len(syms) != 1 || len(occs) != 1 {
		t.Fatalf("got syms=%v occs=%v, want 1 each", syms, occs)
	}
	if f.extractCalls != 1 || f.refCalls != 1 {
		t.Errorf("extractCalls=%d refCalls=%d, want 1,1 (fallback path unchanged for not-yet-upgraded extractors)", f.extractCalls, f.refCalls)
	}
}

func TestExtractDefsRefs_DefOnlyExtractorSkipsRefs(t *testing.T) {
	// TSExtractor implements neither RefExtractor nor DefsRefsExtractor; the
	// helper must still work, with no references and no panic.
	syms, occs, err := extractDefsRefs(TSExtractor{}, []byte("export class T { m() {} }"))
	if err != nil {
		t.Fatalf("extractDefsRefs: %v", err)
	}
	if len(syms) == 0 {
		t.Fatal("expected symbols from TSExtractor")
	}
	if occs != nil {
		t.Errorf("expected no references for a def-only extractor, got %v", occs)
	}
}

func TestExtractDefsRefs_PropagatesExtractError(t *testing.T) {
	wantErr := errors.New("boom")

	f := &combinedFake{err: wantErr}
	if _, _, err := extractDefsRefs(f, []byte("content")); err != wantErr {
		t.Errorf("combined err = %v, want %v", err, wantErr)
	}

	lf := &legacyFake{err: wantErr}
	if _, _, err := extractDefsRefs(lf, []byte("content")); err != wantErr {
		t.Errorf("legacy err = %v, want %v", err, wantErr)
	}
}

func symbolsEqual(a, b []Symbol) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func occsEqual(a, b []Occurrence) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestGoExtractor_ExtractDefsRefs_MatchesSeparateCalls proves the new combined
// single-parse method produces byte-identical results to calling Extract then
// ExtractRefs separately, on a fixture that exercises both defs and refs.
func TestGoExtractor_ExtractDefsRefs_MatchesSeparateCalls(t *testing.T) {
	src := []byte(refFixture)
	var ge GoExtractor

	wantSyms, err := ge.Extract(src)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	wantOccs, err := ge.ExtractRefs(src)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}

	gotSyms, gotOccs, err := ge.ExtractDefsRefs(src)
	if err != nil {
		t.Fatalf("ExtractDefsRefs: %v", err)
	}
	if !symbolsEqual(wantSyms, gotSyms) {
		t.Errorf("ExtractDefsRefs syms = %+v, want %+v", gotSyms, wantSyms)
	}
	if !occsEqual(wantOccs, gotOccs) {
		t.Errorf("ExtractDefsRefs occs = %+v, want %+v", gotOccs, wantOccs)
	}
}

func TestGoExtractor_ExtractDefsRefs_ParseError(t *testing.T) {
	var ge GoExtractor
	syms, occs, err := ge.ExtractDefsRefs([]byte("package x\nfunc ("))
	if err == nil {
		t.Fatal("expected parse error for malformed Go, got nil")
	}
	if syms != nil || occs != nil {
		t.Errorf("expected nil syms/occs on error, got %v / %v", syms, occs)
	}
}

// TestCSharpExtractor_ExtractDefsRefs_MatchesSeparateCalls proves the combined
// method matches Extract+ExtractRefs called separately.
func TestCSharpExtractor_ExtractDefsRefs_MatchesSeparateCalls(t *testing.T) {
	src := []byte(csRefFixture)
	var ce CSharpExtractor

	wantSyms, err := ce.Extract(src)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	wantOccs, err := ce.ExtractRefs(src)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}

	gotSyms, gotOccs, err := ce.ExtractDefsRefs(src)
	if err != nil {
		t.Fatalf("ExtractDefsRefs: %v", err)
	}
	if !symbolsEqual(wantSyms, gotSyms) {
		t.Errorf("ExtractDefsRefs syms = %+v, want %+v", gotSyms, wantSyms)
	}
	if !occsEqual(wantOccs, gotOccs) {
		t.Errorf("ExtractDefsRefs occs = %+v, want %+v", gotOccs, wantOccs)
	}
}

// TestBuild_UsesCombinedExtractionExactlyOncePerBlob proves Build, given an
// extractor that implements DefsRefsExtractor, calls the combined method
// exactly once per blob and never calls Extract/ExtractRefs separately.
func TestBuild_UsesCombinedExtractionExactlyOncePerBlob(t *testing.T) {
	ix := index.New()
	ix.AddFile("r", "a.txt", "/a.txt", "sha-a", []byte("blob a"))
	ix.AddFile("r", "b.txt", "/b.txt", "sha-b", []byte("blob b"))

	f := &combinedFake{syms: someSym(), occs: someOccs()}
	sym := Build(ix, f)

	if f.combinedCalls != 2 {
		t.Errorf("combinedCalls = %d, want 2 (one per blob)", f.combinedCalls)
	}
	if f.extractCalls != 0 || f.refCalls != 0 {
		t.Errorf("extractCalls=%d refCalls=%d, want 0,0", f.extractCalls, f.refCalls)
	}
	if sym.NumBlobs() != 2 || sym.NumRefBlobs() != 2 {
		t.Errorf("NumBlobs=%d NumRefBlobs=%d, want 2,2", sym.NumBlobs(), sym.NumRefBlobs())
	}
}
