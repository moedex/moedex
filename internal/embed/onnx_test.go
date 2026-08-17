//go:build onnx

package embed

import (
	"context"
	"math"
	"os"
	"testing"
)

// TestONNXEmbedderSemantics exercises the real in-process model. It needs the
// ONNX Runtime shared library; set ONNXRUNTIME_LIB_PATH (or rely on a system
// install) or the test skips cleanly — it never fails CI for a missing lib.
func TestONNXEmbedderSemantics(t *testing.T) {
	lib := os.Getenv("ONNXRUNTIME_LIB_PATH")
	e, err := NewONNXEmbedder(lib)
	if err != nil {
		t.Skipf("onnx runtime unavailable (set ONNXRUNTIME_LIB_PATH): %v", err)
	}
	defer e.Close()

	if e.Dim() != 768 {
		t.Errorf("Dim() = %d, want 768", e.Dim())
	}

	vecs, err := e.Embed(context.Background(), []string{
		"a small domestic cat",
		"a kitten playing",
		"a commercial jet airplane",
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("got %d vectors, want 3", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != 768 {
			t.Fatalf("vec %d has dim %d, want 768", i, len(v))
		}
		// meanPool L2-normalizes, so each vector should be unit length.
		var norm float64
		for _, x := range v {
			norm += float64(x) * float64(x)
		}
		if math.Abs(math.Sqrt(norm)-1.0) > 1e-3 {
			t.Errorf("vec %d not unit length: |v|=%.4f", i, math.Sqrt(norm))
		}
	}

	// Semantic sanity: cat is closer to kitten than to airplane.
	catKitten := dot(vecs[0], vecs[1])
	catPlane := dot(vecs[0], vecs[2])
	if catKitten <= catPlane {
		t.Errorf("expected cos(cat,kitten)=%.3f > cos(cat,airplane)=%.3f", catKitten, catPlane)
	}
	t.Logf("cos(cat,kitten)=%.3f  cos(cat,airplane)=%.3f", catKitten, catPlane)
}

func TestONNXEmbedderSimilarFunctions(t *testing.T) {
	lib := os.Getenv("ONNXRUNTIME_LIB_PATH")
	e, err := NewONNXEmbedder(lib)
	if err != nil {
		t.Skipf("onnx runtime unavailable (set ONNXRUNTIME_LIB_PATH): %v", err)
	}
	defer e.Close()

	vecs, err := e.Embed(context.Background(), []string{
		`func SumPositiveValues(values []int) int {
			total := 0
			for _, value := range values {
				if value > 0 { total += value }
			}
			return total
		}`,
		`func AddPositiveNumbers(numbers []int) int {
			sum := 0
			for _, number := range numbers {
				if number > 0 { sum += number }
			}
			return sum
		}`,
		`func ParseAuthorizationHeader(request *Request) (string, error) {
			raw := request.Header.Get("Authorization")
			parts := SplitN(raw, " ", 2)
			if len(parts) != 2 { return "", ErrMalformedHeader }
			return parts[1], nil
		}`,
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	similar := dot(vecs[0], vecs[1])
	unrelatedA := dot(vecs[0], vecs[2])
	unrelatedB := dot(vecs[1], vecs[2])
	t.Logf("function cosine: similar=%.4f unrelatedA=%.4f unrelatedB=%.4f", similar, unrelatedA, unrelatedB)
	if similar <= unrelatedA || similar <= unrelatedB {
		t.Fatalf("similar functions must outrank unrelated: %.4f vs %.4f/%.4f", similar, unrelatedA, unrelatedB)
	}
}
