package eval

import "testing"

// These tests pin UDCGAtK by hand computation, and pin its defining property:
// distractors (explicit negative grades and implicit unlabeled grade-0 hits in
// the top-k) are penalized, where nDCG would ignore them. d1/d2 (the log2
// position discounts) and assertClose/eps are defined in metrics_test.go.

// TestUDCGPerfectEqualsNDCG: with the relevant doc on top and no distractors in
// the top-k, UDCG has no penalty term and equals nDCG (== 1.0 for a perfect rank).
func TestUDCGPerfectEqualsNDCG(t *testing.T) {
	gold := map[string]int{"a": 2}
	ranked := []string{"a"}
	assertClose(t, "UDCG perfect", UDCGAtK(ranked, gold, 5), 1.0)
	assertClose(t, "UDCG==NDCG perfect", UDCGAtK(ranked, gold, 5), NDCGAtK(ranked, gold, 5))
}

// TestUDCGImplicitPenalty: an UNLABELED doc (grade 0) in the top-k costs
// unlabeledDistractorPenalty * discount(rank), pulling UDCG below nDCG.
// IDCG = gain(2)*d1 = 3. UDCG = (3*d1 - 0.5*d2) / 3.
func TestUDCGImplicitPenalty(t *testing.T) {
	gold := map[string]int{"a": 2}
	ranked := []string{"a", "x"} // x is unlabeled
	idcg := 3.0
	want := (3.0*d1 - unlabeledDistractorPenalty*d2) / idcg
	assertClose(t, "UDCG implicit penalty", UDCGAtK(ranked, gold, 5), want)

	// nDCG ignores the unlabeled hit entirely, so UDCG must be strictly lower.
	if UDCGAtK(ranked, gold, 5) >= NDCGAtK(ranked, gold, 5) {
		t.Errorf("UDCG (%.6f) should be < NDCG (%.6f) when an unlabeled doc sits in top-k",
			UDCGAtK(ranked, gold, 5), NDCGAtK(ranked, gold, 5))
	}
}

// TestUDCGHardCostsMoreThanWeak: an explicit hard distractor (grade -1, penalty
// gain(1)=1.0) at a position costs MORE than an unlabeled doc (penalty 0.5) at the
// same position — the whole point of the explicit labels.
func TestUDCGHardCostsMoreThanWeak(t *testing.T) {
	idcg := 3.0
	// Hard distractor "d" (grade -1) at rank 2.
	goldHard := map[string]int{"a": 2, "d": -1}
	hard := UDCGAtK([]string{"a", "d"}, goldHard, 5)
	wantHard := (3.0*d1 - distractorPenalty(-1)*d2) / idcg
	assertClose(t, "UDCG hard distractor", hard, wantHard)

	// Unlabeled "x" at the same rank 2.
	goldWeak := map[string]int{"a": 2}
	weak := UDCGAtK([]string{"a", "x"}, goldWeak, 5)

	if !(hard < weak) {
		t.Errorf("hard distractor UDCG (%.6f) should be < unlabeled UDCG (%.6f): "+
			"a confirmed plausible-but-wrong hit must cost more than a probable one", hard, weak)
	}
	// And both below the distractor-free perfect score.
	if !(weak < 1.0) {
		t.Errorf("unlabeled-in-topk UDCG (%.6f) should be < 1.0", weak)
	}
}

// TestUDCGGrade2DistractorCostsMore: a grade -2 distractor (penalty gain(2)=3.0)
// costs strictly more than a grade -1 (penalty 1.0) at the same rank.
func TestUDCGGrade2DistractorCostsMore(t *testing.T) {
	d1Gold := map[string]int{"a": 2, "d": -1}
	d2Gold := map[string]int{"a": 2, "d": -2}
	u1 := UDCGAtK([]string{"a", "d"}, d1Gold, 5)
	u2 := UDCGAtK([]string{"a", "d"}, d2Gold, 5)
	if !(u2 < u1) {
		t.Errorf("grade -2 distractor UDCG (%.6f) should be < grade -1 (%.6f)", u2, u1)
	}
}

// TestUDCGBeyondCutoffNoPenalty: a distractor BELOW the cutoff k is not in the
// agent's context window, so it adds no penalty — UDCG equals the distractor-free
// nDCG. Here k=1 so only "a" is in window; "d" at rank 2 is ignored.
func TestUDCGBeyondCutoffNoPenalty(t *testing.T) {
	gold := map[string]int{"a": 2, "d": -1}
	ranked := []string{"a", "d"}
	assertClose(t, "UDCG distractor beyond cutoff", UDCGAtK(ranked, gold, 1), 1.0)
	assertClose(t, "UDCG==NDCG beyond cutoff", UDCGAtK(ranked, gold, 1), NDCGAtK(ranked, gold, 1))
}

// TestUDCGCanGoNegative: a top-k dominated by a costly distractor with the true
// relevant doc absent scores below 0 — worse for an agent than retrieving nothing.
// IDCG = gain(2)*d1 = 3 (from "a" in the gold). UDCG = (-gain(2)*d1)/3 = -1.
func TestUDCGCanGoNegative(t *testing.T) {
	gold := map[string]int{"a": 2, "d": -2}
	ranked := []string{"d"} // the relevant "a" was not retrieved
	assertClose(t, "UDCG negative", UDCGAtK(ranked, gold, 1), -1.0)
}

// TestUDCGNoRelevantIsZero: with no relevant docs IDCG is 0 and UDCG is undefined,
// returning 0 to match NDCGAtK — even when distractors are present.
func TestUDCGNoRelevantIsZero(t *testing.T) {
	gold := map[string]int{"d": -1}
	assertClose(t, "UDCG no relevant", UDCGAtK([]string{"d", "x"}, gold, 5), 0.0)
}

// TestUDCGDeduplicates: a doc's second occurrence contributes nothing, for both a
// relevant doc and a distractor (matching nDCG's first-occurrence rule).
func TestUDCGDeduplicates(t *testing.T) {
	gold := map[string]int{"a": 2}
	assertClose(t, "UDCG dup relevant", UDCGAtK([]string{"a", "a"}, gold, 5), 1.0)

	goldD := map[string]int{"a": 2, "d": -1}
	once := UDCGAtK([]string{"a", "d"}, goldD, 5)
	twice := UDCGAtK([]string{"a", "d", "d"}, goldD, 5)
	assertClose(t, "UDCG dup distractor", twice, once)
}

// TestUDCGPenaltyHelpers pins the penalty functions directly.
func TestUDCGPenaltyHelpers(t *testing.T) {
	assertClose(t, "distractorPenalty(-1)", distractorPenalty(-1), 1.0) // gain(1)
	assertClose(t, "distractorPenalty(-2)", distractorPenalty(-2), 3.0) // gain(2)
	assertClose(t, "distractorPenalty(0)", distractorPenalty(0), 0.0)
	assertClose(t, "distractorPenalty(2)", distractorPenalty(2), 0.0) // relevant, not a distractor
	if unlabeledDistractorPenalty >= distractorPenalty(-1) {
		t.Errorf("unlabeled penalty (%.3f) must be < hard-distractor penalty (%.3f)",
			unlabeledDistractorPenalty, distractorPenalty(-1))
	}
}

// TestNegativeGradesInvisibleToOtherMetrics is the load-bearing safety property:
// adding a grade -1 distractor label to the gold must NOT change nDCG, recall,
// precision, MRR, or the relevant-count — they all guard on grade >= 1. This is
// why the four hard-distractor labels in gold_corpus.go do not move the existing
// baselines or gate floors; only UDCG sees them.
func TestNegativeGradesInvisibleToOtherMetrics(t *testing.T) {
	without := map[string]int{"a": 2, "b": 1}
	with := map[string]int{"a": 2, "b": 1, "d": -1}
	ranked := []string{"a", "d", "b"} // distractor retrieved between the two relevants

	for k := 1; k <= 3; k++ {
		assertClose(t, "NDCG invariant", NDCGAtK(ranked, with, k), NDCGAtK(ranked, without, k))
		assertClose(t, "Recall invariant", RecallAtK(ranked, with, k), RecallAtK(ranked, without, k))
		assertClose(t, "Precision invariant", PrecisionAtK(ranked, with, k), PrecisionAtK(ranked, without, k))
	}
	assertClose(t, "MRR invariant", MRR(ranked, with), MRR(ranked, without))
	if numRelevant(with) != numRelevant(without) {
		t.Errorf("numRelevant changed: with=%d without=%d", numRelevant(with), numRelevant(without))
	}
	// But UDCG MUST differ — the distractor at rank 2 is penalized.
	if UDCGAtK(ranked, with, 3) >= UDCGAtK(ranked, without, 3) {
		t.Errorf("UDCG should drop when a hard distractor is labeled and retrieved: with=%.6f without=%.6f",
			UDCGAtK(ranked, with, 3), UDCGAtK(ranked, without, 3))
	}
}
