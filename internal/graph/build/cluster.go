package graphbuild

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"moedex/internal/graph"
	"moedex/internal/graph/artifact"
	"moedex/internal/graph/cluster"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
)

const DefaultClusterMaxNodes = 250_000

type offlineLocation struct {
	Repo, Path, AbsPath string
	Line                int
}

type offlineNode struct {
	Symbol, Kind string
	Locations    []offlineLocation
}

func clusterMaxNodes() (int, error) {
	raw := os.Getenv("MOEDEX_GRAPH_CLUSTER_MAX_NODES")
	if raw == "" {
		return DefaultClusterMaxNodes, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("MOEDEX_GRAPH_CLUSTER_MAX_NODES must be a positive integer")
	}
	return value, nil
}

func buildClusterSidecar(dir string) (report cluster.BuildReport, err error) {
	cap, err := clusterMaxNodes()
	if err != nil {
		return report, err
	}
	graphFile, sweep, nodes, err := openOfflineCatalog(dir)
	if err != nil {
		return report, err
	}
	defer func() {
		if closeErr := graphFile.Close(); err == nil {
			err = closeErr
		}
		if closeErr := sweep.Close(); err == nil {
			err = closeErr
		}
	}()
	sidecar, err := detectClusters(context.Background(), graphFile, nodes, cap)
	if err != nil {
		return report, err
	}
	if err := cluster.Save(artifact.ClusterPath(dir), sidecar); err != nil {
		return report, fmt.Errorf("graph build: persist cluster sidecar: %w", err)
	}
	return sidecar.Report(), nil
}

// BuildClusterSidecar rebuilds the generation-bound cluster artifact from an
// already-published graph. It is offline work and is never imported by the
// daemon's graph-serving package.
func BuildClusterSidecar(dir string) (cluster.BuildReport, error) {
	return buildClusterSidecar(dir)
}

func ensureClusterSidecar(dir string, generation uint64) (cluster.BuildReport, error) {
	if sidecar, err := cluster.Load(artifact.ClusterPath(dir), generation); err == nil {
		return sidecar.Report(), nil
	}
	return buildClusterSidecar(dir)
}

func detectClusters(ctx context.Context, graphFile *diskgraph.Graph, catalog map[diskgraph.Key]offlineNode, cap int) (cluster.Sidecar, error) {
	start := time.Now()
	sidecar := cluster.Sidecar{
		Version: cluster.SidecarVersion, Generation: graphFile.Generation(), Cap: cap,
		ObservedNodes: graphFile.NumNodes(), ObservedEdges: graphFile.NumEdges(), Clusters: []cluster.Cluster{},
	}
	type eligibleEdge struct {
		source, target diskgraph.Key
		weight         float64
	}
	nodeKeys := make(map[diskgraph.Key]struct{})
	var eligible []eligibleEdge
	for index, source := range graphFile.Keys() {
		if index&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return cluster.Sidecar{}, err
			}
		}
		for _, edge := range graphFile.Edges(source) {
			if !offlineClusterEdge(edge) {
				continue
			}
			target := diskgraph.Key{BlobSHA: edge.TargetBlob, SymbolOffset: edge.TargetOffset}
			nodeKeys[source] = struct{}{}
			nodeKeys[target] = struct{}{}
			sidecar.EligibleEdges++
			if len(nodeKeys) <= cap {
				eligible = append(eligible, eligibleEdge{source: source, target: target, weight: edge.Weight()})
			}
		}
	}
	sidecar.EligibleNodes = len(nodeKeys)
	if sidecar.EligibleNodes > cap {
		sidecar.Status = cluster.StatusOverCap
		sidecar.BuildMillis = time.Since(start).Milliseconds()
		return sidecar, nil
	}
	if cluster.UnderCovered(sidecar.EligibleNodes, sidecar.ObservedNodes) {
		sidecar.Status = cluster.StatusUnderCovered
		sidecar.BuildMillis = time.Since(start).Milliseconds()
		return sidecar, nil
	}
	nodes := make([]cluster.Node, 0, len(nodeKeys))
	for key := range nodeKeys {
		meta := catalog[key]
		node := cluster.Node{ID: offlineKeyID(key), Name: meta.Symbol}
		if len(meta.Locations) > 0 {
			node.Repo, node.Path, node.Line = meta.Locations[0].Repo, meta.Locations[0].Path, meta.Locations[0].Line
		}
		nodes = append(nodes, node)
	}
	edges := make([]cluster.Edge, 0, len(eligible))
	for _, edge := range eligible {
		edges = append(edges, cluster.Edge{Source: offlineKeyID(edge.source), Target: offlineKeyID(edge.target), Weight: edge.weight})
	}
	sidecar.Status = cluster.StatusAvailable
	sidecar.Clusters = cluster.Detect(nodes, edges)
	sidecar.BuildMillis = time.Since(start).Milliseconds()
	return sidecar, nil
}

func offlineClusterEdge(edge diskgraph.Edge) bool {
	if edge.Confidence < graph.Pattern {
		return false
	}
	switch edge.Type {
	case diskgraph.EdgeCalls, diskgraph.EdgeHTTPCalls, diskgraph.EdgeImports, diskgraph.EdgeDependsOn:
		return true
	default:
		return false
	}
}

func openOfflineCatalog(dir string) (*diskgraph.Graph, *graphSweep, map[diskgraph.Key]offlineNode, error) {
	sweep, err := openGraphSweep(dir)
	if err != nil {
		return nil, nil, nil, err
	}
	graphFile, err := diskgraph.Open(GraphPath(dir))
	if err != nil {
		_ = sweep.Close()
		return nil, nil, nil, err
	}
	return graphFile, sweep, buildOfflineCatalog(graphFile, sweep), nil
}

func buildOfflineCatalog(graphFile *diskgraph.Graph, sweep *graphSweep) map[diskgraph.Key]offlineNode {
	nodes := make(map[diskgraph.Key]offlineNode)
	blobs := make(map[string][]*index.Blob)
	for shard, symbols := range sweep.symbols {
		for id := uint64(0); id < uint64(sweep.idxs[shard].NumBlobs()); id++ {
			blob := sweep.idxs[shard].Blob(id)
			if blob == nil || blob.SHA == "" {
				continue
			}
			blobs[blob.SHA] = append(blobs[blob.SHA], blob)
			for _, symbol := range symbols.Symbols(id) {
				if symbol.NameStart < 0 {
					continue
				}
				key := diskgraph.Key{BlobSHA: blob.SHA, SymbolOffset: uint64(symbol.NameStart)}
				meta := nodes[key]
				if meta.Symbol == "" {
					meta.Symbol, meta.Kind = symbol.Name, symbol.Kind.String()
				}
				meta.Locations = mergeOfflineLocations(meta.Locations, offlineLocations(blob, symbol.NameStart))
				nodes[key] = meta
			}
		}
	}
	locationsForKey := func(key diskgraph.Key) []offlineLocation {
		var locations []offlineLocation
		for _, blob := range blobs[key.BlobSHA] {
			offset := int(key.SymbolOffset)
			if offset > len(blob.Content) {
				offset = len(blob.Content)
			}
			locations = mergeOfflineLocations(locations, offlineLocations(blob, offset))
		}
		return locations
	}
	for _, key := range graphFile.Keys() {
		if _, exists := nodes[key]; !exists {
			nodes[key] = offlineNode{Locations: locationsForKey(key)}
		}
	}
	graphFile.EachEdge(func(source diskgraph.Key, edge diskgraph.Edge) bool {
		target := diskgraph.Key{BlobSHA: edge.TargetBlob, SymbolOffset: edge.TargetOffset}
		if _, exists := nodes[target]; !exists {
			nodes[target] = offlineNode{Locations: locationsForKey(target)}
		}
		switch edge.Type {
		case diskgraph.EdgeDependsOn:
			assignOfflineKind(nodes, source, "File")
			assignOfflineKind(nodes, target, "File")
		case diskgraph.EdgeHTTPCalls:
			assignOfflineKind(nodes, source, "Occurrence")
			assignOfflineKind(nodes, target, "Route")
		case diskgraph.EdgeUnknown:
		default:
			assignOfflineKind(nodes, source, "Occurrence")
			assignOfflineKind(nodes, target, "Occurrence")
		}
		return true
	})
	return nodes
}

func assignOfflineKind(nodes map[diskgraph.Key]offlineNode, key diskgraph.Key, kind string) {
	meta := nodes[key]
	if meta.Symbol != "" {
		return
	}
	priority := map[string]int{"": 0, "Occurrence": 1, "Route": 2, "File": 3}
	if priority[kind] > priority[meta.Kind] {
		meta.Kind = kind
		nodes[key] = meta
	}
}

func offlineLocations(blob *index.Blob, offset int) []offlineLocation {
	line := blob.LineOf(offset)
	locations := make([]offlineLocation, 0, len(blob.Files))
	for _, file := range blob.Files {
		locations = append(locations, offlineLocation{Repo: file.Repo, Path: file.RelPath, AbsPath: file.AbsPath, Line: line})
	}
	return locations
}

func mergeOfflineLocations(left, right []offlineLocation) []offlineLocation {
	seen := make(map[string]bool, len(left)+len(right))
	out := make([]offlineLocation, 0, len(left)+len(right))
	for _, locations := range [][]offlineLocation{left, right} {
		for _, location := range locations {
			id := location.Repo + "\x00" + location.Path + "\x00" + location.AbsPath + "\x00" + strconv.Itoa(location.Line)
			if !seen[id] {
				seen[id] = true
				out = append(out, location)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].AbsPath != out[j].AbsPath {
			return out[i].AbsPath < out[j].AbsPath
		}
		return out[i].Line < out[j].Line
	})
	return out
}

func offlineKeyID(key diskgraph.Key) string {
	return key.BlobSHA + ":" + strconv.FormatUint(key.SymbolOffset, 10)
}
