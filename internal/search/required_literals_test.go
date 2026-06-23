package search

import "testing"

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
		// Concat with a required literal run; we take the longest run.
		{`func_[0-9]+`, []string{"func_"}},
		{`public\s+class`, []string{"public"}}, // both "public"&"class" required; longest wins
		// Alternation where every branch has a required literal.
		{`handler|response|payload`, []string{"handler", "response", "payload"}},
		// "(get|set);" simplifies to Concat[Alternate(get,set), Literal(;)], so it
		// is NOT a top-level alternation: the longest required run is ";" (present
		// in every match). Sound, just coarser than per-branch.
		{`(get|set);`, []string{";"}},
		// SOUNDNESS: a branch with NO required literal disables the WHOLE filter.
		{`foo|[0-9]+`, nil},
		{`foo|bar?`, nil}, // "bar?" -> "ba" required actually; check below
		// Leading/standalone char class: no required literal.
		{`[Tt]oken`, []string{"oken"}}, // "oken" is the required run after the class
		{`[0-9]+`, nil},
		{`.`, nil},
		{`a*`, nil}, // star: nothing required
		{`colou?r`, []string{"colo"}}, // "u?" optional, but "colo" then "r" required; longest "colo"
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

// TestRequiredLiteralsFolded checks the case-insensitive prefilter (Phase 2): a
// clean ASCII (?i) literal yields a non-empty variant-trigram set, while one with
// no clean trigram position (k/s fold orbits leave ASCII) safely yields none.
func TestRequiredLiteralsFolded(t *testing.T) {
	nonEmpty := func(pat string) bool { return len(requiredLiterals(pat)) > 0 }
	// 'password' has the clean position w-o-r; 'return' is all-clean.
	for _, pat := range []string{`(?i)password`, `(?i)return`, `(?i)public`} {
		if !nonEmpty(pat) {
			t.Errorf("requiredLiterals(%q) empty; expected a variant-trigram prefilter", pat)
		}
	}
	// Every trigram position touches k or s (orbit leaves ASCII) -> no prefilter.
	for _, pat := range []string{`(?i)key`, `(?i)MSG`, `(?i)ssk`} {
		if nonEmpty(pat) {
			t.Errorf("requiredLiterals(%q) non-empty; k/s-dirty literal must disable the prefilter (sound)", pat)
		}
	}
	// Soundness of the variant set: every member must be a real case variant of a
	// clean trigram of "password" (w-o-r); check the canonical lowercase is present.
	var hasWor bool
	for _, b := range requiredLiterals(`(?i)password`) {
		if string(b) == "wor" {
			hasWor = true
		}
	}
	if !hasWor {
		t.Errorf("(?i)password prefilter missing canonical trigram 'wor'; got %v", requiredLiterals(`(?i)password`))
	}
}
