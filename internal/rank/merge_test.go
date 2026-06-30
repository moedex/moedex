package rank

import "testing"

// TestMergeLineRangesOverlapping checks two overlapping ranges fold into one,
// taking the union of their bounds via combine.
func TestMergeLineRangesOverlapping(t *testing.T) {
	spans := []LineSpan{{StartLine: 5, EndLine: 9}, {StartLine: 7, EndLine: 12}}
	out := MergeLineRanges(spans, lineSpanBounds, combineLineSpanEnd)
	if len(out) != 1 {
		t.Fatalf("expected 1 merged range, got %d: %+v", len(out), out)
	}
	if out[0].StartLine != 5 || out[0].EndLine != 12 {
		t.Errorf("expected 5-12, got %d-%d", out[0].StartLine, out[0].EndLine)
	}
}

// TestMergeLineRangesTouching checks the off-by-one adjacency rule: ranges that
// merely TOUCH (next.start == last.end+1) merge, but a one-line gap does not.
func TestMergeLineRangesTouching(t *testing.T) {
	touching := []LineSpan{{StartLine: 1, EndLine: 3}, {StartLine: 4, EndLine: 6}}
	out := MergeLineRanges(touching, lineSpanBounds, combineLineSpanEnd)
	if len(out) != 1 {
		t.Fatalf("touching ranges should merge to 1, got %d: %+v", len(out), out)
	}

	gapped := []LineSpan{{StartLine: 1, EndLine: 3}, {StartLine: 5, EndLine: 6}}
	out = MergeLineRanges(gapped, lineSpanBounds, combineLineSpanEnd)
	if len(out) != 2 {
		t.Fatalf("a one-line gap must NOT merge, got %d: %+v", len(out), out)
	}
}

// TestMergeLineRangesUnsortedInput checks the function sorts before merging, so
// callers need not pre-sort.
func TestMergeLineRangesUnsortedInput(t *testing.T) {
	spans := []LineSpan{{StartLine: 10, EndLine: 12}, {StartLine: 1, EndLine: 2}, {StartLine: 3, EndLine: 4}}
	out := MergeLineRanges(spans, lineSpanBounds, combineLineSpanEnd)
	if len(out) != 2 {
		t.Fatalf("expected 2 merged ranges, got %d: %+v", len(out), out)
	}
	if out[0].StartLine != 1 || out[0].EndLine != 4 {
		t.Errorf("expected first merged range 1-4, got %d-%d", out[0].StartLine, out[0].EndLine)
	}
	if out[1].StartLine != 10 || out[1].EndLine != 12 {
		t.Errorf("expected second range 10-12, got %d-%d", out[1].StartLine, out[1].EndLine)
	}
}

// TestMergeLineRangesEmpty checks the nil/empty input contract.
func TestMergeLineRangesEmpty(t *testing.T) {
	if out := MergeLineRanges([]LineSpan(nil), lineSpanBounds, combineLineSpanEnd); out != nil {
		t.Errorf("expected nil for empty input, got %+v", out)
	}
}

// TestMergeLineRangesCombineCalledOnMerge checks combine is invoked (not just
// bounds-union) so a caller can carry along extra payload fields through a
// merge, mirroring contextwin's mergeFile (which keeps the max Score).
func TestMergeLineRangesCombineCalledOnMerge(t *testing.T) {
	type scored struct {
		start, end int
		score      float64
	}
	items := []scored{{1, 3, 0.2}, {3, 5, 0.9}}
	out := MergeLineRanges(items,
		func(s scored) (int, int) { return s.start, s.end },
		func(a, b scored) scored {
			if b.end > a.end {
				a.end = b.end
			}
			if b.score > a.score {
				a.score = b.score
			}
			return a
		})
	if len(out) != 1 {
		t.Fatalf("expected 1 merged item, got %d: %+v", len(out), out)
	}
	if out[0].score != 0.9 {
		t.Errorf("expected combine to keep max score 0.9, got %v", out[0].score)
	}
}
