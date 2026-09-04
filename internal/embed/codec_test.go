package embed

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMDXEv3RoundTrip(t *testing.T) {
	s := storeFromVectors(t, 4, [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}})
	p := filepath.Join(t.TempDir(), "e.store")
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadStore(p)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	defer got.Close()
	if got.Dim() != s.Dim() || got.Len() != s.Len() {
		t.Fatalf("dim/len mismatch: (%d,%d) vs (%d,%d)", got.Dim(), got.Len(), s.Dim(), s.Len())
	}
	for i := 0; i < s.Len(); i++ {
		a, b := s.vecAt(i), got.vecAt(i)
		for j := range a {
			if a[j] != b[j] {
				t.Fatalf("vector %d component %d: %v != %v", i, j, a[j], b[j])
			}
		}
	}
}

func TestMDXEv3VectorBlockIs64ByteAligned(t *testing.T) {
	s := storeFromVectors(t, 3, [][]float32{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}})
	p := filepath.Join(t.TempDir(), "e.store")
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	h, err := parseStoreHeader(b, uint64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if h.vecOff%64 != 0 {
		t.Fatalf("vector block at offset %d is not 64-byte aligned", h.vecOff)
	}
}

func TestLoadStoreRejectsV1AndV2AsLegacy(t *testing.T) {
	for _, version := range []uint32{1, 2} {
		p := filepath.Join(t.TempDir(), "old.store")
		buf := make([]byte, storeHeaderSize)
		copy(buf, "MDXE")
		binary.LittleEndian.PutUint32(buf[4:], version)
		if err := os.WriteFile(p, buf, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadStore(p); !errors.Is(err, ErrLegacyFormat) {
			t.Fatalf("version %d: want ErrLegacyFormat, got %v", version, err)
		}
	}
}
