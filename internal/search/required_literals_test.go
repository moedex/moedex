package search

import (
	"strings"
	"testing"

	"moedex/internal/trigram"
)

// TestRequiredLiterals exercises the prefilter literal extractor directly
// (white-box), since its correctness is the soundness contract for the regex
// prefilter. The cardinal rule: a literal may appear in the result ONLY if every
// match of the pattern is guaranteed to contain it. Under-returning (empty) is
// always safe (just disables the prefilter); over-returning a non-required
// literal would drop true matches and break ripgrep parity.
func TestRequiredLiterals(t *testing.T) {
	got := func(pat string) []string {
		var out []string
		for _, b := range requiredLiterals(pat) {
			out = append(out, string(b))
		}
		return out
	}
	eq := func(a []string, b ...string) bool {
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

	cases := []struct {
		pat  string
		want []string // nil means "no prefilter"
	}{
		// Concat with required literal runs; every sub-filter is required.
		{`func_[0-9]+`, []string{"func_"}},
		{`public\s+class`, []string{"public", "class"}},
		// Alternation where every branch has a required literal.
		{`handler|response|payload`, []string{"handler", "response", "payload"}},
		// "(get|set);" simplifies to Concat[Alternate(get,set), Literal(;)], so it
		// becomes (get OR set) AND ";": sound and stronger than the old single-literal
		// fallback.
		{`(get|set);`, []string{"get", "set", ";"}},
		// SOUNDNESS: a branch with NO required literal disables the WHOLE filter.
		{`foo|[0-9]+`, nil},
		{`foo|bar?`, nil}, // "bar?" -> "ba" required actually; check below
		// Leading/standalone char class: no required literal.
		{`[Tt]oken`, []string{"T", "t", "oken"}},
		{`[0-9]+`, nil},
		{`.`, nil},
		{`a*`, nil},                        // star: nothing required
		{`colou?r`, []string{"colo", "r"}}, // "u?" optional, but "colo" and "r" are required
	}
	for _, c := range cases {
		g := got(c.pat)
		// "foo|bar?" : bar? = "ba"+"r?" so branch2 requires "ba"; branch1 "foo".
		if c.pat == `foo|bar?` {
			if !eq(g, "foo", "ba") {
				t.Errorf("requiredLiterals(%q) = %v, want [foo ba]", c.pat, g)
			}
			continue
		}
		if c.want == nil {
			if len(g) != 0 {
				t.Errorf("requiredLiterals(%q) = %v, want empty (no prefilter)", c.pat, g)
			}
			continue
		}
		if !eq(g, c.want...) {
			t.Errorf("requiredLiterals(%q) = %v, want %v", c.pat, g, c.want)
		}
	}
}

// TestRequiredLiteralsFolded checks the case-insensitive prefilter: a clean ASCII
// (?i) literal yields bounded variant sets, and a dirty folded literal can still
// use its longest clean ASCII 1-2 byte span when a full clean trigram is absent.
func TestRequiredLiteralsFolded(t *testing.T) {
	nonEmpty := func(pat string) bool { return len(requiredLiterals(pat)) > 0 }
	// 'password' has the clean position w-o-r; 'return' is all-clean. 'key' has
	// no clean trigram, but can use the clean fallback span "ey".
	for _, pat := range []string{`(?i)password`, `(?i)return`, `(?i)public`, `(?i)key`} {
		if !nonEmpty(pat) {
			t.Errorf("requiredLiterals(%q) empty; expected a variant prefilter", pat)
		}
	}
	// Every position is k/s-dirty (orbit leaves ASCII) -> no prefilter.
	for _, pat := range []string{`(?i)ssk`, `(?i)sks`} {
		if nonEmpty(pat) {
			t.Errorf("requiredLiterals(%q) non-empty; all-dirty literal must disable the prefilter (sound)", pat)
		}
	}
	// Soundness of the variant set: every member must be a real case variant of a
	// clean trigram of "password"; check multiple canonical lowercase positions
	// are present.
	var hasWor, hasOrd bool
	for _, b := range requiredLiterals(`(?i)password`) {
		switch string(b) {
		case "wor":
			hasWor = true
		case "ord":
			hasOrd = true
		}
	}
	if !hasWor {
		t.Errorf("(?i)password prefilter missing canonical trigram 'wor'; got %v", requiredLiterals(`(?i)password`))
	}
	if !hasOrd {
		t.Errorf("(?i)password prefilter missing canonical trigram 'ord'; got %v", requiredLiterals(`(?i)password`))
	}
	if n := len(requiredLiterals(`(?i)Computes`)); n <= maxFoldedPrefilterVariants {
		t.Errorf("(?i)Computes prefilter used only one variant set; got %d literals", n)
	}
	if f := requiredLineFilter(`(?i)Computes`); f == nil || !strings.HasPrefix(f.String(), "all(") {
		t.Errorf("(?i)Computes filter = %v, want conjunctive multi-position filter", f)
	}
	if f := requiredLineFilter(`(?i)MemberInfo`); f == nil {
		t.Fatalf("(?i)MemberInfo filter nil; expected conjunctive folded filter")
	} else {
		if !f.maybe([]byte("memberinfo")) {
			t.Fatalf("(?i)MemberInfo filter rejected canonical lowercase")
		}
		if f.maybe([]byte("mem only")) {
			t.Fatalf("(?i)MemberInfo filter accepted partial folded literal")
		}
		all, ok := f.(allFilter)
		if !ok {
			t.Fatalf("(?i)MemberInfo filter = %T, want allFilter", f)
		}
		if len(all) < 2 || len(all) > maxFoldedPrefilterPositions {
			t.Fatalf("(?i)MemberInfo allFilter children = %d, want 2..%d", len(all), maxFoldedPrefilterPositions)
		}
	}
	if lits := requiredLiterals(`(?i)abcdefghijklmnop`); len(lits) == 0 || len(lits) > maxFoldedPrefilterPositions*maxFoldedPrefilterVariants {
		t.Fatalf("long folded literal leaves = %d, want 1..%d", len(lits), maxFoldedPrefilterPositions*maxFoldedPrefilterVariants)
	} else {
		for _, lit := range lits {
			if len(lit) != trigram.N {
				t.Fatalf("long folded literal leaf length = %d for %q, want trigram-sized leaves", len(lit), lit)
			}
		}
	}
}

func TestRequiredLiteralsUnicodeCharClass(t *testing.T) {
	f := requiredLineFilter(`[α-ω]+`)
	if f == nil {
		t.Fatalf("requiredLineFilter([α-ω]+) = nil, want bounded Unicode class filter")
	}
	if len(f.lits()) != 0 {
		t.Fatalf("Unicode class filter exposed literal leaves: got %q", f.lits())
	}
	if !f.maybe([]byte("abc β def")) {
		t.Fatalf("Unicode class filter rejected a line containing beta")
	}
	if f.maybe([]byte("plain ascii")) {
		t.Fatalf("Unicode class filter accepted a plain ASCII line")
	}
	if got := requiredLiterals(`\p{Greek}`); len(got) != 0 {
		t.Fatalf("requiredLiterals(\\p{Greek}) = %q, want empty for oversized class", got)
	}
	if f := requiredLineFilter(`(?i)[é]`); f != nil {
		t.Fatalf("requiredLineFilter((?i)[é]) = %v, want nil until folded classes are explicitly supported", f)
	}
}
