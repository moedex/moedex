package query

import (
	"reflect"
	"testing"

	"moedex/internal/index"
	"moedex/internal/trigram"
)

func sg(s string) trigram.Trigram { return trigram.Trigram{s[0], s[1], s[2]} }

// selectiveIndex builds a selective index over contents, dropping any trigram
// whose document frequency exceeds frac of the blobs (FREE-style).
func selectiveIndex(frac float64, contents ...string) *index.Index {
	b := index.NewSelective(index.FrequencyThresholdSelector{MaxDocFraction: frac})
	for i, c := range contents {
		sha := string(rune('a'+i)) + c
		b.AddFile("r", "f", "/abs/f", sha, []byte(c))
	}
	return b.Finalize()
}

func eagerIndex(contents ...string) *index.Index {
	ix := index.New()
	for i, c := range contents {
		sha := string(rune('a'+i)) + c
		ix.AddFile("r", "f", "/abs/f", sha, []byte(c))
	}
	return ix
}

// TestTriQNonIndexedGramIsAll: a triQ on a deselected gram must Eval to the full
// blob set (All), NOT an empty posting list. This is the central parity gate —
// an empty list for a non-indexed gram would intersect an AND to empty and drop
// matches.
func TestTriQNonIndexedGramIsAll(t *testing.T) {
	// "xyz" is in every blob (df=1.0) so frac 0.5 drops it.
	ix := selectiveIndex(0.5, "xyz a", "xyz b", "xyz c")
	if ix.IndexedGram(sg("xyz")) {
		t.Fatalf("precondition: xyz should be dropped")
	}
	got := triQ{sg("xyz")}.Eval(ix)
	want := allQ{}.Eval(ix)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("triQ on deselected gram = %v, want All %v", got, want)
	}
	if len(got) != ix.NumBlobs() {
		t.Errorf("triQ on deselected gram returned %d blobs, want all %d", len(got), ix.NumBlobs())
	}
}

// TestTriQIndexedGramUsesPostings: a kept gram still evaluates from its postings
// (selective indexing must not turn every gram into All).
func TestTriQIndexedGramUsesPostings(t *testing.T) {
	// "rar" only in blob 0; "xyz" in all -> xyz dropped, rar kept.
	ix := selectiveIndex(0.5, "rare xyz", "xyz two", "xyz three")
	if !ix.IndexedGram(sg("rar")) {
		t.Fatalf("precondition: rar should be kept")
	}
	got := triQ{sg("rar")}.Eval(ix)
	if !reflect.DeepEqual(got, []uint64{0}) {
		t.Errorf("triQ on kept rare gram = %v, want [0]", got)
	}
}

// TestAndWithDeselectedGramDoesNotUnderApprox: query "foobar" where the middle
// trigram "oob" is deselected must still return the blob containing "foobar".
// The AND must NOT intersect to empty.
func TestAndWithDeselectedGramDoesNotUnderApprox(t *testing.T) {
	// "oob" planted in many blobs to make it frequent; only blob 0 has "foobar".
	contents := []string{
		"the foobar target",
		"oob noise one",
		"oob noise two",
		"oob noise three",
	}
	ix := selectiveIndex(0.5, contents...)
	// Sanity: at least one trigram of "foobar" was dropped, else this is vacuous.
	dropped := false
	for _, s := range []string{"foo", "oob", "oba", "bar"} {
		if !ix.IndexedGram(sg(s)) {
			dropped = true
		}
	}
	if !dropped {
		t.Fatalf("precondition: expected at least one trigram of foobar to be dropped")
	}

	q := literalQuery("foobar")
	cands := q.Eval(ix)
	if !containsU64(cands, 0) {
		t.Errorf("candidates %v must include blob 0 (contains foobar); AND under-approximated", cands)
	}
}

// TestSelectiveNeverUnderApproxVsAllTrigram is the parity-equivalence spine at
// the query layer: for a battery of patterns, the selective index's candidate
// set must be a SUPERSET of the all-trigram index's. A subset would mean the
// selective path dropped a true candidate (a parity break). We compare the
// query the search layer would actually run (FromRegexp / literalQuery).
func TestSelectiveNeverUnderApproxVsAllTrigram(t *testing.T) {
	contents := []string{
		"package main\nfunc Handler(w http.ResponseWriter) {}\n",
		"the quick brown fox\nfoobar baz qux\n",
		"public class SslService {\n  string Token;\n}\n",
		"namespace Example.Api;\nusing System;\n",
		"var x = function(y) { return z(y); };\n",
		"alpha beta gamma\ndelta epsilon\n",
		"foobar appears again here\n",
		"FOOBAR upper\nFooBar mixed\n",
	}
	eager := eagerIndex(contents...)
	// Several thresholds, including aggressive 0.1 that drops most common grams.
	for _, frac := range []float64{0.1, 0.3, 0.5, 0.9} {
		sel := selectiveIndex(frac, contents...)
		patterns := []string{
			"foobar",
			"Handler",
			"public",
			"class",
			"[ab]cd",
			"foo|bar",
			"(get|set)",
			`func\s+[A-Z]`,
			"(?i)foobar",
			"(?i)token",
			"namespace",
			"return",
			"alpha",
			"epsilon",
			"http.ResponseWriter",
			".",
			"qux",
			"SslService",
			"function",
			"System",
		}
		for _, pat := range patterns {
			eq, err := FromRegexp(pat)
			if err != nil {
				t.Fatalf("FromRegexp(%q): %v", pat, err)
			}
			sq, err := FromRegexp(pat)
			if err != nil {
				t.Fatalf("FromRegexp(%q): %v", pat, err)
			}
			eagerCands := eq.Eval(eager)
			selCands := sq.Eval(sel)
			if !isSuperset(selCands, eagerCands) {
				t.Errorf("frac=%.2f pattern %q: selective candidates are NOT a superset of all-trigram candidates\n selective=%v\n all-trigram=%v",
					frac, pat, selCands, eagerCands)
			}
		}
	}
}

func containsU64(s []uint64, v uint64) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// isSuperset reports whether super ⊇ sub (both are sorted-distinct blob-id sets).
func isSuperset(super, sub []uint64) bool {
	set := make(map[uint64]struct{}, len(super))
	for _, x := range super {
		set[x] = struct{}{}
	}
	for _, x := range sub {
		if _, ok := set[x]; !ok {
			return false
		}
	}
	return true
}
