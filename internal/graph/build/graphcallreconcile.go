package graphbuild

import (
	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
)

type lspConfirmedCall struct {
	source   diskgraph.Key
	target   diskgraph.Key
	name     string
	evidence graph.Evidence
	repoRoot string
}

type lspPatternReconciliation struct {
	calls      []lspConfirmedCall
	pruneSites map[patternCallSiteKey]struct{}
	stats      LSPGraphStats
}

func (r lspPatternReconciliation) prunes(edge graphKeyEdge) bool {
	if edge.Edge.Type != diskgraph.EdgeCalls || edge.Edge.Confidence != graph.Pattern {
		return false
	}
	_, ok := r.pruneSites[patternCallSiteKey{
		sourceBlob: edge.Edge.Evidence.BlobSHA,
		evidence:   edge.Edge.Evidence.ByteOffset,
		name:       edge.Edge.Name,
	}]
	return ok
}

func addReconciledLSPCalls(builder *diskgraph.Builder, seen map[persistedGraphEdge]struct{}, reconciliation *lspPatternReconciliation, generation uint64) error {
	if reconciliation == nil {
		return nil
	}
	for _, call := range reconciliation.calls {
		if err := builder.AddNode(call.target); err != nil {
			return err
		}
		edge := diskgraph.Edge{
			Type:         diskgraph.EdgeCalls,
			TargetBlob:   call.target.BlobSHA,
			TargetOffset: call.target.SymbolOffset,
			Confidence:   graph.Proven,
			Evidence:     call.evidence,
			Name:         call.name,
			Generation:   generation,
		}
		record := persistedGraphEdge{
			sourceBlob:     call.source.BlobSHA,
			sourceOffset:   call.source.SymbolOffset,
			name:           edge.Name,
			typeID:         edge.Type,
			targetBlob:     edge.TargetBlob,
			targetOffset:   edge.TargetOffset,
			confidence:     uint64(edge.Confidence),
			evidenceBlob:   edge.Evidence.BlobSHA,
			evidence:       edge.Evidence.ByteOffset,
			evidenceLength: edge.Evidence.ByteLength,
		}
		if _, duplicate := seen[record]; duplicate {
			continue
		}
		seen[record] = struct{}{}
		if _, err := builder.AddOrUpgradeEdge(call.source, edge); err != nil {
			return err
		}
		reconciliation.stats.CallEdges++
	}
	return nil
}
