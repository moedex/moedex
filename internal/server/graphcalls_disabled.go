//go:build !lsp

package server

import (
	"context"

	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
	"moedex/internal/symbol"
)

// The systematic language-server sweep is wholly absent from default builds.
func addLSPCallEdges(_ context.Context, _ *diskgraph.Builder, _ *symbol.Corpus, _ []*index.Index, _ map[persistedGraphEdge]struct{}, _ map[string]bool, _ GraphBuildOptions) error {
	return nil
}
