//go:build !onnx

package graphbuild

import (
	"context"
	"fmt"

	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
	"moedex/internal/symbol"
)

func addSimilarToEdges(_ context.Context, _ *diskgraph.Builder, _ *symbol.Corpus, _ []*index.Index, _ map[persistedGraphEdge]struct{}, opts GraphBuildOptions) error {
	if opts.SimilarTopK > 0 {
		return fmt.Errorf("server: SIMILAR_TO graph edges require a build with -tags onnx")
	}
	return nil
}
