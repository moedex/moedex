package graphserve

import (
	"context"

	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
)

// The budget bounds retained traversal work, not the logical graph. Reaching it
// is reported explicitly; discovered totals are then lower bounds.
const maxGraphTraversalEdges = 10_000

type traversalBudget struct {
	remaining int
	truncated bool
}

func newTraversalBudget() *traversalBudget {
	return &traversalBudget{remaining: maxGraphTraversalEdges}
}
func (b *traversalBudget) take() bool {
	if b.truncated {
		return false
	}
	if b.remaining == 0 {
		b.truncated = true
		return false
	}
	b.remaining--
	return true
}

func (s *graphSnapshot) outgoing(ctx context.Context, key diskgraph.Key, allow func(diskgraph.EdgeType) bool, minConfidence graph.ConfidenceTier, budget *traversalBudget, visit func(graphRelation)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if budget.truncated {
		return nil
	}
	err := s.graph.EachOutgoingContext(ctx, key, func(edge diskgraph.Edge) bool {
		return edge.Confidence >= minConfidence && allow(edge.Type)
	}, func(edge diskgraph.Edge) bool {
		if ctx.Err() != nil || !budget.take() {
			return false
		}
		visit(relationFrom(key, edge))
		return true
	})
	return err
}

func (r *GraphQueryResult) setBudget(b *traversalBudget) {
	r.TotalIsExact = !b.truncated
	r.Truncated = b.truncated
	if b.truncated {
		r.ExpansionLimit = maxGraphTraversalEdges
	}
}
