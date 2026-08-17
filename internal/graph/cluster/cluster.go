// Package cluster groups graph nodes into tightly connected communities.
//
// Detect applies the local-moving phase of the Louvain algorithm to a weighted,
// undirected view of the input edges. The graph stores directed code
// relationships, but service affinity is symmetric: a call from A to B is
// evidence that A and B belong together regardless of traversal direction.
package cluster

import (
	"math"
	"sort"
)

// Node is one graph member. ID is the stable graph identity. The remaining
// fields are optional source metadata carried through to list_clusters output.
type Node struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Repo      string `json:"repo,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Path      string `json:"path,omitempty"`
	Line      int    `json:"line,omitempty"`
}

// Edge is a weighted relationship between two node IDs. Detect treats it as
// undirected. Parallel edges are additive; self-loops and non-positive or
// non-finite weights do not influence community assignment.
type Edge struct {
	Source string
	Target string
	Weight float64
}

// Cluster is one detected community. ClusterID is assigned after communities
// are deterministically ordered, so identical input produces identical output.
type Cluster struct {
	ClusterID   int    `json:"cluster_id"`
	Label       string `json:"label"`
	MemberCount int    `json:"member_count"`
	Members     []Node `json:"members"`
}

// Detect groups nodes by greedily moving each node to the neighboring community
// with the largest positive modularity gain (the Louvain local-moving phase).
// Explicit nodes with no usable edges remain singleton clusters. Edge endpoints
// omitted from nodes are retained with ID-only metadata rather than discarded.
func Detect(nodes []Node, edges []Edge) []Cluster {
	byID := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		if node.ID == "" {
			continue
		}
		if _, exists := byID[node.ID]; !exists {
			byID[node.ID] = node
		}
	}
	for _, edge := range edges {
		if edge.Source != "" {
			if _, exists := byID[edge.Source]; !exists {
				byID[edge.Source] = Node{ID: edge.Source}
			}
		}
		if edge.Target != "" {
			if _, exists := byID[edge.Target]; !exists {
				byID[edge.Target] = Node{ID: edge.Target}
			}
		}
	}
	if len(byID) == 0 {
		return nil
	}

	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	index := make(map[string]int, len(ids))
	ordered := make([]Node, len(ids))
	for i, id := range ids {
		index[id] = i
		ordered[i] = byID[id]
	}

	adjacency := make([]map[int]float64, len(ids))
	degree := make([]float64, len(ids))
	var totalDegree float64
	for _, edge := range edges {
		left, leftOK := index[edge.Source]
		right, rightOK := index[edge.Target]
		if !leftOK || !rightOK || left == right || edge.Weight <= 0 || math.IsNaN(edge.Weight) || math.IsInf(edge.Weight, 0) {
			continue
		}
		if adjacency[left] == nil {
			adjacency[left] = make(map[int]float64)
		}
		if adjacency[right] == nil {
			adjacency[right] = make(map[int]float64)
		}
		adjacency[left][right] += edge.Weight
		adjacency[right][left] += edge.Weight
		degree[left] += edge.Weight
		degree[right] += edge.Weight
		totalDegree += 2 * edge.Weight
	}

	community := make([]int, len(ids))
	communityDegree := append([]float64(nil), degree...)
	for i := range community {
		community[i] = i
	}
	if totalDegree > 0 {
		moveByModularity(adjacency, degree, totalDegree, community, communityDegree)
	}

	grouped := make(map[int][]Node)
	for i, communityID := range community {
		grouped[communityID] = append(grouped[communityID], ordered[i])
	}
	communities := make([][]Node, 0, len(grouped))
	for _, members := range grouped {
		sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
		communities = append(communities, members)
	}
	sort.Slice(communities, func(i, j int) bool {
		return communities[i][0].ID < communities[j][0].ID
	})

	out := make([]Cluster, len(communities))
	for i, members := range communities {
		out[i] = Cluster{
			ClusterID:   i + 1,
			Label:       dominantLabel(members),
			MemberCount: len(members),
			Members:     members,
		}
	}
	return out
}

// moveByModularity performs deterministic Louvain node moves until a complete
// pass makes no improvement. The gain expression drops constants common to all
// candidate communities: k_i,in - (sum_tot*k_i/m2).
func moveByModularity(adjacency []map[int]float64, degree []float64, totalDegree float64, community []int, communityDegree []float64) {
	const epsilon = 1e-12
	for {
		moved := false
		for node := range community {
			if degree[node] == 0 {
				continue
			}
			current := community[node]
			communityDegree[current] -= degree[node]

			neighborWeight := make(map[int]float64, len(adjacency[node]))
			for neighbor, weight := range adjacency[node] {
				neighborWeight[community[neighbor]] += weight
			}
			candidateIDs := make([]int, 0, len(neighborWeight))
			for candidate := range neighborWeight {
				candidateIDs = append(candidateIDs, candidate)
			}
			sort.Ints(candidateIDs)

			best := current
			bestGain := 0.0
			for _, candidate := range candidateIDs {
				gain := neighborWeight[candidate] - communityDegree[candidate]*degree[node]/totalDegree
				if gain > bestGain+epsilon || (gain > epsilon && math.Abs(gain-bestGain) <= epsilon && candidate < best) {
					best = candidate
					bestGain = gain
				}
			}
			community[node] = best
			communityDegree[best] += degree[node]
			if best != current {
				moved = true
			}
		}
		if !moved {
			return
		}
	}
}

func dominantLabel(nodes []Node) string {
	counts := make(map[string]int)
	for _, node := range nodes {
		label := node.Namespace
		if label == "" {
			label = node.Repo
		}
		if label != "" {
			counts[label]++
		}
	}
	best := ""
	bestCount := 0
	for label, count := range counts {
		if count > bestCount || (count == bestCount && (best == "" || label < best)) {
			best, bestCount = label, count
		}
	}
	if best != "" {
		return best
	}
	return nodes[0].ID
}
