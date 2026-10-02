package graphbuild

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"moedex/internal/graph"
	"moedex/internal/graph/cluster"
	"moedex/internal/graph/diskgraph"
)

func compactClusterGraph(t *testing.T, factored bool, exclusions []int) *diskgraph.Graph {
	t.Helper()
	b := diskgraph.NewBuilder()
	targets := []diskgraph.Key{{BlobSHA: "a"}, {BlobSHA: "b"}, {BlobSHA: "c"}}
	for _, key := range targets {
		if err := b.AddNode(key); err != nil {
			t.Fatal(err)
		}
	}
	id, err := b.AddTargetSet(targets)
	if err != nil {
		t.Fatal(err)
	}
	for i, exclude := range exclusions {
		source := diskgraph.Key{BlobSHA: "source", SymbolOffset: uint64(i)}
		prototype := diskgraph.Edge{Type: diskgraph.EdgeCalls, Name: "Call", Confidence: graph.Pattern, Evidence: graph.Evidence{BlobSHA: "source", ByteOffset: uint64(i), ByteLength: 1}}
		if factored {
			err = b.AddFactoredSource(source, prototype, id, exclude)
		} else {
			for j, target := range targets {
				if j == exclude {
					continue
				}
				e := prototype
				e.TargetBlob = target.BlobSHA
				e.TargetOffset = target.SymbolOffset
				if err = b.AddEdge(source, e); err != nil {
					break
				}
			}
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "graph")
	if err := b.Save(path); err != nil {
		t.Fatal(err)
	}
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

func TestCompactClusterEligibilityAndWorkCap(t *testing.T) {
	for _, exclusions := range [][]int{{0, 0}, {0, 1}, {-1, 0}} {
		explicit := compactClusterGraph(t, false, exclusions)
		compact := compactClusterGraph(t, true, exclusions)
		a, err := detectClustersWithLimits(context.Background(), explicit, nil, 20, 20)
		if err != nil {
			t.Fatal(err)
		}
		b, err := detectClustersWithLimits(context.Background(), compact, nil, 20, 20)
		if err != nil {
			t.Fatal(err)
		}
		a.BuildMillis = 0
		b.BuildMillis = 0
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("exclusion %v parity differs\n%+v\n%+v", exclusions, a, b)
		}
		if b.Status != cluster.StatusAvailable {
			t.Fatalf("unexpected status %+v", b)
		}
		capped, err := detectClustersWithLimits(context.Background(), compact, nil, 20, b.EligibleEdges-1)
		if err != nil || capped.Status != cluster.StatusOverEdgeCap || capped.EligibleNodes != b.EligibleNodes || capped.EligibleEdges != b.EligibleEdges || len(capped.Clusters) != 0 {
			t.Fatalf("edge work cap %+v %v", capped, err)
		}
		if err := cluster.Save(filepath.Join(t.TempDir(), "clusters.json"), capped); err != nil {
			t.Fatal(err)
		}
		capped, err = detectClustersWithLimits(context.Background(), compact, nil, b.EligibleNodes-1, 20)
		if err != nil || capped.Status != cluster.StatusOverCap {
			t.Fatalf("node cap %+v %v", capped, err)
		}
	}
}

func TestClusterEdgeCapConfiguration(t *testing.T) {
	t.Setenv("MOEDEX_GRAPH_CLUSTER_MAX_EDGES", "")
	if got, err := clusterMaxEdges(); err != nil || got != DefaultClusterMaxEdges {
		t.Fatalf("default %d %v", got, err)
	}
	t.Setenv("MOEDEX_GRAPH_CLUSTER_MAX_EDGES", "42")
	if got, err := clusterMaxEdges(); err != nil || got != 42 {
		t.Fatalf("configured %d %v", got, err)
	}
	for _, bad := range []string{"0", "-1", "nope"} {
		t.Setenv("MOEDEX_GRAPH_CLUSTER_MAX_EDGES", bad)
		if _, err := clusterMaxEdges(); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestCompactClusterRejectsDenseGroupsWithoutExpansion(t *testing.T) {
	const n = 2000
	b := diskgraph.NewBuilder()
	targets := make([]diskgraph.Key, n)
	for i := range targets {
		targets[i] = diskgraph.Key{BlobSHA: fmt.Sprintf("target-%d", i)}
		if err := b.AddNode(targets[i]); err != nil {
			t.Fatal(err)
		}
	}
	id, err := b.AddTargetSet(targets)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		source := diskgraph.Key{BlobSHA: "sources", SymbolOffset: uint64(i)}
		e := diskgraph.Edge{Type: diskgraph.EdgeCalls, Confidence: graph.Pattern, Evidence: graph.Evidence{BlobSHA: "sources", ByteOffset: uint64(i), ByteLength: 1}}
		if err := b.AddFactoredSource(source, e, id, -1); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "graph")
	if err := b.Save(path); err != nil {
		t.Fatal(err)
	}
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	got, err := detectClustersWithLimits(context.Background(), g, nil, 2*n, DefaultClusterMaxEdges)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != cluster.StatusOverEdgeCap || got.EligibleEdges != n*n || got.EligibleNodes != 2*n || len(got.Clusters) != 0 {
		t.Fatalf("dense admission %+v", got)
	}
}
