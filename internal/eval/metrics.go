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
// The metric math is the load-bearing correctness work and is pinned by
// hand-computed tests. Alongside the rank-position metrics it includes UDCG
// (Utility and Distraction-aware Cumulative Gain), the agent-consumer metric
// called for in research/learned-reranker.md: unlike nDCG, which simply ignores
// non-relevant hits, UDCG penalizes the distractors an LLM agent would ingest
// from the top-k context window.
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
// Distractor grades (negative) and grade 0 are excluded — the ideal ranking
// surfaces relevant docs and no distractors, so it is unchanged by the distractor
// labels UDCG consumes.
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

// unlabeledDistractorPenalty is the implicit per-document cost UDCG charges to a
// retrieved doc in the top-k that the gold marks NEITHER relevant (grade >= 1)
// NOR an explicit hard distractor (grade < 0) — i.e. an UNLABELED doc (grade 0).
// It is deliberately SMALLER than a hard distractor's penalty (a grade -1 costs
// distractorPenalty == 1.0): an unlabeled doc is only a PROBABLE distractor (the
// gold is sparse and single-judge, so it may be an unlabeled-relevant), whereas
// an explicit -1 is a hand-confirmed plausible-but-wrong. The ratio (0.5 vs 1.0)
// encodes "about half as confident this is noise as a labeled hard distractor."
const unlabeledDistractorPenalty = 0.5

// distractorPenalty is the cost of a DISTRACTOR — a document the gold explicitly
// marks plausible-but-wrong with a NEGATIVE grade. The magnitude mirrors gain()
// so the penalty scale is symmetric with the utility scale: penalty(-g) ==
// gain(g) == 2^g - 1. A "hard" distractor (grade -1) costs 1.0 (exactly what a
// grade-1 relevant hit is worth); a costlier one (grade -2) costs 3.0. A
// non-negative grade is not an explicit distractor and costs 0 here (grade 0 is
// handled separately by unlabeledDistractorPenalty).
func distractorPenalty(grade int) float64 {
	if grade >= 0 {
		return 0
	}
	return gain(-grade)
}

// UDCGAtK is Utility- and Distraction-aware Cumulative Gain at cutoff k,
// normalized — the agent-consumer metric from research/learned-reranker.md
// (arXiv 2510.21440). Unlike nDCG, which simply IGNORES non-relevant results, an
// LLM agent ingests the whole top-k context window jointly, so an irrelevant hit
// is not free: a plausible-but-wrong "distractor" actively degrades the answer.
// UDCG therefore REWARDS relevant docs (the nDCG utility term) and SUBTRACTS a
// cost for distractors that appear in the top-k:
//
//   - relevant (grade >= 1):            + gain(grade) * discount(rank)   [same as nDCG]
//   - explicit hard distractor (g < 0): - distractorPenalty(grade) * discount(rank)
//   - unlabeled (grade 0):              - unlabeledDistractorPenalty * discount(rank)
//
// The result is (utility - distraction) / IDCG@k, the SAME ideal normalizer nDCG
// uses: the ideal ranking has the relevant docs at the top and NO distractors, so
// its distraction term is 0 and IDCG is unchanged. Consequences:
//
//   - UDCG <= NDCG always, with equality iff no distractor sits in the top-k.
//   - UDCG can go NEGATIVE: a top-k dominated by hard distractors is worse for an
//     agent than an empty result (which scores 0). This is intentional — it is the
//     "distractors actively harm" thesis the metric exists to express. It is NOT
//     clamped to [0,1].
//
// The position discount applies to penalties too, so a distractor high in the
// window (where the agent anchors) costs more than one near the cutoff.
//
// HONESTY: like nDCG, UDCG is only as good as the labels. The implicit penalty
// assumes an unlabeled top-k hit is noise; against a sparse single-judge gold it
// may penalize an unlabeled-RELEVANT doc. That is why UDCG is a regression WATCH
// (logged), not a hard gate floor, and why the explicit hard-distractor labels
// (the high-confidence penalties) carry the larger weight. Returns 0 when there
// are no relevant docs (IDCG 0 — undefined, matching NDCGAtK). Duplicates: only a
// doc's first (best) occurrence contributes; later ones are skipped.
func UDCGAtK(ranked []string, relevant map[string]int, k int) float64 {
	k = effectiveK(k, len(ranked))
	idcg := idealDCGAtK(relevant, k)
	if idcg == 0 {
		return 0
	}

	seen := make(map[string]bool)
	var udcg float64
	for i := 0; i < k; i++ {
		r := ranked[i]
		if seen[r] {
			continue
		}
		seen[r] = true
		switch grade := relevant[r]; {
		case grade >= 1:
			udcg += gain(grade) * discount(i+1)
		case grade < 0:
			udcg -= distractorPenalty(grade) * discount(i+1)
		default: // grade == 0: unlabeled, an implicit (probable) distractor
			udcg -= unlabeledDistractorPenalty * discount(i+1)
		}
	}
	return udcg / idcg
}
