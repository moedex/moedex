package graphbuild

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"moedex/internal/graph/artifact"
	"moedex/internal/graph/cluster"
	"moedex/internal/graph/diskgraph"
)

type finalizationCloseProbe struct{ calls int }

func (p *finalizationCloseProbe) Close() error { p.calls++; return nil }

func finalizationFixture(t *testing.T) (string, *graphSweep, *diskgraph.Graph, *finalizationCloseProbe) {
	t.Helper()
	dir := t.TempDir()
	writeGraphShards(t, dir, []graphFile{
		{"app", "root.go", "package app\nfunc Alpha() { Beta(); Beta() }\n"},
		{"app", "leaf.go", "package app\nfunc Beta() {}\n"},
	}, 1)
	if _, _, err := BuildGraph(dir); err != nil {
		t.Fatal(err)
	}
	sweep, err := openGraphSweep(dir)
	if err != nil {
		t.Fatal(err)
	}
	probe := &finalizationCloseProbe{}
	sweep.closers = append(sweep.closers, probe)
	t.Cleanup(func() { sweep.Close() })
	g, err := diskgraph.Open(GraphPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return dir, sweep, g, probe
}

// Hide the directory's shard names while keeping the owner's mmaps alive. A
// finalizer that reopens/re-extracts the corpus cannot reproduce these results.
func hideFinalizationShards(t *testing.T, dir string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "shard-*.idx"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if err := os.Rename(path, path+".borrowed"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFinalizationReusesLiveSweepAndMatchesStandalone(t *testing.T) {
	for _, mode := range []string{"available", "node_cap", "edge_cap"} {
		t.Run(mode, func(t *testing.T) {
			dir, sweep, g, probe := finalizationFixture(t)
			if mode == "node_cap" {
				t.Setenv("MOEDEX_GRAPH_CLUSTER_MAX_NODES", "1")
			}
			if mode == "edge_cap" {
				t.Setenv("MOEDEX_GRAPH_CLUSTER_MAX_EDGES", "1")
			}
			wantCluster, err := BuildClusterSidecar(dir)
			if err != nil {
				t.Fatal(err)
			}
			wantCounts, err := measureGraphBuildCounts(dir, 17, 23)
			if err != nil {
				t.Fatal(err)
			}
			wantSidecar, err := cluster.Load(artifact.ClusterPath(dir), g.Generation())
			if err != nil {
				t.Fatal(err)
			}
			hideFinalizationShards(t, dir)
			gotCluster, gotCounts, err := finalizeGraph(dir, sweep, nil, 17, 23)
			if err != nil {
				t.Fatal(err)
			}
			wantCluster.BuildMillis = 0
			gotCluster.BuildMillis = 0
			if !reflect.DeepEqual(gotCluster, wantCluster) || !reflect.DeepEqual(gotCounts, wantCounts) {
				t.Fatalf("finalization differs: cluster %+v/%+v counts %+v/%+v", gotCluster, wantCluster, gotCounts, wantCounts)
			}
			gotSidecar, err := cluster.Load(artifact.ClusterPath(dir), g.Generation())
			if err != nil {
				t.Fatal(err)
			}
			gotSidecar.BuildMillis = 0
			wantSidecar.BuildMillis = 0
			if !reflect.DeepEqual(gotSidecar, wantSidecar) {
				t.Fatalf("cluster memberships changed: %+v/%+v", gotSidecar, wantSidecar)
			}
			if probe.calls != 0 || sweep.corpus == nil || g.BuildID() == "" {
				t.Fatal("borrowed sweep or unrelated graph closed")
			}
		})
	}
}

func TestUnchangedFinalizationBorrowsGraphAndPreservesCache(t *testing.T) {
	dir, sweep, g, probe := finalizationFixture(t)
	before, err := os.ReadFile(artifact.ClusterPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	wantCounts, err := measureGraphBuildCounts(dir, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	hideFinalizationShards(t, dir)
	if err := os.Rename(GraphPath(dir), GraphPath(dir)+".borrowed"); err != nil {
		t.Fatal(err)
	}
	// A valid generation-bound sidecar does not revalidate current build caps.
	t.Setenv("MOEDEX_GRAPH_CLUSTER_MAX_NODES", "invalid")
	report, counts, err := finalizeGraph(dir, sweep, g, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(artifact.ClusterPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || !reflect.DeepEqual(counts, wantCounts) || report.Status == "" {
		t.Fatal("unchanged refresh rewrote cache or changed counts")
	}
	if probe.calls != 0 || g.BuildID() == "" {
		t.Fatal("borrowed resources closed")
	}
}

func TestFinalizationRegeneratesMissingOrStaleSidecarWithoutReopening(t *testing.T) {
	for _, mode := range []string{"missing", "stale"} {
		t.Run(mode, func(t *testing.T) {
			dir, sweep, g, probe := finalizationFixture(t)
			want, err := cluster.Load(artifact.ClusterPath(dir), g.Generation())
			if err != nil {
				t.Fatal(err)
			}
			if mode == "missing" {
				if err := os.Remove(artifact.ClusterPath(dir)); err != nil {
					t.Fatal(err)
				}
			} else {
				stale := *want
				stale.Generation++
				if err := cluster.Save(artifact.ClusterPath(dir), stale); err != nil {
					t.Fatal(err)
				}
			}
			hideFinalizationShards(t, dir)
			report, _, err := finalizeGraph(dir, sweep, g, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if report.Status != want.Status || report.EligibleEdges != want.EligibleEdges {
				t.Fatalf("regenerated report %+v", report)
			}
			if _, err := cluster.Load(artifact.ClusterPath(dir), g.Generation()); err != nil {
				t.Fatal(err)
			}
			if probe.calls != 0 || g.BuildID() == "" {
				t.Fatal("regeneration closed borrowed resources")
			}
		})
	}
}

func TestFinalizationErrorsPreserveBorrowedResources(t *testing.T) {
	dir, sweep, g, probe := finalizationFixture(t)
	if err := os.Remove(artifact.ClusterPath(dir)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOEDEX_GRAPH_CLUSTER_MAX_EDGES", "invalid")
	if _, _, err := finalizeGraph(dir, sweep, g, 0, 0); err == nil || !strings.Contains(err.Error(), "MOEDEX_GRAPH_CLUSTER_MAX_EDGES") {
		t.Fatalf("configuration error %v", err)
	}
	t.Setenv("MOEDEX_GRAPH_CLUSTER_MAX_EDGES", "1000000")
	if err := os.Mkdir(artifact.ClusterPath(dir), 0700); err != nil {
		t.Fatal(err)
	}
	for _, previous := range []*diskgraph.Graph{nil, g} {
		if _, counts, err := finalizeGraph(dir, sweep, previous, 0, 0); err == nil || !strings.Contains(err.Error(), "persist cluster sidecar") || counts.Confidence != nil {
			t.Fatalf("save error lost or counts ran after failure: %+v %v", counts, err)
		}
	}
	if err := os.Rename(GraphPath(dir), GraphPath(dir)+".borrowed"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := finalizeGraph(dir, sweep, nil, 0, 0); !os.IsNotExist(err) {
		t.Fatalf("missing graph error = %v", err)
	}
	if probe.calls != 0 || sweep.corpus == nil || g.BuildID() == "" {
		t.Fatal("failure closed borrowed resources")
	}
	if err := sweep.Close(); err != nil {
		t.Fatal(err)
	}
	if probe.calls != 1 {
		t.Fatalf("owner close calls=%d", probe.calls)
	}
}
