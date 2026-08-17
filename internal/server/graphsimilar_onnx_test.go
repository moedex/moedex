//go:build onnx

package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/embed"
	graphmodel "moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
)

const (
	similarFunctionA = `package first

func SumPositiveValues(values []int) int {
	total := 0
	for _, value := range values {
		if value > 0 {
			total += value
		}
	}
	return total
}
`
	similarFunctionB = `package second

func AddPositiveNumbers(numbers []int) int {
	sum := 0
	for _, number := range numbers {
		if number > 0 {
			sum += number
		}
	}
	return sum
}
`
	unrelatedFunction = `package third

func ParseAuthorizationHeader(request *Request) (string, error) {
	raw := request.Header.Get("Authorization")
	parts := SplitN(raw, " ", 2)
	if len(parts) != 2 {
		return "", ErrMalformedHeader
	}
	return parts[1], nil
}
`
)

type graphFixtureEmbedder struct {
	texts []string
}

func (*graphFixtureEmbedder) Dim() int { return 2 }

func (e *graphFixtureEmbedder) Embed(_ context.Context, texts []string) ([]embed.Vector, error) {
	e.texts = append(e.texts, texts...)
	out := make([]embed.Vector, len(texts))
	for i, text := range texts {
		switch {
		case strings.Contains(text, "Positive"):
			out[i] = embed.Vector{1, 0}
		default:
			out[i] = embed.Vector{0, 1}
		}
	}
	return out, nil
}

func TestBuildGraphSimilarToEdges(t *testing.T) {
	dir, shas := writeSimilarityFixture(t)
	embedder := &graphFixtureEmbedder{}
	path, _, err := BuildGraphWithOptions(dir, GraphBuildOptions{
		SimilarTopK:      1,
		SimilarThreshold: 0.90,
		Embedder:         embedder,
	})
	if err != nil {
		t.Fatalf("BuildGraphWithOptions: %v", err)
	}
	graph, err := diskgraph.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close()
	if got := strings.Join(embedder.texts, "\n"); len(embedder.texts) != 3 ||
		!strings.Contains(got, "SumPositiveValues") ||
		!strings.Contains(got, "AddPositiveNumbers") ||
		!strings.Contains(got, "ParseAuthorizationHeader") {
		t.Fatalf("embedded definition bodies = %#v, want all three symbols", embedder.texts)
	}

	assertSimilarPair(t, graph, shas[0], "SumPositiveValues", similarFunctionA, shas[1], "AddPositiveNumbers", similarFunctionB)
	assertSimilarPair(t, graph, shas[1], "AddPositiveNumbers", similarFunctionB, shas[0], "SumPositiveValues", similarFunctionA)
	if score := similarScore(t, graph, shas[0], "SumPositiveValues", similarFunctionA); score != 1 {
		t.Fatalf("persisted cosine similarity = %g, want 1", score)
	}
	assertNoSimilarEdges(t, graph, shas[2], "ParseAuthorizationHeader", unrelatedFunction)
}

// TestBuildGraphSimilarToEdgesONNX proves the same contract with the bundled
// code model. It skips cleanly when ONNX Runtime is not installed, just like
// the embed package's model-level test.
func TestBuildGraphSimilarToEdgesONNX(t *testing.T) {
	embedder, err := embed.NewONNXEmbedder(os.Getenv("ONNXRUNTIME_LIB_PATH"))
	if err != nil {
		t.Skipf("onnx runtime unavailable (set ONNXRUNTIME_LIB_PATH): %v", err)
	}
	defer embedder.Close()

	dir, shas := writeSimilarityFixture(t)
	path, _, err := BuildGraphWithOptions(dir, GraphBuildOptions{
		SimilarTopK:      1,
		SimilarThreshold: DefaultSimilarThreshold,
		Embedder:         embedder,
	})
	if err != nil {
		t.Fatalf("BuildGraphWithOptions: %v", err)
	}
	graph, err := diskgraph.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close()

	assertSimilarPair(t, graph, shas[0], "SumPositiveValues", similarFunctionA, shas[1], "AddPositiveNumbers", similarFunctionB)
	assertSimilarPair(t, graph, shas[1], "AddPositiveNumbers", similarFunctionB, shas[0], "SumPositiveValues", similarFunctionA)
	assertNoSimilarEdges(t, graph, shas[2], "ParseAuthorizationHeader", unrelatedFunction)
}

func writeSimilarityFixture(t *testing.T) (string, [3]string) {
	t.Helper()
	dir := t.TempDir()
	contents := [3]string{similarFunctionA, similarFunctionB, unrelatedFunction}
	var shas [3]string
	for i, content := range contents {
		shas[i] = diskstore.GitBlobSHA1([]byte(content))
		ix := index.New()
		ix.AddFile("repo", "fixture.go", "/fixture.go", shas[i], []byte(content))
		if err := diskstore.Save(ix, filepath.Join(dir, "shard-000"+string(rune('0'+i))+".idx")); err != nil {
			t.Fatal(err)
		}
	}
	return dir, shas
}

func assertSimilarPair(t *testing.T, graph *diskgraph.Graph, sourceSHA, sourceName, sourceContent, targetSHA, targetName, targetContent string) {
	t.Helper()
	sourceOffset := uint64(strings.Index(sourceContent, sourceName))
	targetOffset := uint64(strings.Index(targetContent, targetName))
	var similar []diskgraph.Edge
	for _, edge := range graph.Load(sourceSHA, sourceOffset) {
		if edge.Type == diskgraph.EdgeSimilarTo {
			similar = append(similar, edge)
		}
	}
	if len(similar) != 1 {
		t.Fatalf("%s SIMILAR_TO edges = %#v, want one", sourceName, similar)
	}
	if similar[0].TargetBlob != targetSHA || similar[0].TargetOffset != targetOffset {
		t.Fatalf("%s SIMILAR_TO = %#v, want %s at %s:%d", sourceName, similar[0], targetName, targetSHA, targetOffset)
	}
	if similar[0].Confidence != graphmodel.Candidate {
		t.Fatalf("%s SIMILAR_TO confidence = %s, want Candidate", sourceName, similar[0].Confidence)
	}
	if similar[0].Similarity < DefaultSimilarThreshold {
		t.Fatalf("%s SIMILAR_TO cosine similarity = %.4f, want >= %.2f", sourceName, similar[0].Similarity, DefaultSimilarThreshold)
	}
	if got := similar[0].Confidence.Score(); got != graphmodel.Candidate.Score() {
		t.Fatalf("%s confidence score = %.4f, want Candidate %.4f", sourceName, got, graphmodel.Candidate.Score())
	}
	if similar[0].Evidence.BlobSHA != sourceSHA {
		t.Fatalf("%s SIMILAR_TO evidence blob = %q, want %q", sourceName, similar[0].Evidence.BlobSHA, sourceSHA)
	}
	line, ok := similar[0].Evidence.SourceLine([]byte(sourceContent))
	if !ok || !strings.Contains(string(line), sourceName) {
		t.Fatalf("%s SIMILAR_TO evidence line = %q, %v", sourceName, line, ok)
	}
}

func similarScore(t *testing.T, graph *diskgraph.Graph, sha, name, content string) float64 {
	t.Helper()
	offset := uint64(strings.Index(content, name))
	for _, edge := range graph.Load(sha, offset) {
		if edge.Type == diskgraph.EdgeSimilarTo {
			return edge.Similarity
		}
	}
	t.Fatalf("%s has no SIMILAR_TO edge", name)
	return 0
}

func assertNoSimilarEdges(t *testing.T, graph *diskgraph.Graph, sha, name, content string) {
	t.Helper()
	offset := uint64(strings.Index(content, name))
	for _, edge := range graph.Load(sha, offset) {
		if edge.Type == diskgraph.EdgeSimilarTo {
			t.Fatalf("unrelated %s unexpectedly has SIMILAR_TO edge %#v", name, edge)
		}
	}
}
