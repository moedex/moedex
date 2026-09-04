package embed

import (
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestLoadedStoreHeapIsAFractionOfFileSize is the dense half of the same
// invariant Task 7 pins for the token index (decision D1): a loaded store keeps
// its vectors in the mapping, not on the heap. Before this change the store
// held its vectors 1:1 on the heap, so the ratio was about 1.0.
func TestLoadedStoreHeapIsAFractionOfFileSize(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a multi-thousand-vector fixture")
	}
	const (
		dim   = 128
		count = 8000
	)
	rng := rand.New(rand.NewSource(1))
	vecs := make([][]float32, count)
	for i := range vecs {
		v := make([]float32, dim)
		for j := range v {
			v[j] = rng.Float32()*2 - 1
		}
		vecs[i] = normalize(v)
	}
	s := storeFromVectors(t, dim, vecs)
	p := filepath.Join(t.TempDir(), "e.store")
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	got, err := LoadStore(p)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	// Touch every vector so nothing is deferred past the measurement.
	var sink float32
	for i := 0; i < got.Len(); i++ {
		sink += got.vecAt(i)[0]
	}
	_ = sink

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if grew < 0 {
		grew = 0
	}
	ratio := float64(grew) / float64(fi.Size())
	t.Logf("file %d bytes, heap grew %d bytes, ratio %.3f", fi.Size(), grew, ratio)
	// Chunks and keys stay decoded on the heap by design (48 bytes per chunk
	// against dim*4 = 512 bytes of vector), so the ceiling is above the token
	// index's 0.20.
	if ratio > 0.25 {
		t.Fatalf("loaded store put %.1f%% of its file on the heap; want under 25%%", ratio*100)
	}
}
