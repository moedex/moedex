//go:build onnx

package main

import (
	"fmt"
	"math"
	"os"
	"strconv"

	"moedex/internal/embed"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/server"
)

func buildGraph(dir string) (path string, stats server.GraphRefreshStats, err error) {
	topK, err := graphIntEnv("MOEDEX_GRAPH_SIMILAR_TOP_K", server.DefaultSimilarTopK)
	if err != nil {
		return "", stats, err
	}
	threshold, err := graphFloatEnv("MOEDEX_GRAPH_SIMILAR_THRESHOLD", server.DefaultSimilarThreshold)
	if err != nil {
		return "", stats, err
	}
	if topK == 0 {
		return server.RefreshGraph(dir)
	}
	intraThreads, err := graphIntEnv("MOEDEX_ONNX_INTRA_OP_THREADS", 0)
	if err != nil {
		return "", stats, err
	}
	interThreads, err := graphIntEnv("MOEDEX_ONNX_INTER_OP_THREADS", 0)
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
	path, report, err := server.BuildGraphWithOptions(dir, server.GraphBuildOptions{
		SimilarTopK:      topK,
		SimilarThreshold: threshold,
		Embedder:         embedder,
		Generation:       generation,
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
	}
	return path, stats, err
}

func nextSemanticGraphGeneration(dir string) (generation, previous uint64, err error) {
	generation = diskgraph.FirstGeneration
	g, openErr := diskgraph.Open(server.GraphPath(dir))
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
