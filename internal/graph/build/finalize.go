package graphbuild

import (
	"fmt"

	"moedex/internal/graph/artifact"
	"moedex/internal/graph/cluster"
	"moedex/internal/graph/diskgraph"
)

// finalizeGraph reuses the live sweep after graph construction. The persisted
// graph and its catalog are shared by clustering and counts, so neither stage
// reopens shards or extracts the corpus again. The caller owns sweep throughout.
//
// previous is supplied only for an unchanged refresh. It remains caller-owned,
// and its matching cluster sidecar is reused when valid. Otherwise this helper
// opens and closes the newly saved graph and regenerates the cluster sidecar.
func finalizeGraph(dir string, sweep *graphSweep, previous *diskgraph.Graph, suppressedRaw, suppressedCrossRepo int) (report cluster.BuildReport, counts GraphBuildCounts, err error) {
	if sweep == nil {
		return report, counts, fmt.Errorf("graph build: missing finalization sweep")
	}
	graphFile := previous
	var cached *cluster.Sidecar
	if previous != nil {
		cached, _ = cluster.Load(artifact.ClusterPath(dir), previous.Generation())
	}
	var cap, edgeCap int
	if cached == nil {
		cap, err = clusterMaxNodes()
		if err != nil {
			return report, counts, err
		}
		edgeCap, err = clusterMaxEdges()
		if err != nil {
			return report, counts, err
		}
	}
	if graphFile == nil {
		graphFile, err = diskgraph.Open(GraphPath(dir))
		if err != nil {
			return report, counts, err
		}
		defer func() {
			if closeErr := graphFile.Close(); err == nil {
				err = closeErr
			}
		}()
	}
	nodes := buildOfflineCatalog(graphFile, sweep)
	if cached != nil {
		report = cached.Report()
	} else {
		report, err = writeClusterSidecar(dir, graphFile, nodes, cap, edgeCap)
		if err != nil {
			return report, counts, err
		}
	}
	counts = measureGraphBuildCountsFromGraph(graphFile, nodes, suppressedRaw, suppressedCrossRepo)
	return report, counts, nil
}
