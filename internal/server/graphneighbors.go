package server

// graphneighbors.go implements phase 14: graph-fused search. It turns the ranked
// context blocks search_context is about to return into their graph
// neighborhoods, so an agent gets "what calls this / what does this call / who
// publishes this event" in the SAME response as the code.
//
// The join is positional: a context block knows a file path and a 1-based line
// range, the node catalog knows where every graph node lives, so anchorsFor maps
// one to the other through graphSnapshot.byPath. From those anchors each
// relationship lane runs its own directed traversal over the mmap'd adjacency
// sidecar — the same data trace_calls and impact_analysis walk, never a second
// copy on the heap.
//
// Cost discipline: reverse traversal has to scan the adjacency records (there is
// no persisted reverse index), so every incoming lane of every block is served by
// ONE scan per hop, not one per block.

import (
	"context"
	"path"
	"strings"

	"moedex/internal/contextwin"
	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/mcp"
)

// neighborBucket identifies one relationship lane of the annotation. Each lane
// traverses in ONE fixed direction over ONE edge-type set, which is what makes
// depth > 1 mean "callers of callers" / "what this transitively depends on"
// rather than an undirected blob around the block.
type neighborBucket int

const (
	bucketCallers neighborBucket = iota
	bucketCallees
	bucketConsumers
	bucketPublishers
	bucketDependsOn
	bucketSimilarTo
	numNeighborBuckets
)

// bucketSpec is one lane's traversal direction(s) and edge-type filter.
type bucketSpec struct {
	outgoing bool
	incoming bool
	allow    func(diskgraph.EdgeType) bool
}

// neighborSpecs defines the six lanes. See mcp.BlockNeighbors for the semantics
// this table implements and for what is deliberately left out.
var neighborSpecs = [numNeighborBuckets]bucketSpec{
	bucketCallers:    {incoming: true, allow: isCallEdge},
	bucketCallees:    {outgoing: true, allow: isCallEdge},
	bucketConsumers:  {incoming: true, allow: isConsumesEdge},
	bucketPublishers: {incoming: true, allow: isPublishesEdge},
	bucketDependsOn:  {outgoing: true, allow: isDependencyEdge},
	bucketSimilarTo:  {incoming: true, outgoing: true, allow: isSimilarEdge},
}

func isCallEdge(t diskgraph.EdgeType) bool      { return t == diskgraph.EdgeCalls }
func isConsumesEdge(t diskgraph.EdgeType) bool  { return t == diskgraph.EdgeConsumes }
func isPublishesEdge(t diskgraph.EdgeType) bool { return t == diskgraph.EdgePublishes }
func isSimilarEdge(t diskgraph.EdgeType) bool   { return t == diskgraph.EdgeSimilarTo }

// isDependencyEdge selects the outgoing edges that mean "this symbol needs that
// definition". CALLS is excluded because it has its own lane, and
// SIBLING_DEFINITION is excluded because a same-named definition elsewhere is a
// peer, not a dependency. A manifest-derived DEPENDS_ON edge type (plan phase 8)
// belongs in this lane when it lands.
func isDependencyEdge(t diskgraph.EdgeType) bool {
	switch t {
	case diskgraph.EdgeImports, diskgraph.EdgeUsesType, diskgraph.EdgeReferences,
		diskgraph.EdgeCandidate, diskgraph.EdgeDependsOn, diskgraph.EdgePublishes, diskgraph.EdgeConsumes,
		diskgraph.EdgeInjects, diskgraph.EdgeQueries, diskgraph.EdgeRenders:
		return true
	}
	return false
}

// isReverseEdge is the union of every incoming lane's filter — the single allow
// predicate the shared per-hop reverse scan runs with.
func isReverseEdge(t diskgraph.EdgeType) bool {
	return isCallEdge(t) || isConsumesEdge(t) || isPublishesEdge(t) || isSimilarEdge(t)
}

// visitKey identifies one node's membership of one lane for one block. Traversal
// state is per-block so two blocks sharing a neighbor each report it, while one
// block never reports the same neighbor twice in the same lane.
type visitKey struct {
	bucket neighborBucket
	block  int
	key    diskgraph.Key
}

// Neighbors implements mcp.GraphAnnotator: it returns the graph neighborhood of
// each block, index-aligned with blocks. A depth of 0 (annotation off), an empty
// block list, or a closed toolset yield nil — "leave the response un-annotated" —
// rather than an error, because a missing graph must never fail a search.
func (g *GraphToolset) Neighbors(ctx context.Context, blocks []contextwin.ContextBlock, depth int) ([]mcp.BlockNeighbors, error) {
	return g.NeighborsWithConfidence(ctx, blocks, depth, graph.DefaultMinConfidence)
}

// NeighborsWithConfidence applies minConfidence before traversal, so a weak
// edge can neither be returned nor act as a bridge to a later node.
func (g *GraphToolset) NeighborsWithConfidence(ctx context.Context, blocks []contextwin.ContextBlock, depth int, minConfidence graph.ConfidenceTier) ([]mcp.BlockNeighbors, error) {
	result, err := g.NeighborsWithSnapshot(ctx, blocks, depth, minConfidence)
	return result.Neighbors, err
}

// NeighborsWithSnapshot captures annotations and graph identity from one
// acquisition so a concurrent reload cannot tear the response stamp.
func (g *GraphToolset) NeighborsWithSnapshot(ctx context.Context, blocks []contextwin.ContextBlock, depth int, minConfidence graph.ConfidenceTier) (mcp.GraphAnnotationResult, error) {
	if depth <= 0 || len(blocks) == 0 {
		return mcp.GraphAnnotationResult{}, nil
	}
	if depth > maxGraphDepth {
		depth = maxGraphDepth
	}
	snap := g.acquire()
	if snap == nil {
		return mcp.GraphAnnotationResult{}, nil
	}
	defer snap.wg.Done()
	neighbors, err := snap.neighbors(ctx, blocks, depth, minConfidence)
	if err != nil {
		return mcp.GraphAnnotationResult{}, err
	}
	identity := snap.identity()
	for _, block := range blocks {
		identity.BlobSHAs = append(identity.BlobSHAs, block.BlobSHA)
	}
	for _, annotation := range neighbors {
		for _, bucket := range [][]mcp.Neighbor{annotation.Callers, annotation.Callees, annotation.Consumers, annotation.Publishers, annotation.DependsOn, annotation.SimilarTo} {
			for _, neighbor := range bucket {
				if cut := strings.LastIndexByte(neighbor.ID, ':'); cut > 0 {
					identity.BlobSHAs = append(identity.BlobSHAs, neighbor.ID[:cut])
				}
				identity.BlobSHAs = append(identity.BlobSHAs, neighbor.EvidenceBlobSHA)
			}
		}
	}
	return mcp.GraphAnnotationResult{Neighbors: neighbors, Snapshot: identity.Normalize()}, nil
}

func (s *graphSnapshot) neighbors(ctx context.Context, blocks []contextwin.ContextBlock, depth int, minConfidence graph.ConfidenceTier) ([]mcp.BlockNeighbors, error) {
	out := make([]mcp.BlockNeighbors, len(blocks))
	found := make([][numNeighborBuckets][]mcp.Neighbor, len(blocks))

	// frontier[lane][node] = the blocks currently reaching that node in that lane,
	// each mapped to the anchor symbol the path started from.
	var frontier [numNeighborBuckets]map[diskgraph.Key]map[int]string
	for b := range frontier {
		frontier[b] = make(map[diskgraph.Key]map[int]string)
	}
	visited := make(map[visitKey]bool)

	anchored := false
	for i, block := range blocks {
		out[i] = mcp.NewBlockNeighbors()
		anchors := s.anchorsFor(block)
		if len(anchors) == 0 {
			continue
		}
		anchored = true
		out[i].Anchors = s.anchorNames(anchors)
		for _, anchor := range anchors {
			name := s.nodes[anchor].Symbol
			for b := range frontier {
				// An anchor is part of the block, never its own neighbor.
				visited[visitKey{bucket: neighborBucket(b), block: i, key: anchor}] = true
				if frontier[b][anchor] == nil {
					frontier[b][anchor] = make(map[int]string)
				}
				frontier[b][anchor][i] = name
			}
		}
	}
	if !anchored {
		return out, nil
	}

	for hop := 1; hop <= depth; hop++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pending := make(map[visitKey]mcp.Neighbor)

		for b, spec := range neighborSpecs {
			if !spec.outgoing {
				continue
			}
			for key, reaching := range frontier[b] {
				for _, edge := range s.graph.Edges(key) {
					if edge.Confidence < minConfidence || !spec.allow(edge.Type) {
						continue
					}
					rel := relationFrom(key, edge)
					s.offer(pending, visited, neighborBucket(b), rel, rel.Target, mcp.DirectionOut, hop, reaching)
				}
			}
		}

		// Every incoming lane shares one reverse scan of the adjacency records.
		targets := make(map[diskgraph.Key]bool)
		for b, spec := range neighborSpecs {
			if !spec.incoming {
				continue
			}
			for key := range frontier[b] {
				targets[key] = true
			}
		}
		if len(targets) > 0 {
			relations, err := s.incoming(ctx, targets, isReverseEdge, minConfidence)
			if err != nil {
				return nil, err
			}
			for _, rel := range relations {
				for b, spec := range neighborSpecs {
					if !spec.incoming || !spec.allow(rel.Type) {
						continue
					}
					reaching, ok := frontier[b][rel.Target]
					if !ok {
						continue
					}
					s.offer(pending, visited, neighborBucket(b), rel, rel.Source, mcp.DirectionIn, hop, reaching)
				}
			}
		}

		if len(pending) == 0 {
			break
		}
		var next [numNeighborBuckets]map[diskgraph.Key]map[int]string
		for b := range next {
			next[b] = make(map[diskgraph.Key]map[int]string)
		}
		for vk, neighbor := range pending {
			visited[vk] = true
			found[vk.block][vk.bucket] = append(found[vk.block][vk.bucket], neighbor)
			if next[vk.bucket][vk.key] == nil {
				next[vk.bucket][vk.key] = make(map[int]string)
			}
			next[vk.bucket][vk.key][vk.block] = neighbor.Anchor
		}
		frontier = next
	}

	for i := range out {
		for bucket := range found[i] {
			for j := range found[i][bucket] {
				setNeighborProximity(&found[i][bucket][j], blocks[i])
			}
		}
		out[i].BucketTotals["callers"] = len(found[i][bucketCallers])
		out[i].BucketTotals["callees"] = len(found[i][bucketCallees])
		out[i].BucketTotals["consumers"] = len(found[i][bucketConsumers])
		out[i].BucketTotals["publishers"] = len(found[i][bucketPublishers])
		out[i].BucketTotals["depends_on"] = len(found[i][bucketDependsOn])
		out[i].BucketTotals["similar_to"] = len(found[i][bucketSimilarTo])
		out[i].Callers = finishBucket(found[i][bucketCallers], &out[i].Truncated)
		out[i].Callees = finishBucket(found[i][bucketCallees], &out[i].Truncated)
		out[i].Consumers = finishBucket(found[i][bucketConsumers], &out[i].Truncated)
		out[i].Publishers = finishBucket(found[i][bucketPublishers], &out[i].Truncated)
		out[i].DependsOn = finishBucket(found[i][bucketDependsOn], &out[i].Truncated)
		out[i].SimilarTo = finishBucket(found[i][bucketSimilarTo], &out[i].Truncated)
	}
	return out, nil
}

func setNeighborProximity(neighbor *mcp.Neighbor, block contextwin.ContextBlock) {
	neighbor.ProximitySameRepo = neighbor.Repo != "" && neighbor.Repo == block.Repo
	neighbor.ProximityDepth = sharedDirectoryPrefixDepth(block.RelPath, neighbor.RelPath)
}

func sharedDirectoryPrefixDepth(left, right string) int {
	leftDir, rightDir := path.Dir(strings.TrimPrefix(left, "./")), path.Dir(strings.TrimPrefix(right, "./"))
	if leftDir == "." || rightDir == "." {
		return 0
	}
	lp, rp := strings.Split(leftDir, "/"), strings.Split(rightDir, "/")
	depth := 0
	for depth < len(lp) && depth < len(rp) && lp[depth] == rp[depth] {
		depth++
	}
	return depth
}

// offer records one discovered neighbor for every block whose path reached it,
// keeping the strongest relationship when several edges lead to the same node.
func (s *graphSnapshot) offer(
	pending map[visitKey]mcp.Neighbor,
	visited map[visitKey]bool,
	bucket neighborBucket,
	rel graphRelation,
	node diskgraph.Key,
	direction string,
	hop int,
	reaching map[int]string,
) {
	for block, anchor := range reaching {
		vk := visitKey{bucket: bucket, block: block, key: node}
		if visited[vk] {
			continue
		}
		candidate := s.neighborOf(node, rel, direction, hop, anchor)
		if existing, ok := pending[vk]; ok && !strongerNeighbor(candidate, existing) {
			continue
		}
		pending[vk] = candidate
	}
}

// strongerNeighbor is a total order over two ways of reaching the same node, so
// the retained relationship does not depend on map iteration order.
func strongerNeighbor(candidate, existing mcp.Neighbor) bool {
	cw, ew := neighborWeight(candidate), neighborWeight(existing)
	if cw != ew {
		return cw > ew
	}
	if candidate.Edge != existing.Edge {
		return candidate.Edge < existing.Edge
	}
	if candidate.Direction != existing.Direction {
		return candidate.Direction < existing.Direction
	}
	return candidate.Anchor < existing.Anchor
}

// neighborWeight mirrors diskgraph.Edge.Weight: semantic edges rank by their
// cosine metric, everything else by its confidence tier's score.
func neighborWeight(n mcp.Neighbor) float64 {
	if n.Edge == diskgraph.EdgeSimilarTo.String() {
		return n.Similarity
	}
	return n.Confidence.Score
}

// neighborOf renders one graph node as an MCP neighbor of an anchor, reached
// across rel. Location is the node's first known site; a node with no resolvable
// location still reports its id, symbol, and relationship.
func (s *graphSnapshot) neighborOf(key diskgraph.Key, rel graphRelation, direction string, hops int, anchor string) mcp.Neighbor {
	meta := s.nodes[key]
	neighbor := mcp.Neighbor{
		ID:              keyID(key),
		Symbol:          meta.Symbol,
		Kind:            meta.Kind,
		Edge:            rel.Type.String(),
		Direction:       direction,
		Hops:            hops,
		Anchor:          anchor,
		Confidence:      graph.ConfidenceOf(rel.Confidence),
		Similarity:      rel.Similarity,
		EvidenceBlobSHA: rel.Evidence.BlobSHA,
	}
	locations := meta.Locations
	if locations == nil {
		locations = s.locationsForKey(key)
	}
	if len(locations) > 0 {
		neighbor.Repo = locations[0].Repo
		neighbor.RelPath = locations[0].Path
		neighbor.Line = locations[0].Line
	}
	return neighbor
}

// finishBucket sorts and caps one lane, OR-ing any trimming into the block's
// Truncated flag so a capped bucket is never mistaken for an exhausted one.
func finishBucket(list []mcp.Neighbor, truncated *bool) []mcp.Neighbor {
	if len(list) == 0 {
		return []mcp.Neighbor{}
	}
	mcp.SortNeighbors(list)
	kept, trimmed := mcp.TrimNeighbors(list)
	if trimmed {
		*truncated = true
	}
	return kept
}

// anchorsFor resolves a context block to the graph nodes that live inside it.
// The block's absolute path is tried first, then repo-qualified and bare
// relative spellings; the FIRST spelling that names a known file wins, so a bare
// relative path can never pull in a same-named file from another repo once the
// absolute path has identified the real one.
func (s *graphSnapshot) anchorsFor(block contextwin.ContextBlock) []diskgraph.Key {
	start, end := block.StartLine, block.EndLine
	if end < start {
		start, end = end, start
	}
	for _, spelling := range pathSpellings(GraphLocation{Repo: block.Repo, Path: block.RelPath, AbsPath: block.AbsPath}) {
		nodes := s.byPath[spelling]
		if len(nodes) == 0 {
			continue
		}
		var out []diskgraph.Key
		seen := make(map[diskgraph.Key]bool, len(nodes))
		for _, node := range nodes {
			if node.line < start || node.line > end || seen[node.key] {
				continue
			}
			seen[node.key] = true
			out = append(out, node.key)
		}
		return out
	}
	return nil
}

// anchorNames lists the distinct symbol names of a block's anchors, in the
// anchors' own (line-sorted) order. Nodes with no extracted symbol name — a raw
// evidence occurrence — contribute nothing rather than an empty string.
func (s *graphSnapshot) anchorNames(anchors []diskgraph.Key) []string {
	out := make([]string, 0, len(anchors))
	seen := make(map[string]bool, len(anchors))
	for _, anchor := range anchors {
		name := s.nodes[anchor].Symbol
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}
