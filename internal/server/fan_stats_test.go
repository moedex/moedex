package server

import (
	"context"
	"reflect"
	"testing"

	"moedex/internal/index"
	"moedex/internal/search"
)

// fanWithStats calls c.fan with a query stub that ignores the real index
// content and instead returns the canned Stats for that shard (matched by
// index pointer identity, since fan passes each shard's *index.Index to query).
func fanWithStats(t *testing.T, c *Corpus, perShard []search.Stats) search.Stats {
	t.Helper()
	if len(perShard) != len(c.shards) {
		t.Fatalf("fanWithStats: %d stats for %d shards", len(perShard), len(c.shards))
	}
	want := make(map[*index.Index]search.Stats, len(c.shards))
	for i, sh := range c.shards {
		want[sh.ix] = perShard[i]
	}
	_, agg, err := c.fan(context.Background(), func(_ context.Context, ix *index.Index) ([]search.Match, search.Stats, error) {
		return nil, want[ix], nil
	})
	if err != nil {
		t.Fatalf("fan: %v", err)
	}
	return agg
}

// TestFanAggregatesStats pins down exactly how Corpus.fan combines per-shard
// Stats: the numeric attribution fields sum, QueryAll is the AND across shards
// (true only if every shard fell back to scanning all candidates).
func TestFanAggregatesStats(t *testing.T) {
	dir := t.TempDir()
	buildShard(t, dir, "shard-0000.idx", map[string]string{"a.go": "x\n"})
	buildShard(t, dir, "shard-0001.idx", map[string]string{"b.go": "y\n"})
	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	agg := fanWithStats(t, c, []search.Stats{
		{QueryAll: true, CandidateBlobs: 3, CandidateBytes: 100, CandidateLines: 10, LinesAfterFilter: 4, LinesRE2: 2},
		{QueryAll: false, CandidateBlobs: 5, CandidateBytes: 200, CandidateLines: 20, LinesAfterFilter: 6, LinesRE2: 3},
	})

	want := search.Stats{
		QueryAll:         false, // AND(true, false)
		CandidateBlobs:   8,
		CandidateBytes:   300,
		CandidateLines:   30,
		LinesAfterFilter: 10,
		LinesRE2:         5,
	}
	if agg.QueryAll != want.QueryAll {
		t.Errorf("QueryAll = %v, want %v", agg.QueryAll, want.QueryAll)
	}
	if agg.CandidateBlobs != want.CandidateBlobs {
		t.Errorf("CandidateBlobs = %d, want %d", agg.CandidateBlobs, want.CandidateBlobs)
	}
	if agg.CandidateBytes != want.CandidateBytes {
		t.Errorf("CandidateBytes = %d, want %d", agg.CandidateBytes, want.CandidateBytes)
	}
	if agg.CandidateLines != want.CandidateLines {
		t.Errorf("CandidateLines = %d, want %d", agg.CandidateLines, want.CandidateLines)
	}
	if agg.LinesAfterFilter != want.LinesAfterFilter {
		t.Errorf("LinesAfterFilter = %d, want %d", agg.LinesAfterFilter, want.LinesAfterFilter)
	}
	if agg.LinesRE2 != want.LinesRE2 {
		t.Errorf("LinesRE2 = %d, want %d", agg.LinesRE2, want.LinesRE2)
	}
}

// TestFanAggregationCoversEveryStatsField guards against the drift in code
// review F-071: Corpus.fan hand-sums search.Stats fields (Stats.add is
// unexported), so a future field added to Stats silently vanishes from every
// server-exposed aggregate unless someone remembers to also touch fan(). This
// walks search.Stats by reflection and fails on any field that is neither
// verified aggregated (by TestFanAggregatesStats) nor explicitly, intentionally
// excluded below.
func TestFanAggregationCoversEveryStatsField(t *testing.T) {
	// Fields fan() deliberately does not aggregate, and why. Keep in sync with
	// the comment on Corpus.fan in corpus.go.
	intentionallySkipped := map[string]string{
		"LineFilter":      "an echoed per-query config string, not a metric to combine",
		"ParallelWorkers": "a per-call concurrency knob, not a cross-shard total",
	}
	// Fields fan() does aggregate, exercised by TestFanAggregatesStats above.
	verifiedAggregated := map[string]bool{
		"QueryAll":         true,
		"CandidateBlobs":   true,
		"CandidateBytes":   true,
		"CandidateLines":   true,
		"LinesAfterFilter": true,
		"LinesRE2":         true,
	}

	for field := range reflect.TypeFor[search.Stats]().Fields() {
		name := field.Name
		if _, skipped := intentionallySkipped[name]; skipped {
			continue
		}
		if verifiedAggregated[name] {
			continue
		}
		t.Errorf("search.Stats field %q is new: aggregate it in Corpus.fan and cover it in "+
			"TestFanAggregatesStats, or add it to intentionallySkipped here with a reason", name)
	}
}
