//go:build moedex_simd && amd64 && goexperiment.simd

// Native SIMD intersection kernel. Compiled ONLY under the three-way build
// constraint moedex_simd && amd64 && goexperiment.simd, so the default build —
// and every non-amd64 / non-GOEXPERIMENT build, including the darwin/arm64 dev
// box and any CGO_ENABLED=0 static binary — uses the pure-Go goIntersect from
// setops_fallback.go instead. There is NO cgo here and NO go.mod dependency:
// simd/archsimd ships with the Go 1.26 toolchain and is reachable only when
// GOEXPERIMENT=simd is set (expressed as the goexperiment.simd build tag). This
// preserves the load-bearing zero-required-dependency / static-binary posture.
//
// archsimd is experimental (no Go 1 compatibility promise); it lives behind the
// opt-in moedex_simd tag the default build never sets, so its instability is
// contained. The API spelling used here (Uint64x4, BroadcastUint64x4,
// LoadUint64x4Slice, Uint64x4.Equal -> Mask64x4, Mask64x4.ToBits, X86.AVX2) was
// verified against `GOEXPERIMENT=simd GOARCH=amd64 go doc simd/archsimd` on the
// Go 1.26.3 toolchain.
//
// CORRECTNESS: simdIntersect computes the exact set intersection and is held to
// the SAME random-input == naive-reference differential assertion as goIntersect
// (see setops_simd_test.go, which runs on amd64). It can never under- or
// over-approximate, so ripgrep parity is preserved. Whenever AVX2 is absent at
// runtime it falls back to goIntersect.

package setops

import "simd/archsimd"

// hasAVX2 is resolved once at init. archsimd's Uint64x4 path needs AVX2 (256-bit
// integer vectors); without it we must use the scalar pure-Go merge.
var hasAVX2 = archsimd.X86.AVX2()

func init() {
	intersectImpl = simdIntersect
}

// simdVecWidth is the number of uint64 lanes in a 256-bit AVX2 vector.
const simdVecWidth = 4

// simdIntersect intersects two sorted-distinct uint64 lists. It uses AVX2 to
// test each element of the short list against a 4-wide block of the long list
// in one packed compare, advancing a galloping cursor through the long list.
// This vectorizes the equality probe that dominates the rare-AND-common
// posting-list shape. When AVX2 is unavailable, or for inputs too small to
// amortize the vector setup, it defers to the pure-Go goIntersect so behavior
// is identical across CPUs.
func simdIntersect(dst, a, b []uint64) []uint64 {
	if !hasAVX2 {
		return goIntersect(dst, a, b)
	}
	out := dst[:0]
	// Drive the scan from the shorter list (intersection is commutative).
	if len(a) > len(b) {
		a, b = b, a
	}
	// Too small to be worth the vector setup; the scalar merge wins on short
	// lists (the predicted common case at this corpus scale).
	if len(a) == 0 || len(b) < simdVecWidth {
		return goIntersect(out, a, b)
	}

	j := 0 // floor cursor into the long list b
	for _, target := range a {
		// Galloping advance: skip whole 4-wide blocks of b whose maximum
		// (last lane) is still below target, so the vector compare only runs on
		// a block that can actually contain target.
		for j+simdVecWidth <= len(b) && b[j+simdVecWidth-1] < target {
			j += simdVecWidth
		}
		if j+simdVecWidth <= len(b) {
			// One packed compare: broadcast target across 4 lanes and test
			// equality against b[j:j+4]. ToBits()!=0 iff some lane matched.
			block := archsimd.LoadUint64x4Slice(b[j : j+simdVecWidth])
			want := archsimd.BroadcastUint64x4(target)
			if block.Equal(want).ToBits() != 0 {
				out = append(out, target)
			}
			// Do NOT advance j here: the next (larger) target may still match
			// within this same block, and the block-skip loop above will move j
			// forward once target exceeds the block.
			continue
		}
		// Tail: fewer than 4 elements of b remain. Linear-scan them, then we are
		// done with the vector path for every remaining (larger) target.
		out = simdTail(out, a, target, b[j:])
		break
	}
	return out
}

// simdTail finishes the intersection once the long list has fewer than a full
// vector remaining. It scans the tail of b for target and for every subsequent
// element of a, using the fact that both are sorted-distinct. a is the full
// short list; target is the current a element (the others after it are found by
// re-scanning a from target). To keep it simple and obviously correct we just
// run the scalar merge over the remaining suffixes.
func simdTail(out, a []uint64, target uint64, bTail []uint64) []uint64 {
	// Find target's position in a so we merge the matching suffixes.
	ai := 0
	for ai < len(a) && a[ai] < target {
		ai++
	}
	bi := 0
	for ai < len(a) && bi < len(bTail) {
		av, bv := a[ai], bTail[bi]
		switch {
		case av == bv:
			out = append(out, av)
			ai++
			bi++
		case av < bv:
			ai++
		default:
			bi++
		}
	}
	return out
}
