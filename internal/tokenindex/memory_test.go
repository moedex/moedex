package tokenindex

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"moedex/internal/index"
)

// TestLoadedIndexHeapIsAFractionOfFileSize is the memory invariant this whole
// format exists to buy: a loaded index must keep its data in the mapping, not
// on the Go heap.
//
// The assertion is a RATIO rather than an absolute ceiling (decision D1): a
// ratio needs no magic number, survives corpus growth, and states the actual
// invariant. Before this change the ratio was about 12.6x; the ceiling here is
// 0.20, which a regression back to heap-resident arrays cannot satisfy.
func TestLoadedIndexHeapIsAFractionOfFileSize(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a multi-thousand-blob fixture")
	}
	ix := index.New()
	for i := 0; i < 4000; i++ {
		content := fmt.Sprintf("package p%d\nfunc HandleRefundRequest%d(ctx Context) error {\n\treturn processPaymentGateway%d(ctx)\n}\n", i, i, i)
		ix.AddFile("r", fmt.Sprintf("f%d.go", i), fmt.Sprintf("/r/f%d.go", i), fmt.Sprintf("sha%d", i), []byte(content))
	}
	p := filepath.Join(t.TempDir(), "tokens.tki")
	if err := Save(Build(ix), p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	ti, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	defer ti.Close()
	// Touch every accessor so nothing is lazily deferred past the measurement.
	if ti.NumDocs() == 0 {
		t.Fatal("loaded an empty index")
	}
	for i := 0; i < len(ti.termOff)-1; i++ {
		_ = ti.Postings(string(ti.termBytes(i))).Len()
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if grew < 0 {
		grew = 0
	}
	ratio := float64(grew) / float64(fi.Size())
	t.Logf("file %d bytes, heap grew %d bytes, ratio %.3f", fi.Size(), grew, ratio)
	if ratio > 0.20 {
		t.Fatalf("loaded index put %.1f%% of its file on the heap; want under 20%% (the data must live in the mapping)", ratio*100)
	}
}
