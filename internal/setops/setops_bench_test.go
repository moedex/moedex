package setops

import (
	"math/rand"
	"testing"
)

// Posting-list-shaped benchmark inputs. Three regimes that map to real trigram
// AND folds (research/simd-kernel.md query mix):
//   - Dense:   two common trigrams -> two large comparable lists (linear merge).
//   - Skewed:  one rare + one common trigram -> tiny ∩ huge (galloping path).
//   - Sparse:  two rare trigrams -> two small lists (overhead-dominated).
//
// All inputs are fixed-seed so the pure-Go (arm64) baseline and the amd64 SIMD
// run measure the SAME data. Each bench reuses a single dst buffer across
// iterations to reflect the andQ.Eval ping-pong and isolate compute from alloc;
// b.ReportAllocs() makes the per-fold allocation a first-class number.

func benchInputs(seed int64, lenA, lenB int, span uint64) (a, b []uint64) {
	rng := rand.New(rand.NewSource(seed))
	a = randSortedDistinctRange(rng, lenA, 0, span)
	b = randSortedDistinctRange(rng, lenB, 0, span)
	return a, b
}

func benchIntersect(bb *testing.B, lenA, lenB int, span uint64) {
	a, b := benchInputs(1, lenA, lenB, span)
	dst := make([]uint64, 0, lenA+8)
	bb.ReportAllocs()
	bb.ResetTimer()
	for i := 0; i < bb.N; i++ {
		dst = Intersect(dst[:0], a, b)
	}
	_ = dst
}

func BenchmarkIntersectDense(b *testing.B) {
	// Two common trigrams: ~50k each over a 200k blob space (~25% selectivity).
	benchIntersect(b, 50000, 50000, 200000)
}

func BenchmarkIntersectSkewed(b *testing.B) {
	// Rare AND common: 200 blobs ∩ 80k blobs -> galloping is the win here.
	benchIntersect(b, 200, 80000, 500000)
}

func BenchmarkIntersectSparse(b *testing.B) {
	// Two rare trigrams: small lists, overhead-dominated (where SIMP/SIMD setup
	// can LOSE to the scalar merge — the honest no-win-at-small-scale case).
	benchIntersect(b, 64, 64, 5000)
}

func BenchmarkIntersectMedium(b *testing.B) {
	// Mid-size comparable lists, the typical multi-term AND fold.
	benchIntersect(b, 4000, 6000, 100000)
}

func BenchmarkUnionDense(b *testing.B) {
	a, bb2 := benchInputs(2, 50000, 50000, 200000)
	dst := make([]uint64, 0, 110000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dst = Union(dst[:0], a, bb2)
	}
	_ = dst
}
