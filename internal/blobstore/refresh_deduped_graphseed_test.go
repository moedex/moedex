package blobstore

import (
	"os"
	"testing"

	graphbuild "moedex/internal/graph/build"
	"moedex/internal/graph/diskgraph"
	server "moedex/internal/serve"
)

// TestDedupedDeltaCarriesGraphSeed covers the served-side half of the
// incremental graph refresh. The delta re-export builds a new dir and swaps it
// in, so without an explicit carry the graph disappears with the old dir and
// every refresh silently degrades to a full corpus-wide rebuild — correct, but
// the whole cost the incremental path exists to avoid. The seed is what makes
// the swap survivable, and the generation stamp is how we can tell.
func TestDedupedDeltaCarriesGraphSeed(t *testing.T) {
	requireGit(t)
	liveDir, casDir, _, _ := buildBaselineAndStagedDelta(t)

	if _, _, err := graphbuild.BuildGraph(liveDir); err != nil {
		t.Fatalf("build baseline graph: %v", err)
	}
	baseline, err := diskgraph.Open(server.GraphPath(liveDir))
	if err != nil {
		t.Fatalf("open baseline graph: %v", err)
	}
	baseGeneration := baseline.Generation()
	baseRoster := baseline.NumCorpusEntries()
	if err := baseline.Close(); err != nil {
		t.Fatal(err)
	}
	if baseRoster == 0 {
		t.Fatal("baseline graph recorded no corpus blobs; there is nothing to diff against")
	}

	_, ds, err := RefreshDedupedShardDir(casDir, liveDir, 1<<10)
	if err != nil {
		t.Fatalf("RefreshDedupedShardDir: %v", err)
	}
	if len(ds.ChangedRepos)+len(ds.AddedRepos)+len(ds.RemovedRepos) == 0 {
		t.Fatal("fixture produced no repo delta; the swap under test never happened")
	}
	if !ds.GraphSeedCarried {
		t.Fatal("GraphSeedCarried = false; the prior graph was lost in the swap")
	}
	if _, err := os.Stat(server.GraphPath(liveDir)); err != nil {
		t.Fatalf("graph missing from the swapped-in dir: %v", err)
	}

	// The carry is verbatim — the swap does not recompute anything — so the
	// generation only advances once the graph refresh actually runs over it.
	carried, err := diskgraph.Open(server.GraphPath(liveDir))
	if err != nil {
		t.Fatalf("open carried graph: %v", err)
	}
	if got := carried.Generation(); got != baseGeneration {
		t.Errorf("carried graph generation = %d, want the untouched %d", got, baseGeneration)
	}
	if err := carried.Close(); err != nil {
		t.Fatal(err)
	}

	_, stats, err := graphbuild.RefreshGraph(liveDir)
	if err != nil {
		t.Fatalf("RefreshGraph over the swapped dir: %v", err)
	}
	if stats.FullRebuild {
		t.Fatalf("refresh over the carried seed fell back to a full rebuild: %s", stats.Reason)
	}
	if stats.Generation != baseGeneration+1 {
		t.Errorf("generation %d -> %d, want a single increment", baseGeneration, stats.Generation)
	}
	if stats.BlobsAdded == 0 {
		t.Error("refresh saw no net-new content despite a changed repo")
	}
}
