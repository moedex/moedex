package query

import (
	"regexp"
	"strings"
	"testing"
)

// This file is the regression guard for a real under-approximation bug: an
// alternation whose branches include an unbounded / large-character-class piece
// (so one branch has no required trigram) wrongly required the OTHER branch's
// trigrams. e.g. foo|[0-9]+ reduced to tri(foo), dropping digit-only matches.
//
// The pre-existing TestNeverUnderApproximates missed this entire shape — its
// pattern list had no alternation with an unbounded/class branch. These tests
// add that coverage directly.

// TestAltUnboundedBranchReducesToAll pins the specific reductions: when one
// alternation branch imposes no trigram constraint, the whole alternation must
// degrade to All (you cannot require the other branch's trigrams).
func TestAltUnboundedBranchReducesToAll(t *testing.T) {
	for _, pat := range []string{
		`foo|[0-9]+`,    // literal | unbounded class
		`[0-9]+|foo`,    // order swapped (fold must be side-independent)
		`foo|bar|[0-9]+`, // multi-way: the unbounded branch dominates
		`foo|.*`,         // literal | anything
		`class|[A-Za-z]+`,
	} {
		q := mustQ(t, pat)
		if _, isAll := q.(allQ); !isAll {
			t.Errorf("%q must reduce to All (an unbounded branch imposes no constraint), got %s", pat, q.String())
		}
	}
}

// TestAltUnderApproxBruteForce is the safety check: for alternation patterns
// with an unbounded/class branch, every blob that truly matches (per the real
// regex engine, line by line) MUST remain a candidate. Over-approximation is
// fine; dropping a true match is the bug.
func TestAltUnderApproxBruteForce(t *testing.T) {
	patterns := []string{
		`foo|[0-9]+`,
		`[0-9]+|foo`,
		`foo|bar|[0-9]+`,
		`class|[A-Za-z]+`,
		`https?|[0-9]+`,
		`(foo|[0-9]+)x`, // alternation nested in a concat: the 'x' may constrain,
		// but the alternation half must not drop the digit branch.
		`[0-9]{3}|foo`, // bounded repeat of a class, still classy
	}
	corpus := []string{
		"the year is 12345 here", // only [0-9]+ / [0-9]{3}
		"abcDEF letters only",    // only [A-Za-z]+
		"foo appears here",       // foo
		"barxy 9x end",           // 'bar', and '9x' for (foo|[0-9]+)x
		"visit http and https",   // https?
		"plain words, nothing numeric or matching",
		"",
	}
	ix := buildIndex(corpus...)
	n := ix.NumBlobs()

	for _, pat := range patterns {
		re := regexp.MustCompile(pat)
		q := mustQ(t, pat)
		cand := map[uint64]bool{}
		for _, id := range q.Eval(ix) {
			cand[id] = true
		}
		for id := 0; id < n; id++ {
			b := ix.Blob(uint64(id))
			truthy := false
			for _, line := range strings.Split(string(b.Content), "\n") {
				if re.MatchString(line) {
					truthy = true
					break
				}
			}
			if truthy && !cand[uint64(id)] {
				t.Errorf("UNDER-APPROXIMATION: %q dropped blob %d (%q); query=%s",
					pat, id, string(b.Content), q.String())
			}
		}
	}
}
