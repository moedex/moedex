package rank

import "testing"

// TestSortByScoreThenBlob checks the shared comparator: score descending, ties
// broken by blob ascending.
func TestSortByScoreThenBlob(t *testing.T) {
	items := []armScore{
		{blob: 5, score: 1},
		{blob: 1, score: 2},
		{blob: 2, score: 2},
		{blob: 3, score: 0.5},
	}
	sortByScoreThenBlob(items, func(a armScore) float64 { return a.score }, func(a armScore) uint64 { return a.blob })
	want := []uint64{1, 2, 5, 3}
	for i, w := range want {
		if items[i].blob != w {
			t.Fatalf("position %d: want blob %d, got %d (%+v)", i, w, items[i].blob, items)
		}
	}
}

// TestCoverageArmNilPostingsOrEmptyTerms checks the disabled/no-op contract:
// nil postings (arm not built) or no query terms both yield nil, never a panic.
func TestCoverageArmNilPostingsOrEmptyTerms(t *testing.T) {
	info := map[uint64][]coverageEntry{0: {{subtoks: []string{"refund"}, span: LineSpan{1, 1}}}}
	postings := map[string][]uint64{"refund": {0}}

	if out := coverageArm([]string{"refund"}, 0.5, nil, info, sumQualifying); out != nil {
		t.Errorf("nil postings should disable the arm, got %+v", out)
	}
	if out := coverageArm(nil, 0.5, postings, info, sumQualifying); out != nil {
		t.Errorf("empty terms should yield nil, got %+v", out)
	}
}

// TestCoverageArmAggregationPolicyDiffers exercises both aggregation policies
// against the SAME candidate (a blob with two entries, each independently
// covering the query's single term) directly through coverageArm, pinning that
// sumQualifying totals across qualifying entries while bestSingle takes the max
// without summing.
func TestCoverageArmAggregationPolicyDiffers(t *testing.T) {
	terms := []string{"refund"}
	postings := map[string][]uint64{"refund": {0}}
	info := map[uint64][]coverageEntry{
		0: {
			{subtoks: []string{"refund"}, span: LineSpan{StartLine: 1, EndLine: 1}},
			{subtoks: []string{"refund"}, span: LineSpan{StartLine: 2, EndLine: 2}},
		},
	}

	sumOut := coverageArm(terms, 1.0, postings, info, sumQualifying)
	if len(sumOut) != 1 || sumOut[0].score != 2 {
		t.Errorf("sumQualifying should total 2 (1+1), got %+v", sumOut)
	}

	bestOut := coverageArm(terms, 1.0, postings, info, bestSingle)
	if len(bestOut) != 1 || bestOut[0].score != 1 {
		t.Errorf("bestSingle should take the single best entry's 1 hit, not sum, got %+v", bestOut)
	}
}
