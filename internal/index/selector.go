package index

import "moedex/internal/trigram"

// GramSelector decides, given per-trigram corpus statistics, which trigrams a
// selective index should materialize. It is the policy seam for the selective
// build path (NewSelective / Builder.Finalize): the builder collects stats,
// then asks the selector for the keep-set, then emits postings for the kept
// grams only. Every gram NOT in the keep-set is treated by the query layer as
// "unknown" (force-scan), never as "zero occurrences", so dropping a gram only
// ever widens the candidate set — it can never under-approximate.
//
// FREE (arXiv 2504.12251) is the lineage: skip grams that are too frequent to
// be selective (they cost storage and barely prune). The first slice implements
// the simplest such policy — a document-frequency threshold. The "variable
// length" half of FREE (extending to longer grams where a trigram is
// non-selective) is deferred; this index is trigram-only.
type GramSelector interface {
	// Select returns the set of trigrams to materialize, given each gram's
	// document frequency (number of distinct blobs it occurs in) and the total
	// number of blobs in the corpus. The returned set is authoritative: any gram
	// absent from it is reported NOT-INDEXED by the resulting index.
	Select(docFreq map[trigram.Trigram]int, numBlobs int) map[trigram.Trigram]struct{}

	// Describe returns a short human-readable policy label (for benchmarks/logs).
	Describe() string
}

// FrequencyThresholdSelector keeps a trigram iff its document frequency is at or
// below MaxDocFraction of the corpus. The intuition (FREE): a near-universal
// trigram prunes almost nothing yet costs the most storage, so dropping it is
// the best size-for-selectivity trade. A trigram present in <= MaxDocFraction of
// blobs is kept (it is still a useful filter).
//
// MaxDocFraction is clamped to [0,1]. At 1.0 every gram is kept (equivalent to
// the all-trigram build, but via the selective path). At 0.0 only grams in zero
// blobs would be kept, i.e. effectively nothing — useful only as a degenerate
// test of the force-scan path.
type FrequencyThresholdSelector struct {
	MaxDocFraction float64
}

// Select implements GramSelector. A gram is kept when df <= floor(frac*numBlobs)
// is false in the strict sense; we keep when df <= threshold where threshold is
// the largest integer count allowed. To avoid float edge cases we compare
// df*denom against num: keep iff df/numBlobs <= frac, i.e. df <= frac*numBlobs.
func (s FrequencyThresholdSelector) Select(docFreq map[trigram.Trigram]int, numBlobs int) map[trigram.Trigram]struct{} {
	frac := s.MaxDocFraction
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	keep := make(map[trigram.Trigram]struct{}, len(docFreq))
	// threshold = frac * numBlobs (real-valued); keep gram iff df <= threshold.
	threshold := frac * float64(numBlobs)
	for t, df := range docFreq {
		if float64(df) <= threshold {
			keep[t] = struct{}{}
		}
	}
	return keep
}

// Describe implements GramSelector.
func (s FrequencyThresholdSelector) Describe() string {
	return "freq-threshold(max-df-fraction=" + ftoa(s.MaxDocFraction) + ")"
}

// ftoa renders a fraction compactly without importing strconv at call sites.
func ftoa(f float64) string {
	// Cheap fixed rendering good enough for a label (e.g. 0.50). Avoids fmt.
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	whole := int(f)
	frac := int((f-float64(whole))*100 + 0.5)
	d0 := byte('0' + whole)
	d1 := byte('0' + frac/10)
	d2 := byte('0' + frac%10)
	return string([]byte{d0, '.', d1, d2})
}
