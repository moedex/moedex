package tokenindex

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTKI2RoundTripPreservesEveryStatistic(t *testing.T) {
	ti := Build(threeBlobIndex(t))
	p := filepath.Join(t.TempDir(), "tokens.tki")
	if err := Save(ti, p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer got.Close()

	if got.NumDocs() != ti.NumDocs() {
		t.Fatalf("NumDocs %d != %d", got.NumDocs(), ti.NumDocs())
	}
	if got.AvgDocLen() != ti.AvgDocLen() {
		t.Fatalf("AvgDocLen %v != %v", got.AvgDocLen(), ti.AvgDocLen())
	}
	for blob := uint64(0); blob < 4; blob++ {
		if got.DocLen(blob) != ti.DocLen(blob) {
			t.Fatalf("DocLen(%d) %d != %d", blob, got.DocLen(blob), ti.DocLen(blob))
		}
	}
	for _, term := range []string{"alpha", "beta", "gamma", "absent"} {
		if got.DocFreq(term) != ti.DocFreq(term) {
			t.Fatalf("DocFreq(%q) %d != %d", term, got.DocFreq(term), ti.DocFreq(term))
		}
		for blob := uint64(0); blob < 4; blob++ {
			if got.TermFreq(term, blob) != ti.TermFreq(term, blob) {
				t.Fatalf("TermFreq(%q,%d) mismatch", term, blob)
			}
		}
	}
}

func TestLoadRejectsTKI1AsLegacy(t *testing.T) {
	p := filepath.Join(t.TempDir(), "old.tki")
	// A TKI1 magic plus enough bytes to clear the header-size check.
	buf := make([]byte, tkiHeaderSize)
	copy(buf, "TKI1")
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); !errors.Is(err, ErrLegacyFormat) {
		t.Fatalf("want ErrLegacyFormat, got %v", err)
	}
}

func TestSaveLoadEmptyIndex(t *testing.T) {
	ti := Build(nil)
	p := filepath.Join(t.TempDir(), "empty.tki")
	if err := Save(ti, p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer got.Close()
	if got.NumDocs() != 0 || got.DocFreq("anything") != 0 {
		t.Fatal("empty index did not round-trip as empty")
	}
}

func TestLoadedIndexIsBackedByAMapping(t *testing.T) {
	ti := Build(threeBlobIndex(t))
	p := filepath.Join(t.TempDir(), "tokens.tki")
	if err := Save(ti, p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.mm == nil {
		t.Fatal("Load returned a heap-backed index; want an mmap-backed one")
	}
	if got.DocFreq("beta") != 2 {
		t.Fatalf("mapped DocFreq(beta) = %d, want 2", got.DocFreq("beta"))
	}
	if err := got.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := got.Close(); err != nil {
		t.Fatalf("Close must be idempotent, got %v", err)
	}
}

func TestBuiltIndexCloseIsNoOp(t *testing.T) {
	ti := Build(threeBlobIndex(t))
	if ti.mm != nil {
		t.Fatal("Build must not produce a mapped index")
	}
	if err := ti.Close(); err != nil {
		t.Fatalf("Close on a built index: %v", err)
	}
	if ti.DocFreq("beta") != 2 {
		t.Fatal("a built index must stay usable after Close")
	}
}

func TestLoadRejectsTruncatedFileAtEveryPrefix(t *testing.T) {
	ti := Build(threeBlobIndex(t))
	dir := t.TempDir()
	full := filepath.Join(dir, "full.tki")
	if err := Save(ti, full); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	// Every proper prefix must be rejected, never panic and never succeed.
	for n := 0; n < len(b); n++ {
		p := filepath.Join(dir, "trunc.tki")
		if err := os.WriteFile(p, b[:n], 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := Load(p)
		if err == nil {
			got.Close()
			t.Fatalf("prefix of %d bytes loaded successfully; want an error", n)
		}
	}
}

func TestLoadRejectsOutOfBoundsSectionOffset(t *testing.T) {
	ti := Build(threeBlobIndex(t))
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.tki")
	if err := Save(ti, p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// postBlobOff (header offset 88) points past the end of the file.
	binary.LittleEndian.PutUint64(b[88:], uint64(len(b))+4096)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("an out-of-bounds section offset loaded successfully")
	}
}

func TestLoadRejectsNonMonotonicOffsets(t *testing.T) {
	ti := Build(threeBlobIndex(t))
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.tki")
	if err := Save(ti, p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	h, err := parseHeader(b, uint64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	// Make termOff[0] larger than termOff[1].
	binary.LittleEndian.PutUint32(b[h.termOffOff:], 0xFFFF)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("non-monotonic offsets loaded successfully")
	}
}
