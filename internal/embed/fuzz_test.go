package embed

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzParseStoreHeaderNeverPanics drives the MDXE v3 parser over arbitrary
// bytes. LoadStore hands the vector block out as a slice aliasing the mapping,
// so an offset that survives validation becomes a segfault inside a query
// rather than an error at load.
func FuzzParseStoreHeaderNeverPanics(f *testing.F) {
	s := &Store{dim: 2, chunks: make([]Chunk, 0), keys: make([]ChunkKey, 0)}
	dir := f.TempDir()
	p := filepath.Join(dir, "seed.store")
	if err := s.Save(p); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b)
			if len(b) > 8 {
				f.Add(b[:len(b)/2])
			}
		}
	}
	f.Add([]byte("MDXE"))
	f.Add(make([]byte, storeHeaderSize))

	f.Fuzz(func(t *testing.T, b []byte) {
		h, err := parseStoreHeader(b, uint64(len(b)))
		if err != nil {
			return
		}
		got, err := storeFrom(h, b, nil)
		if err != nil {
			return
		}
		// A successfully parsed store must be safe to exercise fully, whichever
		// quantization it decoded to: vecAt reads the f32 block and scoreAgainst
		// additionally exercises the int8 block plus its per-vector scale.
		q := make([]float32, got.dim)
		for i := 0; i < got.Len(); i++ {
			if got.quant == quantF32 {
				v := got.vecAt(i)
				for j := range v {
					_ = v[j]
				}
			}
			_ = got.scoreAgainst(q, i)
		}
	})
}

func TestLoadStoreRejectsTruncatedFileAtEveryPrefix(t *testing.T) {
	s := storeFromVectors(t, 4, [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}})
	dir := t.TempDir()
	full := filepath.Join(dir, "full.store")
	if err := s.Save(full); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < len(b); n++ {
		p := filepath.Join(dir, "trunc.store")
		if err := os.WriteFile(p, b[:n], 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := LoadStore(p)
		if err == nil {
			got.Close()
			t.Fatalf("prefix of %d bytes loaded successfully; want an error", n)
		}
	}
}
