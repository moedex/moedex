package rank

import (
	"math"
	"sort"
)

// armScore is the common (blob, score, span) result shape shared by the dense,
// symbol-name, and path ranking arms — only lexicalArm's lexScore differs (it
// carries no span; lexical spans are computed separately in lexicalSpans).
type armScore struct {
	blob  uint64
	score float64
	span  LineSpan
}

// sortByScoreThenBlob sorts items by score descending, breaking ties by blob
// ascending for determinism — the single (score desc, blob asc) rule every
// ranking arm, and the final Rank order, shares.
func sortByScoreThenBlob[T any](items []T, score func(T) float64, blob func(T) uint64) {
	sort.SliceStable(items, func(i, j int) bool {
		si, sj := score(items[i]), score(items[j])
		if si != sj {
			return si > sj
		}
		return blob(items[i]) < blob(items[j])
	})
}

// coverageEntry is a precomputed per-candidate record shared by the symbol-name
// and path arms: one symbol/path's DISTINCT subtokens (tokenized once at index
// build, never per query) and the LineSpan it contributes if it ends up the
// qualifying entry.
type coverageEntry struct {
	subtoks []string
	span    LineSpan
}

// coverageHits counts how many of subtoks are query terms (want). subtoks are
// distinct (deduped at build), so each match is a distinct query term.
func coverageHits(subtoks []string, want map[string]bool) int {
	hits := 0
	for _, t := range subtoks {
		if want[t] {
			hits++
		}
	}
	return hits
}

// minHitsFor turns a coverage fraction into the minimum integer hit count a
// candidate must reach: ceil(coverage * nTerms), floored at 1 so a positive
// threshold always requires a real match.
func minHitsFor(coverage float64, nTerms int) int {
	minHits := int(math.Ceil(coverage * float64(nTerms)))
	return max(minHits, 1)
}

// wantSet turns query terms into a distinct lookup set, dropping any empty term.
func wantSet(terms []string) map[string]bool {
	want := make(map[string]bool, len(terms))
	for _, t := range terms {
		if t != "" {
			want[t] = true
		}
	}
	return want
}

// gatherCandidates unions postings[t] over every term in want — the blobs that
// share at least one query term with a candidate's subtokens, gathered without
// a full-corpus scan.
func gatherCandidates(want map[string]bool, postings map[string][]uint64) map[uint64]struct{} {
	cand := map[uint64]struct{}{}
	for t := range want {
		for _, b := range postings[t] {
			cand[b] = struct{}{}
		}
	}
	return cand
}

// coverageArm implements the candidate-gather + coverage-gate + sort boilerplate
// shared by the symbol-name and path arms: build the query's distinct term set,
// gather candidates from postings, then ask agg to turn each candidate's
// precomputed entries into a (score, span, ok) per the arm's own scoring
// policy — symbolArm sums every qualifying entry's hits (sumQualifying),
// pathArm takes the single best entry (bestSingle); see those for why the
// policies differ. Returns nil when postings is nil (arm disabled/unbuilt) or
// terms is empty.
func coverageArm(terms []string, minCoverage float64, postings map[string][]uint64, info map[uint64][]coverageEntry, agg func(entries []coverageEntry, want map[string]bool, minHits int) (score float64, span LineSpan, ok bool)) []armScore {
	if postings == nil || len(terms) == 0 {
		return nil
	}
	want := wantSet(terms)
	if len(want) == 0 {
		return nil
	}
	minHits := minHitsFor(minCoverage, len(want))
	cand := gatherCandidates(want, postings)
	if len(cand) == 0 {
		return nil
	}
	out := make([]armScore, 0, len(cand))
	for blob := range cand {
		score, span, ok := agg(info[blob], want, minHits)
		if !ok {
			continue
		}
		out = append(out, armScore{blob: blob, score: score, span: span})
	}
	sortByScoreThenBlob(out, func(a armScore) float64 { return a.score }, func(a armScore) uint64 { return a.blob })
	return out
}

// sumQualifying is the symbol arm's aggregation policy: every entry that
// individually meets minHits contributes its hit count to the blob's total
// score, and the contributed span is the qualifying entry with the most hits.
// A blob defining several symbols that each match the query independently
// outranks one with a single match (TestSymbolArmSumsMultipleQualifyingSymbols).
func sumQualifying(entries []coverageEntry, want map[string]bool, minHits int) (float64, LineSpan, bool) {
	var total float64
	bestHits := -1
	var bestSpan LineSpan
	for _, e := range entries {
		hits := coverageHits(e.subtoks, want)
		if hits < minHits {
			continue // coverage gate: too weak a match to vote
		}
		total += float64(hits)
		if hits > bestHits {
			bestHits = hits
			bestSpan = e.span
		}
	}
	return total, bestSpan, total > 0
}

// bestSingle is the path arm's aggregation policy: a candidate (a SHA-deduped
// blob may have several file paths) scores by its SINGLE best-matching entry,
// never summing across entries — so a multi-path blob is judged the same as a
// fresh per-query tokenize of just its best path
// (TestPathArmMultiPathBlobBestMatch). The coverage gate applies once, to that
// best hit count.
func bestSingle(entries []coverageEntry, want map[string]bool, minHits int) (float64, LineSpan, bool) {
	bestHits := 0
	var bestSpan LineSpan
	for _, e := range entries {
		hits := coverageHits(e.subtoks, want)
		if hits > bestHits {
			bestHits = hits
			bestSpan = e.span
		}
	}
	return float64(bestHits), bestSpan, bestHits >= minHits
}
