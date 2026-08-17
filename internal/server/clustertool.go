package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"moedex/internal/graph/cluster"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/mcp"
)

// ClusterTool returns the list_clusters handler backed by this hot-swappable
// graph generation. The first call per generation computes and caches the
// communities; reloads are detected by snapshot identity and recomputed.
func (g *GraphToolset) ClusterTool() mcp.ToolHandler {
	return &listClustersTool{owner: g}
}

type listClustersTool struct {
	owner *GraphToolset

	mu       sync.Mutex
	snapshot *graphSnapshot
	clusters []cluster.Cluster
}

func (t *listClustersTool) Name() string { return "list_clusters" }

func (t *listClustersTool) Descriptor() map[string]interface{} {
	return map[string]interface{}{
		"name":        t.Name(),
		"description": "List modularity-based service communities from the code edge graph. Labels use the dominant repository in each community; isolated graph nodes are returned as singleton clusters.",
		"inputSchema": map[string]interface{}{
			"type":                 "object",
			"properties":           map[string]interface{}{},
			"additionalProperties": false,
		},
	}
}

func (t *listClustersTool) Call(ctx context.Context, raw json.RawMessage) (map[string]interface{}, error) {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("{}")) {
		return mcp.TextResult("invalid arguments: list_clusters accepts no arguments", true), nil
	}
	snapshot := t.owner.acquire()
	if snapshot == nil {
		return mcp.TextResult("graph tools are closed", true), nil
	}
	defer snapshot.wg.Done()

	communities, err := t.forSnapshot(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(communities)
	if err != nil {
		return nil, fmt.Errorf("server: encode clusters: %w", err)
	}
	// The text fallback is intentionally the requested JSON array shape. The
	// structured channel carries the same typed array for MCP-aware clients.
	return mcp.StructuredResult(string(encoded), communities, false), nil
}

func (t *listClustersTool) forSnapshot(ctx context.Context, snapshot *graphSnapshot) ([]cluster.Cluster, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.snapshot == snapshot {
		return cloneClusterResults(t.clusters), nil
	}
	communities, err := detectSnapshotClusters(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	t.snapshot = snapshot
	t.clusters = communities
	return cloneClusterResults(communities), nil
}

func detectSnapshotClusters(ctx context.Context, snapshot *graphSnapshot) ([]cluster.Cluster, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	nodesByID := make(map[string]cluster.Node, snapshot.graph.NumNodes())
	edges := make([]cluster.Edge, 0, snapshot.graph.NumEdges())
	addNode := func(key diskgraph.Key) string {
		id := keyID(key)
		if _, exists := nodesByID[id]; exists {
			return id
		}
		meta := snapshot.nodes[key]
		locations := meta.Locations
		if len(locations) == 0 {
			locations = snapshot.locationsForKey(key)
		}
		node := cluster.Node{ID: id, Name: meta.Symbol}
		if len(locations) > 0 {
			// buildCatalog sorts locations by repo/path/absolute path/line, making
			// this canonical attribution stable for content shared across repos.
			node.Repo = locations[0].Repo
			node.Path = locations[0].Path
			node.Line = locations[0].Line
		}
		nodesByID[id] = node
		return id
	}

	for i, key := range snapshot.graph.Keys() {
		if i&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		sourceID := addNode(key)
		for _, edge := range snapshot.graph.Edges(key) {
			target := diskgraph.Key{BlobSHA: edge.TargetBlob, SymbolOffset: edge.TargetOffset}
			edges = append(edges, cluster.Edge{
				Source: sourceID,
				Target: addNode(target),
				Weight: edge.Weight(),
			})
		}
	}
	nodes := make([]cluster.Node, 0, len(nodesByID))
	for _, node := range nodesByID {
		nodes = append(nodes, node)
	}
	return cluster.Detect(nodes, edges), nil
}

func cloneClusterResults(src []cluster.Cluster) []cluster.Cluster {
	if src == nil {
		return nil
	}
	out := make([]cluster.Cluster, len(src))
	copy(out, src)
	for i := range out {
		out[i].Members = append([]cluster.Node(nil), src[i].Members...)
	}
	return out
}
