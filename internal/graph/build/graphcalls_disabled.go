//go:build !lsp

package graphbuild

import (
	"context"

	"moedex/internal/index"
	"moedex/internal/symbol"
)

func lspGraphAvailable() bool { return false }

// The systematic language-server sweep is wholly absent from default builds.
func collectLSPPatternReconciliation(_ context.Context, _ *symbol.Corpus, _ []*index.Index, _ []patternCallGroup, _ map[string]bool, _ GraphBuildOptions) (*lspPatternReconciliation, error) {
	return &lspPatternReconciliation{}, nil
}
