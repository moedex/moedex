package eval

import (
	"math"
	"testing"
)

const eps = 1e-9

func assertClose(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > eps {
		t.Errorf("%s = %.12f, want %.12f (diff %.2e)", name, got, want, math.Abs(got-want))
	}
}

// log2 discounts used in the hand computations below, named for clarity.
var (
	d1 = 1.0                // 1/log2(2)
	d2 = 1.0 / math.Log2(3) // 0.6309297535714574
	d3 = 1.0 / math.Log2(4) // 0.5
	d4 = 1.0 / math.Log2(5) // 0.43067655807339306
)

func binary(rels ...string) map[string]int {
	m := make(map[string]int, len(rels))
	for _, r := range rels {
		m[r] = 1
	}
	return m
}

// --- Recall@k ---------------------------------------------------------------

func TestRecallAtK(t *testing.T) {
	// 4 relevant docs total: a,b,c,d. Ranking surfaces a,d in top 3.
	ranked := []string{"a", "x", "d", "y", "z"}
	rel := binary("a", "b", "c", "d")

	// top 3 contains a and d -> 2 of 4 relevant -> 0.5
	assertClose(t, "recall@3", RecallAtK(ranked, rel, 3), 0.5)
	// top 1 contains a -> 1 of 4 -> 0.25
	assertClose(t, "recall@1", RecallAtK(ranked, rel, 1), 0.25)
	// whole list still only has a,d -> 0.5 (b,c never retrieved)
	assertClose(t, "recall@all", RecallAtK(ranked, rel, 0), 0.5)
}

func TestRecallNoRelevant(t *testing.T) {
	// Empty relevance set: nothing to recall, defined as 0.
	assertClose(t, "recall no-rel", RecallAtK([]string{"a", "b"}, map[string]int{}, 5), 0)
}

func TestRecallDuplicatesCountOnce(t *testing.T) {
	// "a" appears twice; it must not be double-counted.
	ranked := []string{"a", "a", "b"}
	rel := binary("a", "b")
	// a found once, b found once -> 2 of 2 -> 1.0
	assertClose(t, "recall dup", RecallAtK(ranked, rel, 3), 1.0)
}

// --- Precision@k ------------------------------------------------------------

func TestPrecisionAtK(t *testing.T) {
	ranked := []string{"a", "x", "d", "y"}
	rel := binary("a", "b", "c", "d")
	// top 3 = a,x,d -> 2 relevant / 3 = 0.6666...
	assertClose(t, "prec@3", PrecisionAtK(ranked, rel, 3), 2.0/3.0)
	// top 1 = a -> 1/1 = 1.0
	assertClose(t, "prec@1", PrecisionAtK(ranked, rel, 1), 1.0)
}

func TestPrecisionEmptyList(t *testing.T) {
	assertClose(t, "prec empty", PrecisionAtK(nil, binary("a"), 5), 0)
}

// --- MRR --------------------------------------------------------------------

func TestMRR(t *testing.T) {
	rel := binary("a", "b")
	// first relevant ("a") at rank 1 -> 1.0
	assertClose(t, "mrr rank1", MRR([]string{"a", "z"}, rel), 1.0)
	// first relevant ("b") at rank 3 -> 1/3
	assertClose(t, "mrr rank3", MRR([]string{"x", "y", "b"}, rel), 1.0/3.0)
	// no relevant retrieved -> 0
	assertClose(t, "mrr none", MRR([]string{"x", "y"}, rel), 0)
}

// --- nDCG@k: hand-computed cases --------------------------------------------

// Perfect binary ranking: 3 relevant docs in the first 3 positions.
// DCG = 1*d1 + 1*d2 + 1*d3; IDCG identical -> nDCG = 1.0 exactly.
func TestNDCGPerfectBinary(t *testing.T) {
	ranked := []string{"a", "b", "c", "x", "y"}
	rel := binary("a", "b", "c")
	assertClose(t, "ndcg perfect", NDCGAtK(ranked, rel, 5), 1.0)
}

// Reversed: the 3 relevant docs sit at positions 3,4,5 (worst feasible for a
// list of these 5). 2 relevant within @5 cutoff... use full list so all three
// count.
// DCG = 1*d3 + 1*d4 + 1*d5 where d5 = 1/log2(6).
// IDCG = d1 + d2 + d3 (three grade-1 docs ideally placed).
func TestNDCGReversed(t *testing.T) {
	ranked := []string{"x", "y", "a", "b", "c"}
	rel := binary("a", "b", "c")
	d5 := 1.0 / math.Log2(6)
	dcg := d3 + d4 + d5
	idcg := d1 + d2 + d3
	assertClose(t, "ndcg reversed", NDCGAtK(ranked, rel, 0), dcg/idcg)
}

// No relevant retrieved -> 0.
func TestNDCGNoneRetrieved(t *testing.T) {
	ranked := []string{"x", "y", "z"}
	rel := binary("a", "b")
	assertClose(t, "ndcg none", NDCGAtK(ranked, rel, 3), 0)
}

// No relevant docs in gold at all -> IDCG 0 -> defined 0.
func TestNDCGEmptyGold(t *testing.T) {
	assertClose(t, "ndcg empty gold", NDCGAtK([]string{"a"}, map[string]int{}, 3), 0)
}

// Partial: one relevant at rank 2, one relevant at rank 4, two relevant total.
// @3 cutoff sees only the rank-2 hit.
// DCG@3 = 1*d2 (only "b" in top 3). IDCG@3 = d1 + d2 (two relevant ideally).
func TestNDCGPartial(t *testing.T) {
	ranked := []string{"x", "b", "y", "c"}
	rel := binary("b", "c")
	dcg := d2
	idcg := d1 + d2
	assertClose(t, "ndcg partial@3", NDCGAtK(ranked, rel, 3), dcg/idcg)
}

// Graded gains: doc "a" grade 3, doc "b" grade 1, ranked a then b perfectly.
// gain(3)=2^3-1=7, gain(1)=1.
// DCG = 7*d1 + 1*d2 = 7 + d2. IDCG identical (already ideal) -> 1.0.
func TestNDCGGradedPerfect(t *testing.T) {
	ranked := []string{"a", "b"}
	rel := map[string]int{"a": 3, "b": 1}
	assertClose(t, "ndcg graded perfect", NDCGAtK(ranked, rel, 2), 1.0)
}

// Graded, sub-optimal order: the grade-1 doc is ranked above the grade-3 doc.
// DCG = gain(1)*d1 + gain(3)*d2 = 1*1 + 7*d2.
// IDCG = gain(3)*d1 + gain(1)*d2 = 7*1 + 1*d2.
func TestNDCGGradedSuboptimal(t *testing.T) {
	ranked := []string{"b", "a"}
	rel := map[string]int{"a": 3, "b": 1}
	dcg := 1.0*d1 + 7.0*d2
	idcg := 7.0*d1 + 1.0*d2
	assertClose(t, "ndcg graded suboptimal", NDCGAtK(ranked, rel, 2), dcg/idcg)
}

// nDCG must not reorder ties / duplicates: a repeated relevant doc contributes
// gain only at its first occurrence.
func TestNDCGDuplicatesCountOnce(t *testing.T) {
	ranked := []string{"a", "a"} // same doc twice
	rel := binary("a")
	// DCG = 1*d1 (second "a" ignored). IDCG = d1. -> 1.0
	assertClose(t, "ndcg dup", NDCGAtK(ranked, rel, 2), 1.0)
}

// --- helper sanity ----------------------------------------------------------

func TestNewBinaryGold(t *testing.T) {
	g := NewBinaryGold("q", "p1", "p2")
	if g.Query != "q" || g.Relevant["p1"] != 1 || g.Relevant["p2"] != 1 || len(g.Relevant) != 2 {
		t.Fatalf("unexpected gold %#v", g)
	}
}

func TestEffectiveK(t *testing.T) {
	cases := []struct{ k, n, want int }{
		{0, 5, 5}, {-1, 5, 5}, {3, 5, 3}, {10, 5, 5}, {5, 5, 5},
	}
	for _, c := range cases {
		if got := effectiveK(c.k, c.n); got != c.want {
			t.Errorf("effectiveK(%d,%d)=%d want %d", c.k, c.n, got, c.want)
		}
	}
}
