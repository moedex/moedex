package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"moedex/internal/graph"
	"moedex/internal/graph/cluster"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/mcp"
)

const (
	ClusterFileName        = "corpus-graph.clusters.json"
	DefaultClusterMaxNodes = 250_000
	defaultClusterLimit    = 50
	maxClusterLimit        = 200
)

func ClusterPath(dir string) string { return filepath.Join(dir, ClusterFileName) }

// ClusterTool returns the list_clusters handler. Requests only page the
// generation-matched sidecar loaded with the graph snapshot; Louvain never runs
// on the serving path.
func (g *GraphToolset) ClusterTool() mcp.ToolHandler { return &listClustersTool{owner: g} }

type listClustersTool struct{ owner *GraphToolset }

func (t *listClustersTool) Name() string { return "list_clusters" }

func (t *listClustersTool) Descriptor() map[string]interface{} {
	return map[string]interface{}{
		"name":        t.Name(),
		"description": "Page precomputed graph-community summaries, or page members of one cluster by cluster_id. Communities are generation-bound and built only during graph build/refresh.",
		"inputSchema": map[string]interface{}{
			"type": "object", "additionalProperties": false,
			"properties": map[string]interface{}{
				"cluster_id": map[string]interface{}{"type": "integer", "minimum": 1, "description": "Optional cluster to inspect; omit for summaries."},
				"offset":     map[string]interface{}{"type": "integer", "minimum": 0, "description": "Zero-based page offset (default 0)."},
				"limit":      map[string]interface{}{"type": "integer", "minimum": 1, "maximum": maxClusterLimit, "description": "Page size (default 50, maximum 200)."},
			},
		},
	}
}

type clusterUnavailableResult struct {
	Available       bool   `json:"available"`
	Status          string `json:"status"`
	GraphGeneration uint64 `json:"graph_generation"`
	ObservedNodes   int    `json:"observed_nodes"`
	ObservedEdges   int    `json:"observed_edges"`
	EligibleNodes   int    `json:"eligible_nodes,omitempty"`
	EligibleEdges   int    `json:"eligible_edges,omitempty"`
	Cap             int    `json:"cap,omitempty"`
	Guidance        string `json:"guidance"`
}

type clusterSummary struct {
	ClusterID   int    `json:"cluster_id"`
	Label       string `json:"label"`
	MemberCount int    `json:"member_count"`
}

type clusterListResult struct {
	Available     bool             `json:"available"`
	Status        string           `json:"status"`
	Generation    uint64           `json:"generation"`
	EligibleNodes int              `json:"eligible_nodes"`
	EligibleEdges int              `json:"eligible_edges"`
	Offset        int              `json:"offset"`
	Limit         int              `json:"limit"`
	Total         int              `json:"total"`
	Clusters      []clusterSummary `json:"clusters"`
}

type clusterDetailResult struct {
	Available     bool            `json:"available"`
	Status        string          `json:"status"`
	Generation    uint64          `json:"generation"`
	EligibleNodes int             `json:"eligible_nodes"`
	EligibleEdges int             `json:"eligible_edges"`
	Offset        int             `json:"offset"`
	Limit         int             `json:"limit"`
	TotalMembers  int             `json:"total_members"`
	Cluster       cluster.Cluster `json:"cluster"`
}

func (t *listClustersTool) Call(ctx context.Context, raw json.RawMessage) (map[string]interface{}, error) {
	var args struct {
		ClusterID *int `json:"cluster_id"`
		Offset    *int `json:"offset"`
		Limit     *int `json:"limit"`
	}
	if err := decodeOptionalArgs(raw, &args); err != nil {
		return invalidGraphArgs(err), nil
	}
	offset, limit := 0, defaultClusterLimit
	if args.Offset != nil {
		offset = *args.Offset
	}
	if args.Limit != nil {
		limit = *args.Limit
	}
	if offset < 0 {
		return invalidGraphArgs(fmt.Errorf("offset must be non-negative")), nil
	}
	if limit < 1 || limit > maxClusterLimit {
		return invalidGraphArgs(fmt.Errorf("limit must be between 1 and %d", maxClusterLimit)), nil
	}
	if args.ClusterID != nil && *args.ClusterID < 1 {
		return invalidGraphArgs(fmt.Errorf("cluster_id must be positive")), nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot := t.owner.acquire()
	if snapshot == nil {
		return mcp.TextResult("graph tools are closed", true), nil
	}
	defer snapshot.wg.Done()

	if snapshot.clusters == nil {
		result := clusterUnavailableResult{
			Status: "unavailable", GraphGeneration: snapshot.graph.Generation(),
			ObservedNodes: snapshot.graph.NumNodes(), ObservedEdges: snapshot.graph.NumEdges(),
			Guidance: "cluster sidecar is absent, stale, or unreadable; rebuild or refresh the graph to regenerate it",
		}
		if snapshot.clusterErr != nil {
			result.Guidance += ": " + snapshot.clusterErr.Error()
		}
		return mcp.StructuredResult("list_clusters unavailable: "+result.Guidance, result, false), nil
	}
	sidecar := snapshot.clusters
	if sidecar.Status == cluster.StatusOverCap {
		result := clusterUnavailableResult{
			Status: cluster.StatusOverCap, GraphGeneration: sidecar.Generation,
			ObservedNodes: snapshot.graph.NumNodes(), ObservedEdges: snapshot.graph.NumEdges(),
			EligibleNodes: sidecar.EligibleNodes, EligibleEdges: sidecar.EligibleEdges, Cap: sidecar.Cap,
			Guidance: fmt.Sprintf("eligible graph has %d nodes, above cap %d; raise MOEDEX_GRAPH_CLUSTER_MAX_NODES and rebuild", sidecar.EligibleNodes, sidecar.Cap),
		}
		return mcp.StructuredResult("list_clusters unavailable: "+result.Guidance, result, false), nil
	}

	if args.ClusterID == nil {
		end := pageEnd(offset, limit, len(sidecar.Clusters))
		start := offset
		if start > len(sidecar.Clusters) {
			start = len(sidecar.Clusters)
		}
		summaries := make([]clusterSummary, 0, end-start)
		for _, community := range sidecar.Clusters[start:end] {
			summaries = append(summaries, clusterSummary{ClusterID: community.ClusterID, Label: community.Label, MemberCount: community.MemberCount})
		}
		result := clusterListResult{Available: true, Status: cluster.StatusAvailable, Generation: sidecar.Generation,
			EligibleNodes: sidecar.EligibleNodes, EligibleEdges: sidecar.EligibleEdges,
			Offset: offset, Limit: limit, Total: len(sidecar.Clusters), Clusters: summaries}
		return mcp.StructuredResult(fmt.Sprintf("list_clusters: %d of %d summaries", len(summaries), len(sidecar.Clusters)), result, false), nil
	}

	for i, community := range sidecar.Clusters {
		if i&255 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if community.ClusterID != *args.ClusterID {
			continue
		}
		start := offset
		if start > len(community.Members) {
			start = len(community.Members)
		}
		end := pageEnd(start, limit, len(community.Members))
		paged := community
		paged.Members = append([]cluster.Node{}, community.Members[start:end]...)
		result := clusterDetailResult{Available: true, Status: cluster.StatusAvailable, Generation: sidecar.Generation,
			EligibleNodes: sidecar.EligibleNodes, EligibleEdges: sidecar.EligibleEdges,
			Offset: offset, Limit: limit, TotalMembers: community.MemberCount, Cluster: paged}
		return mcp.StructuredResult(fmt.Sprintf("list_clusters: cluster %d members %d-%d of %d", community.ClusterID, start, end, community.MemberCount), result, false), nil
	}
	return mcp.TextResult(fmt.Sprintf("cluster_id %d not found", *args.ClusterID), true), nil
}

func pageEnd(offset, limit, total int) int {
	if offset >= total {
		return total
	}
	if limit >= total-offset {
		return total
	}
	return offset + limit
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

func isClusterEdge(edge diskgraph.Edge) bool {
	if edge.Confidence < graph.Verified {
		return false
	}
	switch edge.Type {
	case diskgraph.EdgeCalls, diskgraph.EdgeHTTPCalls, diskgraph.EdgeImports, diskgraph.EdgeDependsOn:
		return true
	default:
		return false
	}
}

func buildClusterSidecar(dir string) (report cluster.BuildReport, err error) {
	cap, err := clusterMaxNodes()
	if err != nil {
		return report, err
	}
	snapshot, err := openGraphSnapshotWithClusters(dir, false)
	if err != nil {
		return report, err
	}
	defer func() {
		if closeErr := snapshot.close(); err == nil {
			err = closeErr
		}
	}()
	sidecar, err := detectSnapshotClusters(context.Background(), snapshot, cap)
	if err != nil {
		return report, err
	}
	if err := cluster.Save(ClusterPath(dir), sidecar); err != nil {
		return report, fmt.Errorf("server: persist cluster sidecar: %w", err)
	}
	return sidecar.Report(), nil
}

func ensureClusterSidecar(dir string, generation uint64) (cluster.BuildReport, error) {
	if sidecar, err := cluster.Load(ClusterPath(dir), generation); err == nil {
		return sidecar.Report(), nil
	}
	return buildClusterSidecar(dir)
}

func detectSnapshotClusters(ctx context.Context, snapshot *graphSnapshot, cap int) (cluster.Sidecar, error) {
	start := time.Now()
	sidecar := cluster.Sidecar{Version: cluster.SidecarVersion, Generation: snapshot.graph.Generation(), Cap: cap, Clusters: []cluster.Cluster{}}
	type eligibleEdge struct {
		source, target diskgraph.Key
		weight         float64
	}
	nodeKeys := make(map[diskgraph.Key]struct{})
	var eligible []eligibleEdge
	for i, source := range snapshot.graph.Keys() {
		if i&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return cluster.Sidecar{}, err
			}
		}
		for _, edge := range snapshot.graph.Edges(source) {
			if !isClusterEdge(edge) {
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
		sidecar.Clusters = []cluster.Cluster{}
		sidecar.BuildMillis = time.Since(start).Milliseconds()
		return sidecar, nil
	}
	nodes := make([]cluster.Node, 0, len(nodeKeys))
	for key := range nodeKeys {
		meta := snapshot.nodes[key]
		locations := meta.Locations
		if len(locations) == 0 {
			locations = snapshot.locationsForKey(key)
		}
		node := cluster.Node{ID: keyID(key), Name: meta.Symbol}
		if len(locations) > 0 {
			node.Repo, node.Path, node.Line = locations[0].Repo, locations[0].Path, locations[0].Line
		}
		nodes = append(nodes, node)
	}
	edges := make([]cluster.Edge, 0, len(eligible))
	for _, edge := range eligible {
		edges = append(edges, cluster.Edge{Source: keyID(edge.source), Target: keyID(edge.target), Weight: edge.weight})
	}
	sidecar.Status = cluster.StatusAvailable
	sidecar.Clusters = cluster.Detect(nodes, edges)
	sidecar.BuildMillis = time.Since(start).Milliseconds()
	return sidecar, nil
}
