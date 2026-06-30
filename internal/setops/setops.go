// Package setops owns the sorted-uint64 set algebra used to fold trigram
// posting lists into a candidate blob-ID set (the AND/OR reduction in
// internal/query). It exists to (a) give the hot intersection loop a clean
// boundary so a native/SIMD kernel can be swapped in behind a build tag without
// query/search ever seeing assembly or cgo, and (b) carry the pure-Go
// allocation-hygiene wins (caller-reusable dst buffer, galloping when one list
// is far shorter) that the research note (research/simd-kernel.md) flags as the
// first lever — reachable in pure Go on every architecture, before any SIMD.
//
// CORRECTNESS CONTRACT (load-bearing for ripgrep parity): every function takes
// sorted, strictly-ascending (distinct) []uint64 inputs and returns a sorted,
// strictly-ascending result. Intersect and Union compute the exact set
// intersection / union — no over- or under-approximation. The candidate set
// query.Eval produces is a *necessary condition* verified downstream by
// internal/search, so as long as these return exactly the same sets the old
// hand-rolled intersect/union did, parity is preserved. This is guarded by the
// differential property test in setops_test.go (both the pure-Go and the native
// kernel are held to the same random-input == naive-reference assertion).
//
// DISPATCH: Intersect delegates its core merge to the package variable
// intersectImpl, set at init time by exactly one build-tagged file:
//   - setops_fallback.go (default, every arch incl. darwin/arm64 and
//     CGO_ENABLED=0): intersectImpl = goIntersect.
//   - setops_simd_amd64.go (//go:build moedex_simd && amd64 && goexperiment.simd):
//     intersectImpl = simdIntersect (archsimd AVX2), itself falling back to
//     goIntersect when the CPU lacks AVX2. No cgo, no go.mod dependency.
package setops

// intersectImpl is the swappable core of Intersect. It MUST honor the same
// contract as goIntersect: sorted-distinct in, exact sorted-distinct
// intersection written into dst[:0]'s backing array, result returned. The
// build-tagged files assign it in init.
var intersectImpl func(dst, a, b []uint64) []uint64

// Intersect returns the sorted intersection of two sorted, distinct slices,
// writing into dst (its capacity is reused to avoid per-fold allocation; dst's
// length is ignored, only its backing array is reused). The returned slice may
// alias dst's backing array but never aliases a or b. Passing dst == nil is
// fine (a fresh slice is allocated). dst must not alias a or b.
func Intersect(dst, a, b []uint64) []uint64 {
	return intersectImpl(dst, a, b)
}

// Union returns the sorted union of two sorted, distinct slices, writing into
// dst's reused capacity. The returned slice never aliases a or b. dst must not
// alias a or b. Union is pure Go on every architecture: it is allocation-bound,
// not compare-bound (it copies every element of the union), so SIMD offers no
// structural win — the research note scopes the native kernel to intersection.
func Union(dst, a, b []uint64) []uint64 {
	return goUnion(dst, a, b)
}

// goIntersect is the pure-Go intersection: a sorted merge with a galloping
// (exponential-search) fast path when one list is much shorter than the other
// — the common "rare trigram AND a common trigram" posting-list shape. It is
// the default impl on every arch and the fallback the SIMD path defers to when
// AVX2 is unavailable.
func goIntersect(dst, a, b []uint64) []uint64 {
	out := dst[:0]
	// Ensure a is the shorter list so the gallop driver scans the small side
	// and binary-jumps the large side. Intersection is commutative, so this is
	// purely a work-minimizing reorder.
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(a) == 0 {
		return out
	}
	// Heuristic: gallop only when the size skew is large enough that the
	// jump-search saves more than the linear merge would cost. For comparable
	// lengths the straight merge has better branch behavior.
	if len(b) >= gallopSkew*len(a) {
		return gallopIntersect(out, a, b)
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		av, bv := a[i], b[j]
		switch {
		case av == bv:
			out = append(out, av)
			i++
			j++
		case av < bv:
			i++
		default:
			j++
		}
	}
	return out
}

// gallopSkew is the |b|/|a| ratio at or above which goIntersect switches from a
// linear merge to galloping. Below this the linear merge is cheaper (and has
// fewer branch mispredictions). Chosen conservatively; the bench validates it.
const gallopSkew = 32

// gallopIntersect intersects a short list a into a long list b using
// exponential (galloping) search to advance through b. For each element of a it
// doubles the probe offset into b until it overshoots, then binary-searches the
// bracketed window. This is O(|a| * log(|b|)) instead of O(|a|+|b|), a large win
// when |a| << |b|. Inputs are sorted-distinct; out is the reused buffer.
func gallopIntersect(out, a, b []uint64) []uint64 {
	j := 0 // current floor in b
	for _, target := range a {
		// Exponential search for the smallest index >= j whose value >= target.
		// hi is reused below as the binary-search upper bound once the window is
		// bracketed — same variable, second job — so the clamp right after this
		// loop also doubles as the binary search's initial bound.
		lo := j
		hi := j + 1
		for hi < len(b) && b[hi-1] < target {
			lo = hi
			hi *= 2
		}
		if hi > len(b) {
			hi = len(b)
		}
		// Binary search the [lo, hi) window for target.
		k := lo
		for k < hi {
			mid := int(uint(k+hi) >> 1)
			if b[mid] < target {
				k = mid + 1
			} else {
				hi = mid
			}
		}
		if k >= len(b) {
			// target is past the end of b; no further a element can match.
			break
		}
		if b[k] == target {
			out = append(out, target)
			k++
		}
		j = k // every subsequent target is larger, so never look back.
	}
	return out
}

// goUnion is the pure-Go sorted union, writing into dst's reused capacity.
func goUnion(dst, a, b []uint64) []uint64 {
	out := dst[:0]
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		av, bv := a[i], b[j]
		switch {
		case av == bv:
			out = append(out, av)
			i++
			j++
		case av < bv:
			out = append(out, av)
			i++
		default:
			out = append(out, bv)
			j++
		}
	}
	out = append(out, a[i:]...)
	out = append(out, b[j:]...)
	return out
}
