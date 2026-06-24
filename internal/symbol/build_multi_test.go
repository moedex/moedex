package symbol

import (
	"testing"

	"moedex/internal/index"
)

func TestExtractorForPath(t *testing.T) {
	cases := map[string]Extractor{
		"foo.go":     GoExtractor{},
		"bar/Baz.cs": CSharpExtractor{},
		"svc.ts":     TSExtractor{},
		"comp.tsx":   TSExtractor{},
		"COMP.TSX":   TSExtractor{},
		"page.cfm":   CFExtractor{},
		"Order.cfc":  CFExtractor{},
		"PAGE.CFM":   CFExtractor{},
		"schema.sql": SQLExtractor{},
		"DUMP.SQL":   SQLExtractor{},
		"readme.md":  nil,
		"noext":      nil,
		"data.json":  nil,
	}
	for path, want := range cases {
		got := ExtractorForPath(path)
		if (got == nil) != (want == nil) {
			t.Errorf("%q: got %T, want %T", path, got, want)
			continue
		}
		if want != nil && got != want {
			t.Errorf("%q: got %T, want %T", path, got, want)
		}
	}
}

func TestBuildMulti_MixedLanguages(t *testing.T) {
	ix := index.New()
	goSrc := []byte("package p\n\nfunc DoGo() {}\n")
	csSrc := []byte("namespace N\n{\n    public class CsType\n    {\n        public void CsMethod() {}\n    }\n}\n")
	tsSrc := []byte("export class TsType {\n  tsMethod() {\n    return 1;\n  }\n}\n")
	otherSrc := []byte("plain text, no symbols\n")

	ix.AddFile("r", "a.go", "/a.go", "sha-go", goSrc)
	ix.AddFile("r", "b.cs", "/b.cs", "sha-cs", csSrc)
	ix.AddFile("r", "c.ts", "/c.ts", "sha-ts", tsSrc)
	ix.AddFile("r", "d.txt", "/d.txt", "sha-txt", otherSrc)

	sym := BuildMulti(ix)

	// blob IDs are assigned in AddFile order.
	check := func(blob uint64, lang string, wantNames map[string]Kind) {
		got := symNames(sym.Symbols(blob))
		for n, k := range wantNames {
			s, ok := got[n]
			if !ok {
				t.Errorf("%s blob %d: missing %q; got %v", lang, blob, n, names(sym.Symbols(blob)))
				continue
			}
			if s.Kind != k {
				t.Errorf("%s %q: kind = %v, want %v", lang, n, s.Kind, k)
			}
		}
	}

	check(0, "go", map[string]Kind{"DoGo": Func})
	check(1, "cs", map[string]Kind{"CsType": Type, "CsMethod": Method})
	check(2, "ts", map[string]Kind{"TsType": Type, "tsMethod": Method})

	if got := sym.Symbols(3); got != nil {
		t.Errorf("txt blob 3: expected no symbols, got %v", names(got))
	}
}

func TestBuildMulti_NilIndex(t *testing.T) {
	if got := BuildMulti(nil); got == nil || got.NumBlobs() != 0 {
		t.Errorf("BuildMulti(nil) = %v, want empty non-nil index", got)
	}
}
