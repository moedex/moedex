//go:build onnx

package indexcmd

import (
	"testing"

	graphbuild "moedex/internal/graph/build"
	"moedex/internal/graph/diskgraph"
)

func TestGraphSimilarityEnvironment(t *testing.T) {
	t.Setenv("MOEDEX_GRAPH_SIMILAR_TOP_K", "9")
	t.Setenv("MOEDEX_GRAPH_SIMILAR_THRESHOLD", "0.72")
	t.Setenv("MOEDEX_GRAPH_SIMILAR_EXACT_LIMIT", "2048")
	t.Setenv("MOEDEX_GRAPH_SIMILAR_MAX_CANDIDATES", "512")
	topK, err := graphIntEnv("MOEDEX_GRAPH_SIMILAR_TOP_K", 5)
	if err != nil || topK != 9 {
		t.Fatalf("top-K = %d, %v; want 9", topK, err)
	}
	threshold, err := graphFloatEnv("MOEDEX_GRAPH_SIMILAR_THRESHOLD", 0.60)
	if err != nil || threshold != 0.72 {
		t.Fatalf("threshold = %g, %v; want 0.72", threshold, err)
	}
	exactLimit, err := graphIntEnv("MOEDEX_GRAPH_SIMILAR_EXACT_LIMIT", 4096)
	if err != nil || exactLimit != 2048 {
		t.Fatalf("exact limit = %d, %v; want 2048", exactLimit, err)
	}
	maxCandidates, err := graphIntEnv("MOEDEX_GRAPH_SIMILAR_MAX_CANDIDATES", 2048)
	if err != nil || maxCandidates != 512 {
		t.Fatalf("max candidates = %d, %v; want 512", maxCandidates, err)
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
	t.Setenv("MOEDEX_GRAPH_LSP_REQUESTS_PER_SECOND", "-1")
	if _, err := graphNonNegativeFloatEnv("MOEDEX_GRAPH_LSP_REQUESTS_PER_SECOND", graphbuild.DefaultLSPRequestsPerSecond); err == nil {
		t.Fatal("negative LSP request rate accepted")
	}
}

func TestNextSemanticGraphGenerationAdvancesExistingGraph(t *testing.T) {
	dir := t.TempDir()
	b := diskgraph.NewBuilder()
	b.SetGeneration(6)
	if err := b.Save(graphbuild.GraphPath(dir)); err != nil {
		t.Fatal(err)
	}
	generation, previous, err := nextSemanticGraphGeneration(dir)
	if err != nil {
		t.Fatal(err)
	}
	if generation != 7 || previous != 6 {
		t.Fatalf("generation = %d, previous = %d; want 7, 6", generation, previous)
	}
}
