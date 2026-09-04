package mmapslice

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUint32sReinterpretsLittleEndian(t *testing.T) {
	b := make([]byte, 12)
	binary.LittleEndian.PutUint32(b[0:], 1)
	binary.LittleEndian.PutUint32(b[4:], 70000)
	binary.LittleEndian.PutUint32(b[8:], 4294967295)
	got, err := Uint32s(b, 3)
	if err != nil {
		t.Fatalf("Uint32s: %v", err)
	}
	want := []uint32{1, 70000, 4294967295}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("elem %d = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestUint32sRejectsShortBuffer(t *testing.T) {
	if _, err := Uint32s(make([]byte, 7), 2); !errors.Is(err, ErrShort) {
		t.Fatalf("want ErrShort, got %v", err)
	}
}

func TestUint32sRejectsMisalignedBuffer(t *testing.T) {
	b := make([]byte, 16)
	// b[1:] is guaranteed misaligned for a 4-byte type.
	if _, err := Uint32s(b[1:], 2); !errors.Is(err, ErrAlignment) {
		t.Fatalf("want ErrAlignment, got %v", err)
	}
}

func TestUint32sRejectsNegativeAndOverflowingCounts(t *testing.T) {
	if _, err := Uint32s(make([]byte, 8), -1); err == nil {
		t.Fatal("want error for negative n")
	}
	if _, err := Uint32s(make([]byte, 8), (1<<62)+1); !errors.Is(err, ErrOverflow) {
		t.Fatalf("want ErrOverflow, got %v", err)
	}
}

func TestZeroLengthIsEmptyNotError(t *testing.T) {
	got, err := Uint32s(nil, 0)
	if err != nil || got != nil {
		t.Fatalf("Uint32s(nil,0) = %v, %v; want nil, nil", got, err)
	}
}

func TestFloat32sReinterprets(t *testing.T) {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b[0:], 0x3F800000) // 1.0
	binary.LittleEndian.PutUint32(b[4:], 0xC0000000) // -2.0
	got, err := Float32s(b, 2)
	if err != nil {
		t.Fatalf("Float32s: %v", err)
	}
	if got[0] != 1.0 || got[1] != -2.0 {
		t.Fatalf("got %v, want [1 -2]", got)
	}
}

func TestInt8sReinterprets(t *testing.T) {
	got, err := Int8s([]byte{0x01, 0xFF, 0x7F, 0x80}, 4)
	if err != nil {
		t.Fatalf("Int8s: %v", err)
	}
	want := []int8{1, -1, 127, -128}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("elem %d = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestAliasingIsZeroCopy(t *testing.T) {
	b := make([]byte, 4)
	got, err := Uint32s(b, 1)
	if err != nil {
		t.Fatalf("Uint32s: %v", err)
	}
	binary.LittleEndian.PutUint32(b, 0xDEADBEEF)
	if got[0] != 0xDEADBEEF {
		t.Fatalf("slice did not alias the backing array: got %#x", got[0])
	}
}

func TestOpenMapsFileAndCloseUnmaps(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.bin")
	payload := []byte("hello mmap")
	if err := os.WriteFile(p, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(m.Bytes()) != string(payload) {
		t.Fatalf("Bytes() = %q, want %q", m.Bytes(), payload)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("second Close must be a no-op, got %v", err)
	}
}

func TestOpenRejectsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.bin")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); err == nil {
		t.Fatal("want error mapping an empty file")
	}
}
