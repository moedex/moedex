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

// TestLoadStoreRejectsInt8QuantAsUnsupported guards Finding 1 from the fix
// round: parseStoreHeader accepts quantInt8 (it is a KNOWN format, just one
// this build doesn't decode yet), so the rejection has to live in storeFrom,
// before the vec-decode loop reads count*dim*4 bytes — four times what an
// int8 block actually holds. A header-only quant flip must produce a clean
// load error, never a slice-bounds panic.
func TestLoadStoreRejectsInt8QuantAsUnsupported(t *testing.T) {
	s := storeFromVectors(t, 4, [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}})
	p := filepath.Join(t.TempDir(), "e.store")
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	b[16] = quantInt8 // flip only the quant byte; vec bytes are still float32
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStore(p); err == nil {
		t.Fatal("LoadStore: want an error for an unsupported quantization, got nil")
	}
}

func TestParseStoreHeaderRejectsOversizedDim(t *testing.T) {
	s := storeFromVectors(t, 4, [][]float32{{1, 0, 0, 0}})
	p := filepath.Join(t.TempDir(), "e.store")
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(b[8:], maxStoreDim+1)
	if _, err := parseStoreHeader(b, uint64(len(b))); err == nil {
		t.Fatal("parseStoreHeader: want an error for dim over the ceiling, got nil")
	}
}

func TestParseStoreHeaderRejectsOversizedCount(t *testing.T) {
	s := storeFromVectors(t, 4, [][]float32{{1, 0, 0, 0}})
	p := filepath.Join(t.TempDir(), "e.store")
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(b[12:], maxStoreCount+1)
	if _, err := parseStoreHeader(b, uint64(len(b))); err == nil {
		t.Fatal("parseStoreHeader: want an error for count over the ceiling, got nil")
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
