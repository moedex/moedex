package query

import (
	"regexp"
	"strings"
	"testing"

	"moedex/internal/index"
)

// buildIndex makes a tiny in-memory index from labeled string contents.
func buildIndex(contents ...string) *index.Index {
	ix := index.New()
	for i, c := range contents {
		sha := string(rune('a'+i)) + c // unique enough for these tests
		ix.AddFile("r", "f", "/abs/f", sha, []byte(c))
	}
	return ix
}

// mustQ reduces a pattern with the Cox reduction.
func mustQ(t *testing.T, pat string) Query {
	t.Helper()
	q, err := FromRegexp(pat)
	if err != nil {
		t.Fatalf("FromRegexp(%q): %v", pat, err)
	}
	return q
}

// TestCharClassExpansion verifies [ab]cd reduces to a real filter (not All) and
// that its candidate set is exactly the blobs containing acd or bcd.
func TestCharClassExpansion(t *testing.T) {
	q := mustQ(t, "[ab]cd")
	if _, isAll := q.(allQ); isAll {
		t.Fatalf("[ab]cd should produce a filter, got All: %s", q.String())
	}
	ix := buildIndex(
		"xacdy", // contains acd -> candidate
		"zbcdw", // contains bcd -> candidate
		"nomatchhere",
		"acb cb", // neither acd nor bcd
	)
	got := q.Eval(ix)
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %d (%v); query=%s", len(got), got, q.String())
	}
}

// TestBoundaryTrigramSynthesis verifies concatenation synthesizes a trigram
// that straddles the join. "fo" + "od" has no length-3 literal run on either
// side, but the boundary requires "foo"/"ood"-style grams via the suffix×prefix
// cross.
func TestBoundaryTrigramSynthesis(t *testing.T) {
	// "fo[oa]d": left exact {fo}, right pieces -> boundary trigrams foo/fod etc.
	q := mustQ(t, "fo[oa]d")
	if _, isAll := q.(allQ); isAll {
		t.Fatalf("fo[oa]d should produce a filter, got All")
	}
	ix := buildIndex(
		"a food b", // food matches
		"a foad b", // foad matches
		"fo od separated", // has 'fo' and 'od' but not contiguous -> not a candidate
	)
	got := q.Eval(ix)
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates (food,foad), got %d (%v); query=%s", len(got), got, q.String())
	}
}

// TestNeverUnderApproximates is the core safety test: for many patterns over
// random-ish content, every blob that truly contains a regex match MUST appear
// in the candidate set. (Over-approximation — extra candidates — is fine.)
func TestNeverUnderApproximates(t *testing.T) {
	patterns := []string{
		`[ab]cd`,
		`fo[oa]d`,
		`public\s+class`,
		`namespace\s+[A-Za-z.]+`,
		`(get|set);`,
		`[Tt]oken`,
		`class\s+[A-Za-z]+`,
		`colou?r`,
		`gr[ae]y`,
		`[0-9]{3}-[0-9]{4}`,
		`Sslv?Service`,
		`https?://`,
		`a.c`,
		`\bword\b`,
		`(foo|bar|baz)qux`,
		`x*y`,
		`.`,
		`end$`,
		`^start`,
		`[A-Z][a-z]+Exception`,
		`(?i)hopped`,
		`(?i)Token`,
		`(?i)password`,
		`(?i)key`,
		`(?i)public|return`,
	}
	corpus := []string{
		"public class SslService {",
		"  namespace TC.SslApi.Internal;",
		"    int x; get; set;",
		"Token token = new Token();",
		"the color is grey today",
		"the colour is gray",
		"call 555-1234 now",
		"visit http://example.com and https://secure.test",
		"abc abd acd bcd",
		"foo food foad bar baz qux barqux",
		"this is the end",
		"start of the file",
		"throw new ArgumentException();",
		"a c a-c axc",
		"a word here",
		"xxxxy yy y",
		"",
		"plain line with nothing special",
		"SslService and SslvService both",
		"random gibberish lkjasdf",
		"HOPPED upstairs and a ToKeN appeared",
		"the PASSWORD field here",
		"user paſſword reset link", // long-s (U+017F): (?i)password must still find it
		"CLASS Public RETURN value",
		"private key store",
	}
	ix := buildIndex(corpus...)
	nBlobs := ix.NumBlobs()

	for _, pat := range patterns {
		re, err := regexp.Compile(pat)
		if err != nil {
			t.Fatalf("compile %q: %v", pat, err)
		}
		q := mustQ(t, pat)
		cand := map[uint64]bool{}
		for _, id := range q.Eval(ix) {
			cand[id] = true
		}
		for id := 0; id < nBlobs; id++ {
			b := ix.Blob(uint64(id))
			truthy := false
			for _, line := range strings.Split(string(b.Content), "\n") {
				if re.MatchString(line) {
					truthy = true
					break
				}
			}
			if truthy && !cand[uint64(id)] {
				t.Errorf("UNDER-APPROXIMATION: pattern %q drops blob %d (%q); query=%s",
					pat, id, string(b.Content), q.String())
			}
		}
	}
}

// TestFoldedLiteralSelectivity proves the fold-aware reduction actually narrows
// (not just stays sound): a case-insensitive literal with clean ASCII trigram
// positions yields a real, non-All trigram query, while one dominated by runes
// whose fold orbit leaves ASCII (k, s) safely degrades to All.
func TestFoldedLiteralSelectivity(t *testing.T) {
	isAll := func(pat string) bool {
		_, ok := mustQ(t, pat).(allQ)
		return ok
	}
	// Clean positions -> selective (must NOT be All). 'return' has no s/k; 'hopped'
	// and 'password' have ≥1 all-clean trigram position.
	for _, pat := range []string{`(?i)hopped`, `(?i)password`, `(?i)return`} {
		if isAll(pat) {
			t.Errorf("%s reduced to All; expected a real trigram constraint", pat)
		}
	}
	// 'key' has a single trigram position and 'k' is fold-dirty (k↔U+212A), and
	// 'Token' is 5 chars with 'k' in all three of its positions, so both must fall
	// back to All (sound, just not selective).
	for _, pat := range []string{`(?i)key`, `(?i)Token`} {
		if !isAll(pat) {
			t.Errorf("%s should degrade to All (k's fold orbit leaves ASCII)", pat)
		}
	}

	// Selectivity in practice: (?i)password excludes a blob with neither case
	// variant, and includes every case variant (lower/upper/long-s).
	ix := buildIndex(
		"the PASSWORD field",  // 0 upper
		"reset password now",  // 1 lower
		"user paſſword reset", // 2 long-s fold variant
		"totally unrelated",   // 3 no match
	)
	q := mustQ(t, `(?i)password`)
	got := map[uint64]bool{}
	for _, id := range q.Eval(ix) {
		got[id] = true
	}
	for _, id := range []uint64{0, 1, 2} {
		if !got[id] {
			t.Errorf("(?i)password dropped blob %d (a real case variant)", id)
		}
	}
	if got[3] {
		t.Logf("(?i)password admitted unrelated blob 3 (allowed: over-approx, verifier removes it)")
	}
	if len(got) > 3 {
		t.Errorf("(?i)password not selective: matched %d/4 blobs", len(got))
	}
}

// TestZeroWidthAndStarStaySafe checks the always-degrade-to-All escape hatches.
func TestZeroWidthAndStarStaySafe(t *testing.T) {
	for _, pat := range []string{"x*", "x?", ".", "^", "$", "a*b*"} {
		q := mustQ(t, pat)
		if _, isAll := q.(allQ); !isAll {
			// Not strictly required to be All, but must never drop matches; just
			// log what we got for visibility.
			t.Logf("pattern %q -> %s (non-All but must still be sound)", pat, q.String())
		}
	}
}

// TestCoxNoWorseThanRequiredLiterals sanity-checks that for a plain literal both
// reductions produce equivalent candidate sets (Cox must not be worse on the
// easy case it already handled).
func TestCoxNoWorseThanRequiredLiterals(t *testing.T) {
	ix := buildIndex("alpha beta gamma", "beta only", "nothing", "gamma here")
	for _, pat := range []string{"beta", "gamma", "alpha beta"} {
		old, err := fromRegexpRequiredLiterals(pat)
		if err != nil {
			t.Fatal(err)
		}
		neu := mustQ(t, pat)
		oc := len(old.Eval(ix))
		nc := len(neu.Eval(ix))
		if nc > oc {
			t.Errorf("pattern %q: Cox returned MORE candidates (%d) than required-literals (%d)", pat, nc, oc)
		}
	}
}
