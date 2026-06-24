//go:build moedex_simd && amd64 && goexperiment.simd

// SIMD differential test. Compiled and RUN only on amd64 with the SIMD kernel
// enabled (moedex_simd && goexperiment.simd). It holds simdIntersect to the
// EXACT same random-input == naive-reference contract as the pure-Go test, AND
// asserts simdIntersect == goIntersect element-for-element on the same inputs —
// so on an amd64 host this proves SIMD == reference == pure-Go, i.e. the native
// kernel cannot break ripgrep parity. On the darwin/arm64 dev box this file is
// not built; its execution + the bench numbers are deferred to an amd64 run by
// the orchestrator (NOT fabricated here).

package setops

import (
	"math/rand"
	"reflect"
	"testing"
)

func TestSIMDMatchesReferenceAndPureGo(t *testing.T) {
	rng := rand.New(rand.NewSource(0x51AD))
	for iter := 0; iter < 5000; iter++ {
		a := randSortedDistinct(rng, rng.Intn(400))
		var b []uint64
		if iter%3 == 0 {
			b = randSortedDistinctRange(rng, rng.Intn(8000)+1, 0, 20000)
		} else {
			b = randSortedDistinct(rng, rng.Intn(400))
		}
		want := refIntersect(a, b)

		gotSIMD := simdIntersect(nil, a, b)
		assertSet(t, "simdIntersect", a, b, gotSIMD, want)

		gotGo := goIntersect(nil, a, b)
		if !reflect.DeepEqual(norm(gotSIMD), norm(gotGo)) {
			t.Fatalf("SIMD != pure-Go:\n a=%v\n b=%v\n simd=%v\n go  =%v", a, b, gotSIMD, gotGo)
		}

		// Commutativity through the SIMD path too.
		gotSIMDRev := simdIntersect(nil, b, a)
		assertSet(t, "simdIntersect(reversed)", b, a, gotSIMDRev, want)
	}
}

// TestSIMDForcedBlockPath drives long b lists so the AVX2 4-wide compare path
// (not just the scalar fallback) executes for the bulk of targets.
func TestSIMDForcedBlockPath(t *testing.T) {
	if !hasAVX2 {
		t.Skip("CPU lacks AVX2; simdIntersect uses the scalar fallback here (covered by the differential test)")
	}
	rng := rand.New(rand.NewSource(123))
	for iter := 0; iter < 3000; iter++ {
		a := randSortedDistinctRange(rng, rng.Intn(200)+1, 0, 100000)
		b := randSortedDistinctRange(rng, rng.Intn(20000)+64, 0, 100000)
		want := refIntersect(a, b)
		got := simdIntersect(nil, a, b)
		assertSet(t, "simdIntersect block path", a, b, got, want)
	}
}

func norm(s []uint64) []uint64 {
	if len(s) == 0 {
		return nil
	}
	return s
}
