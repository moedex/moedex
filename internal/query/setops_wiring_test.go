package query

import (
	"reflect"
	"testing"

	"moedex/internal/trigram"
)

// TestAndEvalOrderInvariance proves the smallest-first reorder in andQ.Eval does
// not change the candidate SET (only the work). It builds an index where the
// three trigram subs have very different posting-list lengths, then asserts the
// AND result is identical regardless of the order the subs are declared in. AND
// is commutative+associative, so this MUST hold — and it guards the ripgrep
// parity invariant (the candidate set query.Eval feeds to search must not depend
// on declaration order).
func TestAndEvalOrderInvariance(t *testing.T) {
	// "abc" is common (many blobs), "xyz" is rare (few), "def" is medium.
	ix := buildIndex(
		"abc def ghi",     // abc, def
		"abc xyz",         // abc, xyz  <- the only blob with all of abc+def+xyz? no def
		"abc def xyz mno", // abc, def, xyz  <- has all three
		"abc",             // abc only
		"def def",         // def only
		"abc def",         // abc, def
		"qrs xyz abc def", // all three
	)

	mk := func(s string) triQ { return triQ{trigram.Trigram{s[0], s[1], s[2]}} }
	abc, def, xyz := mk("abc"), mk("def"), mk("xyz")

	orders := [][]Query{
		{abc, def, xyz},
		{xyz, def, abc},
		{def, xyz, abc},
		{xyz, abc, def},
	}

	var want []uint64
	for i, subs := range orders {
		got := andQ{subs}.Eval(ix)
		if i == 0 {
			want = got
			continue
		}
		if !reflect.DeepEqual(norm(got), norm(want)) {
			t.Fatalf("order %d changed AND result: got %v want %v", i, got, want)
		}
	}
	if len(want) == 0 {
		t.Fatal("expected a non-empty intersection for this fixture")
	}
}

// TestOrEvalMatchesUnionIdentity checks orQ.Eval over the ping-pong buffers
// equals the set union of the individual sub results (no buffer-reuse
// corruption across folds).
func TestOrEvalMatchesUnionIdentity(t *testing.T) {
	ix := buildIndex(
		"abc only",
		"def only",
		"xyz only",
		"abc def xyz",
	)
	mk := func(s string) triQ { return triQ{trigram.Trigram{s[0], s[1], s[2]}} }
	abc, def, xyz := mk("abc"), mk("def"), mk("xyz")

	got := orQ{[]Query{abc, def, xyz}}.Eval(ix)

	// Reference: set-union the three sub-evals naively.
	set := map[uint64]bool{}
	for _, s := range []triQ{abc, def, xyz} {
		for _, b := range s.Eval(ix) {
			set[b] = true
		}
	}
	if len(got) != len(set) {
		t.Fatalf("union size %d want %d (got=%v)", len(got), len(set), got)
	}
	for _, b := range got {
		if !set[b] {
			t.Fatalf("union returned blob %d not in any sub", b)
		}
	}
	// Output must be sorted-distinct (parity-relevant).
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("orQ.Eval output not sorted-distinct: %v", got)
		}
	}
}

func norm(s []uint64) []uint64 {
	if len(s) == 0 {
		return nil
	}
	return s
}
