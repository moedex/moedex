//go:build onnx

package main

import (
	"fmt"
	"math"
	"os"
	"strconv"

	"moedex/internal/embed"
	"moedex/internal/server"
)

// buildGraphSidecar enables the semantic graph pass in tagged index binaries.
// Environment settings keep the refresh/build/cas-export paths on one shared
// configuration seam; top-K zero explicitly disables semantic edges.
func buildGraphSidecar(dir string) (path string, err error) {
	topK, err := graphIntEnv("MOEDEX_GRAPH_SIMILAR_TOP_K", server.DefaultSimilarTopK)
	if err != nil {
		return "", err
	}
	threshold, err := graphFloatEnv("MOEDEX_GRAPH_SIMILAR_THRESHOLD", server.DefaultSimilarThreshold)
	if err != nil {
		return "", err
	}
	if topK == 0 {
		path, _, err := server.BuildGraphSidecar(dir)
		return path, err
	}

	embedder, err := embed.NewONNXEmbedder(os.Getenv("ONNXRUNTIME_LIB_PATH"))
	if err != nil {
		return "", fmt.Errorf("initialize graph ONNX embedder: %w", err)
	}
	defer func() {
		if closeErr := embedder.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close graph ONNX embedder: %w", closeErr)
		}
	}()
	path, _, err = server.BuildGraphSidecarWithOptions(dir, server.GraphBuildOptions{
		SimilarTopK:      topK,
		SimilarThreshold: threshold,
		Embedder:         embedder,
	})
	return path, err
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
