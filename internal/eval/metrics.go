// Package eval is moedex's evaluation and profiling harness: the measurement
// gate that ranking and performance work depend on. It provides
//
//   - pure, unit-testable IR metrics (recall@k, precision@k, MRR, nDCG@k) over a
//     ranked list of document identifiers and a gold relevance set, and
//   - a Runner that builds a real index + token index from a corpus, runs gold
//     queries through the real (pure-lexical) ranker, and aggregates the metrics
//     into a report.
//
// The relevance key throughout is a document's RelPath: stable across runs and
// human-authorable in a gold set. Metrics take plain Go values (a []string
// ranked list + a map[string]int relevance set) so the math is provable in
// isolation without building an index.
//
// This is slice 1. It deliberately does NOT yet cover: UDCG (the
// distraction-aware, agent-consumer metric called for in research/), dense-arm
// evaluation, or a large gold set — see the package report. The metric math is
// the load-bearing correctness work and is pinned by hand-computed tests.
package eval

import (
	"math"
	"sort"
)

// GoldQuery is one labeled query in a gold set: a query string and the set of
// documents judged relevant to it, keyed by RelPath. Relevance is graded: the
// map value is the relevance grade (>=1 means relevant; larger is more
// relevant). A binary gold set simply uses grade 1 for every relevant doc.
//
// RelPath is the relevance key because it is stable across index rebuilds and
// can be written by hand, unlike a blob ID or SHA.
type GoldQuery struct {
	Query    string         // the query string fed to the ranker
	Relevant map[string]int // RelPath -> graded relevance (>=1 relevant)
}

// NewBinaryGold builds a GoldQuery whose listed RelPaths are all relevant at
// grade 1 — the common case when labels are unary "relevant / not".
func NewBinaryGold(query string, relevant ...string) GoldQuery {
	m := make(map[string]int, len(relevant))
	for _, r := range relevant {
		m[r] = 1
	}
	return GoldQuery{Query: query, Relevant: m}
}

// effectiveK clamps a requested cutoff to [0, n]. A non-positive k (or one past
// the list end) means "the whole list": every metric then considers all of
// ranked. This is the conventional @k convention and keeps callers from having
// to special-case short result lists.
func effectiveK(k, n int) int {
	if k <= 0 || k > n {
		return n
	}
	return k
}

// numRelevant counts documents with grade >= 1 in the relevance set.
func numRelevant(relevant map[string]int) int {
	n := 0
	for _, g := range relevant {
		if g >= 1 {
			n++
		}
	}
	return n
}

// RecallAtK is (relevant docs found in the top k) / (total relevant docs).
// It answers "of everything we should have surfaced, how much did we surface in
// the top k?" Returns 0 when there are no relevant docs (nothing to recall).
//
// ranked is the ordered list of retrieved RelPaths (best first). Duplicates in
// ranked are counted once: the same relevant doc appearing twice does not
// inflate recall.
func RecallAtK(ranked []string, relevant map[string]int, k int) float64 {
	total := numRelevant(relevant)
	if total == 0 {
		return 0
	}
	k = effectiveK(k, len(ranked))
	seen := make(map[string]bool)
	hits := 0
	for i := 0; i < k; i++ {
		r := ranked[i]
		if seen[r] {
			continue
		}
		seen[r] = true
		if relevant[r] >= 1 {
			hits++
		}
	}
	return float64(hits) / float64(total)
}

// PrecisionAtK is (relevant docs in the top k) / k. It answers "of what we put
// in the top k, how much was relevant?" The denominator is the cutoff k (clamped
// to the list length), not the number of relevant docs. Duplicates count once.
// Returns 0 for an empty list.
func PrecisionAtK(ranked []string, relevant map[string]int, k int) float64 {
	k = effectiveK(k, len(ranked))
	if k == 0 {
		return 0
	}
	seen := make(map[string]bool)
	hits := 0
	for i := 0; i < k; i++ {
		r := ranked[i]
		if seen[r] {
			continue
		}
		seen[r] = true
		if relevant[r] >= 1 {
			hits++
		}
	}
	return float64(hits) / float64(k)
}

// MRR is the reciprocal rank of the first relevant document: 1/rank where rank
// is 1-based. With no relevant document retrieved it is 0. (Mean Reciprocal Rank
// across many queries is the mean of this per-query value — computed by the
// Runner.) MRR considers the whole list, since the first relevant hit can be
// anywhere; pass the full ranked slice.
func MRR(ranked []string, relevant map[string]int) float64 {
	for i, r := range ranked {
		if relevant[r] >= 1 {
			return 1.0 / float64(i+1)
		}
	}
	return 0
}

// gain is the graded gain of a relevance grade: 2^grade - 1. This is the
// standard DCG gain function — grade 0 contributes nothing, grade 1 contributes
// 1, and higher grades grow exponentially so a single highly-relevant doc
// outweighs several marginal ones. For a binary gold set (all grades 1) this
// reduces to the binary DCG where every relevant hit contributes gain 1.
func gain(grade int) float64 {
	if grade <= 0 {
		return 0
	}
	return math.Exp2(float64(grade)) - 1
}

// discount is the rank discount 1/log2(rank+1) for 1-based rank — position 1 has
// discount 1/log2(2)=1, position 2 has 1/log2(3), etc.
func discount(rank1Based int) float64 {
	return 1.0 / math.Log2(float64(rank1Based)+1)
}

// NDCGAtK is Normalized Discounted Cumulative Gain at cutoff k, using graded
// gains (2^grade - 1) and the standard log2 position discount. It is DCG@k
// (the discounted gain of the actual ranking) divided by IDCG@k (the DCG of the
// ideal ranking — relevant docs sorted by grade descending). The result is in
// [0,1]: 1.0 is a perfect ranking, 0 when nothing relevant is in the top k.
//
// Tie behavior: NDCGAtK does not reorder ranked. Whatever order the caller
// supplies for equal-scoring documents is the order scored. The ranker upstream
// already produces a deterministic stable order, so ties are scored as the
// ranker emitted them rather than best- or worst-cased here.
//
// Duplicates: if the same RelPath appears more than once in ranked, only its
// first (best) occurrence contributes gain; later occurrences contribute 0.
// This matches recall/precision treating a doc as found-once and prevents a
// ranker that lists one blob's many files from inflating the score.
func NDCGAtK(ranked []string, relevant map[string]int, k int) float64 {
	k = effectiveK(k, len(ranked))
	idcg := idealDCGAtK(relevant, k)
	if idcg == 0 {
		return 0
	}

	seen := make(map[string]bool)
	var dcg float64
	for i := 0; i < k; i++ {
		r := ranked[i]
		if seen[r] {
			continue
		}
		seen[r] = true
		dcg += gain(relevant[r]) * discount(i+1)
	}
	return dcg / idcg
}

// idealDCGAtK is the DCG of the best possible ranking: the relevant grades
// sorted descending, placed at positions 1..k, the rest contributing nothing.
func idealDCGAtK(relevant map[string]int, k int) float64 {
	grades := make([]int, 0, len(relevant))
	for _, g := range relevant {
		if g >= 1 {
			grades = append(grades, g)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(grades)))

	var idcg float64
	for i := 0; i < len(grades) && i < k; i++ {
		idcg += gain(grades[i]) * discount(i+1)
	}
	return idcg
}
