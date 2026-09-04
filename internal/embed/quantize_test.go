package embed

import (
	"math"
	"path/filepath"
	"testing"
)

func TestQuantizeRoundTripsWithinTolerance(t *testing.T) {
	v := normalize([]float32{0.5, -0.25, 0.8, -0.1, 0.3, 0.02})
	q, scale := Quantize(v)
	if len(q) != len(v) {
		t.Fatalf("quantized length %d, want %d", len(q), len(v))
	}
	for i := range v {
		got := float32(q[i]) * scale
		if math.Abs(float64(got-v[i])) > float64(scale) {
			t.Fatalf("component %d: %v vs %v (scale %v)", i, got, v[i], scale)
		}
	}
}

func TestQuantizeHandlesZeroVector(t *testing.T) {
	q, scale := Quantize([]float32{0, 0, 0})
	if scale != 0 {
		t.Fatalf("zero vector scale = %v, want 0", scale)
	}
	for i, x := range q {
		if x != 0 {
			t.Fatalf("component %d = %d, want 0", i, x)
		}
	}
}

func TestInt8StoreCosineTracksFloat32(t *testing.T) {
	vecs := [][]float32{
		normalize([]float32{1, 0.2, 0.1, 0}),
		normalize([]float32{0, 1, 0.3, 0.1}),
		normalize([]float32{0.4, 0.4, 1, 0.2}),
	}
	f := storeFromVectors(t, 4, vecs)
	dir := t.TempDir()
	pf := filepath.Join(dir, "f32.store")
	pq := filepath.Join(dir, "int8.store")
	if err := f.Save(pf); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveQuantized(pq); err != nil {
		t.Fatal(err)
	}
	qs, err := LoadStore(pq)
	if err != nil {
		t.Fatal(err)
	}
	defer qs.Close()
	if qs.quant != quantInt8 {
		t.Fatalf("quant = %d, want %d", qs.quant, quantInt8)
	}

	q := normalize([]float32{1, 0.2, 0.1, 0})
	for i := range vecs {
		want := dot(q, f.vecAt(i))
		got := qs.scoreAgainst(q, i)
		// int8 with a per-vector scale keeps ~2 decimal places on unit vectors.
		if math.Abs(float64(got-want)) > 0.02 {
			t.Fatalf("chunk %d: int8 cosine %v vs f32 %v", i, got, want)
		}
	}
}
