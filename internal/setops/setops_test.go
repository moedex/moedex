package setops

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

// refIntersect is the obviously-correct reference intersection, used to hold
// Intersect to the ripgrep-parity-grade "exact set, no approximation" contract.
func refIntersect(a, b []uint64) []uint64 {
	set := make(map[uint64]bool, len(a))
	for _, v := range a {
		set[v] = true
	}
	var out []uint64
	seen := make(map[uint64]bool)
	for _, v := range b {
		if set[v] && !seen[v] {
			out = append(out, v)
			seen[v] = true
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func refUnion(a, b []uint64) []uint64 {
	set := make(map[uint64]bool, len(a)+len(b))
	for _, v := range a {
		set[v] = true
	}
	for _, v := range b {
		set[v] = true
	}
	out := make([]uint64, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// sortedDistinct turns an arbitrary slice into a sorted-distinct one (the input
// contract every setops function assumes).
func sortedDistinct(in []uint64) []uint64 {
	if len(in) == 0 {
		return nil
	}
	cp := append([]uint64(nil), in...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	out := cp[:1]
	for _, v := range cp[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

// isSortedDistinct asserts the parity-relevant output shape.
func isSortedDistinct(s []uint64) bool {
	for i := 1; i < len(s); i++ {
		if s[i] <= s[i-1] {
			return false
		}
	}
	return true
}

func TestIntersectEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		a, b []uint64
		want []uint64
	}{
		{"both empty", nil, nil, nil},
		{"a empty", nil, []uint64{1, 2, 3}, nil},
		{"b empty", []uint64{1, 2, 3}, nil, nil},
		{"disjoint", []uint64{1, 3, 5}, []uint64{2, 4, 6}, nil},
		{"identical", []uint64{1, 2, 3}, []uint64{1, 2, 3}, []uint64{1, 2, 3}},
		{"single match", []uint64{5}, []uint64{5}, []uint64{5}},
		{"single no match", []uint64{5}, []uint64{6}, nil},
		{"subset", []uint64{2, 4}, []uint64{1, 2, 3, 4, 5}, []uint64{2, 4}},
		{"adjacent not equal", []uint64{1, 2, 3}, []uint64{4, 5, 6}, nil},
		{"overlap at ends", []uint64{1, 2, 3, 9}, []uint64{3, 9, 10}, []uint64{3, 9}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Intersect(nil, tc.a, tc.b)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Intersect(%v,%v)=%v want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestUnionEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		a, b []uint64
		want []uint64
	}{
		{"both empty", nil, nil, nil},
		{"a empty", nil, []uint64{1, 2, 3}, []uint64{1, 2, 3}},
		{"b empty", []uint64{1, 2, 3}, nil, []uint64{1, 2, 3}},
		{"disjoint", []uint64{1, 3, 5}, []uint64{2, 4, 6}, []uint64{1, 2, 3, 4, 5, 6}},
		{"identical", []uint64{1, 2, 3}, []uint64{1, 2, 3}, []uint64{1, 2, 3}},
		{"overlap", []uint64{1, 2, 3}, []uint64{2, 3, 4}, []uint64{1, 2, 3, 4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Union(nil, tc.a, tc.b)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Union(%v,%v)=%v want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestIntersectDifferential is the parity-grade discipline: thousands of random
// sorted-distinct pairs, asserting Intersect == naive reference bit-for-bit and
// the output is sorted-distinct. Lengths are varied to exercise the linear-merge
// path, the galloping path (one tiny + one huge), disjoint, and identical cases.
func TestIntersectDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(0xC0FFEE))
	for iter := 0; iter < 5000; iter++ {
		a := randSortedDistinct(rng, rng.Intn(400))
		// Bias toward skewed sizes to exercise galloping.
		var b []uint64
		if iter%3 == 0 {
			b = randSortedDistinctRange(rng, rng.Intn(8000)+1, 0, 20000)
		} else {
			b = randSortedDistinct(rng, rng.Intn(400))
		}
		want := refIntersect(a, b)
		got := Intersect(nil, a, b)
		assertSet(t, "Intersect", a, b, got, want)
		// Commutativity: AND is symmetric, the set must not depend on arg order.
		gotRev := Intersect(nil, b, a)
		assertSet(t, "Intersect(reversed)", b, a, gotRev, want)
	}
}

func TestUnionDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(0xBEEF))
	for iter := 0; iter < 5000; iter++ {
		a := randSortedDistinct(rng, rng.Intn(400))
		b := randSortedDistinct(rng, rng.Intn(400))
		want := refUnion(a, b)
		got := Union(nil, a, b)
		assertSet(t, "Union", a, b, got, want)
	}
}

// TestGallopPathDirectly hammers the |b| >= gallopSkew*|a| branch specifically,
// since the random test only hits it probabilistically.
func TestGallopPathDirectly(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	for iter := 0; iter < 2000; iter++ {
		a := randSortedDistinctRange(rng, rng.Intn(20)+1, 0, 50000)
		b := randSortedDistinctRange(rng, gallopSkew*len(a)+rng.Intn(2000)+10, 0, 50000)
		want := refIntersect(a, b)
		got := Intersect(nil, a, b)
		assertSet(t, "gallop Intersect", a, b, got, want)
	}
}

// TestIntersectDstReuse asserts a non-nil dst's backing array is reused (no new
// allocation when the result fits) and that reuse does not corrupt results.
func TestIntersectDstReuse(t *testing.T) {
	a := []uint64{1, 2, 3, 4, 5, 6, 7, 8}
	b := []uint64{2, 4, 6, 8, 10}
	want := []uint64{2, 4, 6, 8}

	dst := make([]uint64, 0, 16)
	got := Intersect(dst, a, b)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if &got[:cap(got)][0] != &dst[:cap(dst)][0] {
		t.Fatalf("Intersect did not reuse dst backing array")
	}
	// Reuse the same buffer for a different intersection; must not be corrupted
	// by the prior contents.
	a2 := []uint64{10, 20, 30}
	b2 := []uint64{20, 30, 40}
	got2 := Intersect(got[:0], a2, b2)
	if !reflect.DeepEqual(got2, []uint64{20, 30}) {
		t.Fatalf("reused-buffer intersect corrupted: got %v", got2)
	}
}

// TestResultDoesNotAliasInputs guards the parity-critical invariant that the
// result never aliases a or b (a fold that aliased an input it still reads would
// silently drop matches).
func TestResultDoesNotAliasInputs(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for iter := 0; iter < 500; iter++ {
		a := randSortedDistinct(rng, rng.Intn(100)+1)
		b := randSortedDistinct(rng, rng.Intn(100)+1)
		got := Intersect(nil, a, b)
		if aliases(got, a) || aliases(got, b) {
			t.Fatalf("result aliases an input: a=%v b=%v got=%v", a, b, got)
		}
	}
}

func assertSet(t *testing.T, op string, a, b, got, want []uint64) {
	t.Helper()
	if !isSortedDistinct(got) {
		t.Fatalf("%s output not sorted-distinct: %v (a=%v b=%v)", op, got, a, b)
	}
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s mismatch:\n a=%v\n b=%v\n got =%v\n want=%v", op, a, b, got, want)
	}
}

func aliases(x, y []uint64) bool {
	if cap(x) == 0 || cap(y) == 0 {
		return false
	}
	return &x[:cap(x)][0] == &y[:cap(y)][0]
}

// randSortedDistinct returns n sorted-distinct values in [0, 4n+8).
func randSortedDistinct(rng *rand.Rand, n int) []uint64 {
	if n == 0 {
		return nil
	}
	return randSortedDistinctRange(rng, n, 0, uint64(4*n+8))
}

// randSortedDistinctRange returns up to n sorted-distinct values in [lo, hi).
func randSortedDistinctRange(rng *rand.Rand, n int, lo, hi uint64) []uint64 {
	if hi <= lo || n == 0 {
		return nil
	}
	raw := make([]uint64, n)
	for i := range raw {
		raw[i] = lo + uint64(rng.Int63n(int64(hi-lo)))
	}
	return sortedDistinct(raw)
}
