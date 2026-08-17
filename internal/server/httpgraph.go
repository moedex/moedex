package server

// httpgraph.go extends the offline graph bridge with phase 10's cross-service
// HTTP edges. It runs beside the name-based sweep in graphbuild.go and writes
// into the same builder, so a shard set gets ONE adjacency file containing both
// kinds of relationship.

import (
	"fmt"

	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/graph/httproute"
)

func addHTTPCallEdges(builder *diskgraph.Builder, corpus *httproute.Corpus, seen map[persistedGraphEdge]struct{}) (httproute.Report, error) {
	result := httproute.Build(corpus)
	for _, edge := range result.Edges {
		callBlob := corpus.Blob(edge.Call)
		handlerBlob := corpus.Blob(edge.Handler)
		if callBlob == nil || handlerBlob == nil {
			return result.Report, fmt.Errorf("server: http edge %q references an unknown blob", edge.Call.Raw)
		}
		source, err := httpNodeOffset(edge.Call.SymbolStart, edge.Call.Start)
		if err != nil {
			return result.Report, err
		}
		target, err := httpNodeOffset(edge.Handler.SymbolStart, edge.Handler.Start)
		if err != nil {
			return result.Report, err
		}
		evidenceStart, err := httpNodeOffset(-1, edge.Call.Start)
		if err != nil {
			return result.Report, err
		}
		evidenceLen := uint64(edge.Call.End - edge.Call.Start)
		if evidenceLen == 0 {
			evidenceLen = 1
		}

		record := persistedGraphEdge{
			sourceBlob:     callBlob.SHA,
			sourceOffset:   source,
			typeID:         diskgraph.EdgeHTTPCalls,
			targetBlob:     handlerBlob.SHA,
			targetOffset:   target,
			confidence:     uint64(edge.Confidence),
			evidenceBlob:   callBlob.SHA,
			evidence:       evidenceStart,
			evidenceLength: evidenceLen,
		}
		if _, duplicate := seen[record]; duplicate {
			continue
		}
		seen[record] = struct{}{}

		if err := builder.Add(callBlob.SHA, source, diskgraph.Edge{
			Type:       diskgraph.EdgeHTTPCalls,
			TargetBlob: handlerBlob.SHA,
			TargetOffset: target,
			Confidence:   edge.Confidence,
			Evidence: graph.Evidence{
				BlobSHA:    callBlob.SHA,
				ByteOffset: evidenceStart,
				ByteLength: evidenceLen,
			},
		}); err != nil {
			return result.Report, err
		}
	}
	return result.Report, nil
}

func httpNodeOffset(symbolStart, evidenceStart int) (uint64, error) {
	off := symbolStart
	if off < 0 {
		off = evidenceStart
	}
	if off < 0 {
		return 0, fmt.Errorf("server: http graph endpoint has a negative byte offset")
	}
	return uint64(off), nil
}
