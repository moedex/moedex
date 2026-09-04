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

// TestInt8UnalignedScaleSection guards against a regression where scaleOff
// was placed immediately after the int8 vec block with no alignment padding
// of its own: vecOff is 64-byte aligned, so scaleOff inherits alignment only
// when count*dim happens to be a multiple of 4. dim=2, count=3 gives
// count*dim=6, which is NOT a multiple of 4 and previously produced a store
// LoadStore could not open.
func TestInt8UnalignedScaleSection(t *testing.T) {
	s := storeFromVectors(t, 2, [][]float32{{1, 0}, {0, 1}, {1, 1}})
	p := filepath.Join(t.TempDir(), "q.store")
	if err := s.SaveQuantized(p); err != nil {
		t.Fatalf("SaveQuantized: %v", err)
	}
	got, err := LoadStore(p)
	if err != nil {
		t.Fatalf("LoadStore of a valid quantized store failed: %v", err)
	}
	defer got.Close()
}

// TestInt8UnalignedScaleSectionScoresTrackFloat32 exercises the same
// unaligned-scale-section shapes end to end: SaveQuantized -> LoadStore ->
// scoreAgainst must still track the f32 cosine, not just load without error.
func TestInt8UnalignedScaleSectionScoresTrackFloat32(t *testing.T) {
	cases := []struct {
		name string
		dim  int
		vecs [][]float32
	}{
		{"dim2count3", 2, [][]float32{{1, 0}, {0, 1}, {1, 1}}}, // count*dim=6
		{"dim3count1", 3, [][]float32{{1, 0.5, 0.25}}},         // count*dim=3
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vecs := make([][]float32, len(tc.vecs))
			for i, v := range tc.vecs {
				vecs[i] = normalize(v)
			}
			f := storeFromVectors(t, tc.dim, vecs)
			pq := filepath.Join(t.TempDir(), "int8.store")
			if err := f.SaveQuantized(pq); err != nil {
				t.Fatal(err)
			}
			qs, err := LoadStore(pq)
			if err != nil {
				t.Fatalf("LoadStore: %v", err)
			}
			defer qs.Close()

			q := vecs[0]
			for i := range vecs {
				want := dot(q, f.vecAt(i))
				got := qs.scoreAgainst(q, i)
				if math.Abs(float64(got-want)) > 0.02 {
					t.Fatalf("chunk %d: int8 cosine %v vs f32 %v", i, got, want)
				}
			}
		})
	}
}
