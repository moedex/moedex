package rank

import "sort"

// MergeLineRanges sorts items by (start, end) ascending, then folds any item
// whose start is <= the running end+1 (overlap-or-touch) into the previous one
// via combine. This is the single off-by-one-sensitive adjacency rule shared by
// every 1-based-inclusive line-range merge in moedex (mergeSpans/lexicalSpans
// here, contextwin's mergeFile) — a future change to the rule needs to be made
// in only this one place.
//
// bounds reads an item's (start, end); combine folds b into a when they
// overlap/touch (the caller decides how non-range fields, e.g. a max score,
// combine — see contextwin.mergeFile). The input slice is sorted in place.
// Returns nil for an empty input.
func MergeLineRanges[T any](items []T, bounds func(T) (start, end int), combine func(a, b T) T) []T {
	if len(items) == 0 {
		return nil
	}
	sort.SliceStable(items, func(i, j int) bool {
		si, ei := bounds(items[i])
		sj, ej := bounds(items[j])
		if si != sj {
			return si < sj
		}
		return ei < ej
	})
	out := []T{items[0]}
	for _, it := range items[1:] {
		last := out[len(out)-1]
		_, lastEnd := bounds(last)
		start, _ := bounds(it)
		if start <= lastEnd+1 {
			out[len(out)-1] = combine(last, it)
			continue
		}
		out = append(out, it)
	}
	return out
}

func lineSpanBounds(s LineSpan) (int, int) { return s.StartLine, s.EndLine }

func combineLineSpanEnd(a, b LineSpan) LineSpan {
	if b.EndLine > a.EndLine {
		a.EndLine = b.EndLine
	}
	return a
}
