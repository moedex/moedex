//go:build onnx

package main

import "testing"

func TestGraphSimilarityEnvironment(t *testing.T) {
	t.Setenv("MOEDEX_GRAPH_SIMILAR_TOP_K", "9")
	t.Setenv("MOEDEX_GRAPH_SIMILAR_THRESHOLD", "0.72")
	topK, err := graphIntEnv("MOEDEX_GRAPH_SIMILAR_TOP_K", 5)
	if err != nil || topK != 9 {
		t.Fatalf("top-K = %d, %v; want 9", topK, err)
	}
	threshold, err := graphFloatEnv("MOEDEX_GRAPH_SIMILAR_THRESHOLD", 0.60)
	if err != nil || threshold != 0.72 {
		t.Fatalf("threshold = %g, %v; want 0.72", threshold, err)
	}
}

func TestGraphSimilarityEnvironmentRejectsInvalidValues(t *testing.T) {
	t.Setenv("MOEDEX_GRAPH_SIMILAR_TOP_K", "-1")
	if _, err := graphIntEnv("MOEDEX_GRAPH_SIMILAR_TOP_K", 5); err == nil {
		t.Fatal("negative top-K accepted")
	}
	t.Setenv("MOEDEX_GRAPH_SIMILAR_THRESHOLD", "NaN")
	if _, err := graphFloatEnv("MOEDEX_GRAPH_SIMILAR_THRESHOLD", 0.60); err == nil {
		t.Fatal("NaN threshold accepted")
	}
}
