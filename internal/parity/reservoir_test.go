package parity

import (
	"fmt"
	"math/rand"
	"testing"
)

// F-053: reservoirAdd must implement standard Algorithm R uniform reservoir
// sampling — after n observations, every item has an equal (max/n) probability
// of surviving in the final reservoir. The old implementation replaced a slot
// with constant probability 1/max once full, which over-weights early items
// instead of giving every item the same retention probability.
func TestReservoirAdd_UniformRetentionProbability(t *testing.T) {
	const max = 10
	const n = 50
	const trials = 30000

	counts := make([]int, n)
	for trial := 0; trial < trials; trial++ {
		rng := rand.New(rand.NewSource(int64(trial)))
		var res []string
		for i := 0; i < n; i++ {
			res = reservoirAdd(rng, res, i, fmt.Sprintf("item%d", i), max)
		}
		if len(res) != max {
			t.Fatalf("trial %d: reservoir has %d items, want %d", trial, len(res), max)
		}
		for _, v := range res {
			var idx int
			if _, err := fmt.Sscanf(v, "item%d", &idx); err != nil {
				t.Fatalf("unparseable item %q: %v", v, err)
			}
			counts[idx]++
		}
	}

	wantP := float64(max) / float64(n) // 10/50 = 0.20 for every item, uniformly
	const tolerance = 0.02
	for i, c := range counts {
		p := float64(c) / float64(trials)
		if p < wantP-tolerance || p > wantP+tolerance {
			t.Fatalf("item %d retained with empirical probability %.4f, want ~%.4f (+/-%.2f)", i, p, wantP, tolerance)
		}
	}
}

// The first `max` observations must always be kept outright (reservoir not yet
// full), regardless of rng draws.
func TestReservoirAdd_FirstMaxItemsAlwaysKept(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var res []string
	const max = 5
	for i := 0; i < max; i++ {
		res = reservoirAdd(rng, res, i, fmt.Sprintf("item%d", i), max)
	}
	if len(res) != max {
		t.Fatalf("len(res) = %d, want %d", len(res), max)
	}
	for i, v := range res {
		want := fmt.Sprintf("item%d", i)
		if v != want {
			t.Fatalf("res[%d] = %q, want %q", i, v, want)
		}
	}
}

// Once full, the reservoir must never grow past max regardless of how many
// further items are observed.
func TestReservoirAdd_NeverExceedsMax(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	var res []string
	const max = 3
	for i := 0; i < 1000; i++ {
		res = reservoirAdd(rng, res, i, fmt.Sprintf("item%d", i), max)
		if len(res) > max {
			t.Fatalf("after %d items, len(res) = %d, want <= %d", i+1, len(res), max)
		}
	}
}
