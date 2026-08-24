package mcp

// neighbors.go is the graph-fusion half of search_context: the contract by which
// a ranked context block is annotated with the graph neighborhood of the symbols
// it contains, so an agent gets "what calls this / what does this call / who
// publishes this event" in the SAME response as the code — no second tool call.
//
// The mcp package owns only the contract and the rendering. Resolving a block to
// graph nodes and walking the adjacency sidecar lives in internal/server (which
// owns the mmap'd graph), wired in through WithGraphAnnotator.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"moedex/internal/contextwin"
	"moedex/internal/graph"
)

const (
	// DefaultGraphDepth is the hop radius search_context annotates with when the
	// caller omits graph_depth. One hop is the direct neighborhood.
	DefaultGraphDepth = 1
	// MaxGraphDepth caps graph_depth, matching the ceiling the standalone graph
	// tools enforce on their own traversals.
	MaxGraphDepth = 10
	// MaxNeighborsPerBucket bounds how many neighbors one relationship bucket of
	// one block reports. A hub definition can have thousands of dependents; the
	// annotation is a cue for the agent, not a full traversal (that is what
	// trace_calls / impact_analysis are for). A trimmed bucket is never silent:
	// BlockNeighbors.Truncated records it.
	MaxNeighborsPerBucket = 25
	// maxRenderedNeighbors bounds how many neighbors per bucket the TEXT
	// rendering spells out; the full set is always present in structuredContent.
	maxRenderedNeighbors = 5
)

// Neighbor edge directions, relative to the annotated block's anchor symbol.
const (
	// DirectionIn means the edge points AT the anchor (neighbor -> anchor).
	DirectionIn = "in"
	// DirectionOut means the edge points AWAY from the anchor (anchor -> neighbor).
	DirectionOut = "out"
)

// GraphAnnotator resolves the graph neighborhood of the blocks a search_context
// call is about to return. Implementations must be safe for concurrent calls.
//
// The returned slice is index-aligned with blocks — exactly one BlockNeighbors
// per block. Returning nil means "no graph available" (e.g. the sidecar is
// closed) and leaves the response un-annotated rather than failing the search.
type GraphAnnotator interface {
	Neighbors(ctx context.Context, blocks []contextwin.ContextBlock, depth int) ([]BlockNeighbors, error)
}

// ConfidenceGraphAnnotator is the confidence-aware extension implemented by
// the production graph toolset. Keeping GraphAnnotator intact preserves custom
// annotators while allowing search_context to enforce min_confidence before
// traversal whenever the backend supports it.
type ConfidenceGraphAnnotator interface {
	NeighborsWithConfidence(ctx context.Context, blocks []contextwin.ContextBlock, depth int, minConfidence graph.ConfidenceTier) ([]BlockNeighbors, error)
}

// GraphAnnotationResult binds graph annotations to the exact graph snapshot
// acquired to compute them.
type GraphAnnotationResult struct {
	Neighbors []BlockNeighbors
	Snapshot  SnapshotIdentity
}

// SnapshotGraphAnnotator is the freshness-aware production boundary. It avoids
// a second current-snapshot lookup after graph traversal.
type SnapshotGraphAnnotator interface {
	NeighborsWithSnapshot(ctx context.Context, blocks []contextwin.ContextBlock, depth int, minConfidence graph.ConfidenceTier) (GraphAnnotationResult, error)
}

// WithGraphAnnotator fuses the graph layer into search_context: every returned
// block is annotated with its graph neighborhood (see BlockNeighbors) unless the
// caller passes graph_depth=0. Without this option search_context behaves exactly
// as before and the graph_depth argument is accepted but inert.
func WithGraphAnnotator(a GraphAnnotator) Option {
	return func(s *Server) { s.graph = a }
}

// Neighbor is one graph node reached from a context block's anchor symbol,
// carrying both the relationship that reached it (Edge/Direction/Hops) and the
// provenance of that relationship (Confidence, and Similarity for SIMILAR_TO).
//
// ID is the graph node id ("<blob_sha>:<symbol_offset>") — the same identity the
// standalone graph tools return, so a neighbor can be followed up with
// trace_calls / impact_analysis without a re-resolution step.
type Neighbor struct {
	ID         string           `json:"id"`
	Symbol     string           `json:"symbol,omitempty"`
	Kind       string           `json:"kind,omitempty"`
	Repo       string           `json:"repo,omitempty"`
	RelPath    string           `json:"rel_path,omitempty"`
	Line       int              `json:"line,omitempty"`
	Edge       string           `json:"edge"`      // persisted edge type, e.g. "calls", "publishes"
	Direction  string           `json:"direction"` // DirectionIn or DirectionOut, relative to the anchor
	Hops       int              `json:"hops"`      // 1 for a direct neighbor
	Anchor     string           `json:"anchor,omitempty"`
	Confidence graph.Confidence `json:"confidence"`
	Similarity float64          `json:"similarity,omitempty"`

	// Proximity fields are computed relative to the annotated context block and
	// used only for deterministic ranking. They are not part of the MCP payload.
	ProximitySameRepo bool   `json:"-"`
	ProximityDepth    int    `json:"-"`
	EvidenceBlobSHA   string `json:"-"` // metadata-only source consumed by the relationship
}

// BlockNeighbors is the graph neighborhood of one context block, bucketed by the
// relationship each neighbor has TO the block's anchor symbols.
//
// Bucket semantics (each is a traversal in one fixed direction, so depth > 1
// means "callers of callers", "what this transitively depends on", and so on):
//
//   - Callers    — symbols that call an anchor (incoming CALLS).
//   - Callees    — symbols an anchor calls (outgoing CALLS).
//   - Consumers  — symbols that consume an anchor (incoming CONSUMES); an anchor
//     here is the event/message, the neighbors are its handlers.
//   - Publishers — symbols that publish an anchor (incoming PUBLISHES).
//   - DependsOn  — what an anchor points at that is not a call: outgoing IMPORTS,
//     USES_TYPE, REFERENCES, CANDIDATE, PUBLISHES and CONSUMES. The last two are
//     here deliberately: a publisher's outgoing PUBLISHES edge is a dependency on
//     the event it emits, and Edge/Direction keep which is which unambiguous.
//   - SimilarTo  — SIMILAR_TO neighbors in either direction (the persisted edge is
//     directed, the relationship is not).
//
// Scope, stated rather than implied: incoming non-call, non-messaging edges (who
// merely references or imports an anchor) are NOT annotated. They are the
// highest-volume edge class in the graph and would swamp a search response;
// impact_analysis is the tool for that question.
//
// Every bucket is non-nil so an anchor with no edges renders as [] rather than
// null, and a block with no anchors at all is still a present, empty annotation.
type BlockNeighbors struct {
	// Anchors are the distinct symbol names the block resolved to in the graph.
	Anchors    []string   `json:"anchors"`
	Callers    []Neighbor `json:"callers"`
	Callees    []Neighbor `json:"callees"`
	Consumers  []Neighbor `json:"consumers"`
	Publishers []Neighbor `json:"publishers"`
	DependsOn  []Neighbor `json:"depends_on"`
	SimilarTo  []Neighbor `json:"similar_to"`
	// BucketTotals records the number discovered before the per-bucket storage
	// cap. Every one of the six bucket names is always present.
	BucketTotals map[string]int `json:"bucket_totals"`
	// Truncated reports that at least one bucket hit MaxNeighborsPerBucket and
	// was trimmed, so a short list is never mistaken for a small neighborhood.
	Truncated bool `json:"truncated,omitempty"`
}

// NewBlockNeighbors returns an annotation with every bucket allocated empty, the
// shape an anchor with no edges must produce.
func NewBlockNeighbors() BlockNeighbors {
	return BlockNeighbors{
		Anchors:    []string{},
		Callers:    []Neighbor{},
		Callees:    []Neighbor{},
		Consumers:  []Neighbor{},
		Publishers: []Neighbor{},
		DependsOn:  []Neighbor{},
		SimilarTo:  []Neighbor{},
		BucketTotals: map[string]int{
			"callers": 0, "callees": 0, "consumers": 0,
			"publishers": 0, "depends_on": 0, "similar_to": 0,
		},
	}
}

// buckets returns the labelled buckets in stable rendering order.
func (n *BlockNeighbors) buckets() []struct {
	label string
	list  []Neighbor
} {
	return []struct {
		label string
		list  []Neighbor
	}{
		{"callers", n.Callers},
		{"callees", n.Callees},
		{"consumers", n.Consumers},
		{"publishers", n.Publishers},
		{"depends_on", n.DependsOn},
		{"similar_to", n.SimilarTo},
	}
}

// Total counts every neighbor across all buckets.
func (n *BlockNeighbors) Total() int {
	total := 0
	for _, b := range n.buckets() {
		bucketTotal := n.BucketTotals[b.label]
		if bucketTotal < len(b.list) {
			bucketTotal = len(b.list)
		}
		total += bucketTotal
	}
	return total
}

// SortNeighbors orders a bucket deterministically: nearest hop first, then
// strongest provenance, then symbol and node id. Implementations call it so two
// runs over the same graph generation render identically.
func SortNeighbors(list []Neighbor) {
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Hops != b.Hops {
			return a.Hops < b.Hops
		}
		if a.Confidence.Score != b.Confidence.Score {
			return a.Confidence.Score > b.Confidence.Score
		}
		if a.ProximitySameRepo != b.ProximitySameRepo {
			return a.ProximitySameRepo
		}
		if a.ProximityDepth != b.ProximityDepth {
			return a.ProximityDepth > b.ProximityDepth
		}
		if a.Symbol != b.Symbol {
			return a.Symbol < b.Symbol
		}
		return a.ID < b.ID
	})
}

// TrimNeighbors caps a bucket at MaxNeighborsPerBucket and reports whether it
// dropped anything, so the caller can set BlockNeighbors.Truncated.
func TrimNeighbors(list []Neighbor) ([]Neighbor, bool) {
	if len(list) <= MaxNeighborsPerBucket {
		return list, false
	}
	return list[:MaxNeighborsPerBucket], true
}

// renderNeighbors formats one block's annotation as a single "[graph] ..." line
// for the text output format. It returns "" when there is nothing to say, so an
// un-annotated or edgeless block renders exactly as it did before graph fusion.
func renderNeighbors(n *BlockNeighbors) string {
	if n == nil || n.Total() == 0 {
		return ""
	}
	var parts []string
	for _, bucket := range n.buckets() {
		if len(bucket.list) == 0 {
			continue
		}
		shown := bucket.list
		if len(shown) > maxRenderedNeighbors {
			shown = shown[:maxRenderedNeighbors]
		}
		names := make([]string, 0, len(shown))
		for _, nb := range shown {
			names = append(names, renderNeighbor(nb))
		}
		part := bucket.label + ": " + strings.Join(names, ", ")
		total := n.BucketTotals[bucket.label]
		if total < len(bucket.list) {
			total = len(bucket.list)
		}
		if more := total - len(shown); more > 0 {
			part += fmt.Sprintf(", +%d more (of %d total)", more, total)
		}
		parts = append(parts, part)
	}
	line := "[graph] " + strings.Join(parts, "; ")
	if n.Truncated {
		line += " (buckets trimmed)"
	}
	return line
}

// renderNeighbor names one neighbor as "Symbol [Tier] (repo/path:line)", degrading to
// whatever identity is actually known rather than fabricating a location.
func renderNeighbor(n Neighbor) string {
	name := n.Symbol
	if name == "" {
		name = n.ID
	}
	name += " [" + n.Confidence.Tier.String() + "]"
	where := n.RelPath
	if n.Repo != "" && where != "" {
		where = n.Repo + "/" + where
	}
	if where == "" {
		return name
	}
	if n.Line > 0 {
		return fmt.Sprintf("%s (%s:%d)", name, where, n.Line)
	}
	return fmt.Sprintf("%s (%s)", name, where)
}
