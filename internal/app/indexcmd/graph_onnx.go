//go:build onnx

package indexcmd

import (
	"fmt"
	"math"
	"os"
	"strconv"

	"moedex/internal/embed"
	graphbuild "moedex/internal/graph/build"
	"moedex/internal/graph/diskgraph"
)

func buildGraph(dir string) (path string, stats graphbuild.GraphRefreshStats, err error) {
	topK, err := graphIntEnv("MOEDEX_GRAPH_SIMILAR_TOP_K", graphbuild.DefaultSimilarTopK)
	if err != nil {
		return "", stats, err
	}
	threshold, err := graphFloatEnv("MOEDEX_GRAPH_SIMILAR_THRESHOLD", graphbuild.DefaultSimilarThreshold)
	if err != nil {
		return "", stats, err
	}
	exactLimit, err := graphIntEnv("MOEDEX_GRAPH_SIMILAR_EXACT_LIMIT", embed.DefaultSimilarExactLimit)
	if err != nil {
		return "", stats, err
	}
	maxCandidates, err := graphIntEnv("MOEDEX_GRAPH_SIMILAR_MAX_CANDIDATES", embed.DefaultSimilarMaxCandidates)
	if err != nil {
		return "", stats, err
	}
	if topK == 0 {
		return graphbuild.RefreshGraph(dir)
	}
	intraThreads, err := graphIntEnv("MOEDEX_ONNX_INTRA_OP_THREADS", 0)
	if err != nil {
		return "", stats, err
	}
	interThreads, err := graphIntEnv("MOEDEX_ONNX_INTER_OP_THREADS", 0)
	if err != nil {
		return "", stats, err
	}
	lspConcurrency, err := graphIntEnv("MOEDEX_GRAPH_LSP_CONCURRENCY", graphbuild.DefaultLSPConcurrency())
	if err != nil {
		return "", stats, err
	}
	lspRate, err := graphNonNegativeFloatEnv("MOEDEX_GRAPH_LSP_REQUESTS_PER_SECOND", graphbuild.DefaultLSPRequestsPerSecond)
	if err != nil {
		return "", stats, err
	}

	embedder, err := embed.NewONNXEmbedderWithOptions(os.Getenv("ONNXRUNTIME_LIB_PATH"), embed.ONNXOptions{
		IntraOpThreads: intraThreads,
		InterOpThreads: interThreads,
	})
	if err != nil {
		return "", stats, fmt.Errorf("initialize graph ONNX embedder: %w", err)
	}
	defer func() {
		if closeErr := embedder.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close graph ONNX embedder: %w", closeErr)
		}
	}()
	generation, previousGeneration, err := nextSemanticGraphGeneration(dir)
	if err != nil {
		return "", stats, err
	}
	var lspStats graphbuild.LSPGraphStats
	path, report, err := graphbuild.BuildGraphWithOptions(dir, graphbuild.GraphBuildOptions{
		SimilarTopK:          topK,
		SimilarThreshold:     threshold,
		SimilarExactLimit:    exactLimit,
		SimilarMaxCandidates: maxCandidates,
		Embedder:             embedder,
		Generation:           generation,
		LSPConcurrency:       lspConcurrency,
		LSPRequestsPerSecond: lspRate,
		LSPStats:             &lspStats,
	})
	if err == nil {
		stats.FullRebuild = true
		stats.Reason = "semantic similarity enabled"
		stats.PreviousGeneration = previousGeneration
		stats.Generation = generation
		stats.NamesEligible = report.Schedule.Names
		stats.NamesRecomputed = report.Schedule.Names
		stats.EdgesRecomputed = int(report.Edges)
		stats.Schedule = report.Schedule
		stats.Cluster = report.Cluster
		stats.Counts = report.Counts
		stats.LSP = lspStats
	}
	return path, stats, err
}

func nextSemanticGraphGeneration(dir string) (generation, previous uint64, err error) {
	generation = diskgraph.FirstGeneration
	g, openErr := diskgraph.Open(graphbuild.GraphPath(dir))
	if openErr != nil {
		return generation, 0, nil
	}
	previous = g.Generation()
	if closeErr := g.Close(); closeErr != nil {
		return 0, 0, fmt.Errorf("close prior graph generation: %w", closeErr)
	}
	if previous == ^uint64(0) {
		return 0, 0, fmt.Errorf("prior graph generation overflow")
	}
	return previous + 1, previous, nil
}

func graphIntEnv(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer, got %q", key, raw)
	}
	return value, nil
}

func graphFloatEnv(key string, fallback float64) (float64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || value < -1 || value > 1 {
		return 0, fmt.Errorf("%s must be a cosine score in [-1,1], got %q", key, raw)
	}
	return value, nil
}

func graphNonNegativeFloatEnv(key string, fallback float64) (float64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, fmt.Errorf("%s must be a finite non-negative number, got %q", key, raw)
	}
	return value, nil
}
