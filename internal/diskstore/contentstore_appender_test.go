package diskstore

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestContentStoreAppenderCarriesForwardAndAppendsOnlyNetNew proves the delta
// primitive: opening an existing MOECONT1 store for append carries every prior
// blob forward at its EXACT byte offset, a re-Put of a present hash is a dedup
// no-op (zero bytes), and only net-new content extends the store. Every prior
// blob's content reads back byte-identical, and the prior content section is
// reproduced byte-for-byte (the file is a true superset).
func TestContentStoreAppenderCarriesForwardAndAppendsOnlyNetNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ContentStoreName)

	// Baseline store with two blobs.
	a := []byte("alpha content one")
	b := []byte("bravo content two, a bit longer")
	cw := NewContentStoreWriter()
	refA := cw.PutContent("sha-a", a)
	refB := cw.PutContent("sha-b", b)
	if err := cw.Write(path); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
	baselineBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Open for append; re-Put existing + Put one net-new blob.
	app, err := OpenContentStoreAppender(path)
	if err != nil {
		t.Fatalf("OpenContentStoreAppender: %v", err)
	}
	// Existing refs must be re-derived identically (content-relative offsets).
	if r, ok := app.Ref("sha-a"); !ok || r != refA {
		t.Errorf("re-opened ref(sha-a) = %+v ok=%v, want %+v", r, ok, refA)
	}
	if r, ok := app.Ref("sha-b"); !ok || r != refB {
		t.Errorf("re-opened ref(sha-b) = %+v ok=%v, want %+v", r, ok, refB)
	}
	// Re-Put of a present hash: dedup no-op, zero appended bytes.
	app.PutContent("sha-a", a)
	app.PutContent("sha-b", b)
	if app.BytesAppended() != 0 {
		t.Errorf("BytesAppended after re-Putting only present hashes = %d, want 0", app.BytesAppended())
	}
	if app.BlobsAppended() != 0 {
		t.Errorf("BlobsAppended = %d, want 0", app.BlobsAppended())
	}
	// Net-new blob.
	c := []byte("charlie net-new content three")
	app.PutContent("sha-c", c)
	if app.BytesAppended() != int64(len(c)) {
		t.Errorf("BytesAppended = %d, want %d (only sha-c)", app.BytesAppended(), len(c))
	}
	if app.BlobsAppended() != 1 {
		t.Errorf("BlobsAppended = %d, want 1", app.BlobsAppended())
	}
	if app.BytesStored() != int64(len(a)+len(b)+len(c)) {
		t.Errorf("BytesStored = %d, want %d", app.BytesStored(), len(a)+len(b)+len(c))
	}

	// Write IN PLACE (outPath == source): the rename swaps after the source is read.
	if err := app.Write(path); err != nil {
		t.Fatalf("append write: %v", err)
	}

	// The new file's CONTENT SECTION must begin with the baseline's content section
	// byte-for-byte (prior offsets preserved). The baseline content section is
	// [contentHeaderSize, baselineDirOff). Decode the FULL uint64 dirOff (bytes
	// [24:32]) — a 32-bit-truncating decode would silently pass for small stores but
	// MISS offset preservation for a store whose dirOff exceeds 4 GiB. (The synthesized
	// large-dirOff regression below proves the decode width actually matters.)
	if string(baselineBytes[0:8]) != contentMagic {
		t.Fatal("baseline magic wrong")
	}
	baselineDirOff := int64(binary.LittleEndian.Uint64(baselineBytes[24:32]))
	priorContent := baselineBytes[contentHeaderSize:baselineDirOff]
	newBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(newBytes[contentHeaderSize:contentHeaderSize+int64(len(priorContent))], priorContent) {
		t.Error("appended store's content section did not reproduce the prior content byte-for-byte")
	}

	// Every blob reads back byte-identical through the read path. (Keys here are
	// arbitrary strings, not git-blob hashes, so we open without the verify hasher;
	// the content-true GitBlobSHA1 verify is exercised end-to-end by the blobstore
	// parity test against a real CAS.)
	cs, err := OpenContentStore(path)
	if err != nil {
		t.Fatalf("open appended store: %v", err)
	}
	defer cs.Close()
	for sha, want := range map[string][]byte{"sha-a": a, "sha-b": b, "sha-c": c} {
		got, ok := cs.Content(sha)
		if !ok {
			t.Errorf("Content(%s) missing after append", sha)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("Content(%s) = %q, want %q", sha, got, want)
		}
	}
	if cs.Len() != 3 {
		t.Errorf("appended store Len = %d, want 3", cs.Len())
	}
}

// TestContentStoreAppenderLargeOffsetsNoTruncation is the 32-bit-truncation
// REGRESSION: it synthesizes a MOECONT1 store header whose dirOff AND whose single
// blob's absolute content offset both EXCEED 4 GiB (low-32-bits != true value), over
// a SPARSE file (no 4 GiB actually written), and asserts OpenContentStoreAppender
// reconstructs the exact content-relative offset using the FULL uint64. A decoder
// that truncated either field to 32 bits would either read the directory from the
// wrong (4 GiB-too-low) offset and fail, or reconstruct a wrong Ref.Offset — so this
// test fails against any 32-bit-truncating decode of dirOff or the per-entry offset.
func TestContentStoreAppenderLargeOffsetsNoTruncation(t *testing.T) {
	fourGiB := int64(1) << 32
	dirOff := fourGiB + (1 << 30)  // 5 GiB: > 4 GiB, low 32 bits = 0x40000000
	blobAbsOff := fourGiB + 100    // > 4 GiB; low 32 bits (100) != true value
	blobLen := int64(64)
	const sha = "huge-offset-sha"
	// Sanity: the test premise is that 32-bit truncation would CHANGE these values.
	if uint64(uint32(uint64(dirOff))) == uint64(dirOff) {
		t.Fatal("test premise: dirOff must not fit in 32 bits")
	}
	if uint64(uint32(uint64(blobAbsOff))) == uint64(blobAbsOff) {
		t.Fatal("test premise: blobAbsOff must not fit in 32 bits")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "huge.dat")

	// Build the directory section (one record): shaLen+sha, absOff(u64), len(u64).
	var dirSec []byte
	dirSec = appendU32LenBytes(dirSec, []byte(sha))
	dirSec = binary.LittleEndian.AppendUint64(dirSec, uint64(blobAbsOff))
	dirSec = binary.LittleEndian.AppendUint64(dirSec, uint64(blobLen))

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Header.
	hdr := make([]byte, contentHeaderSize)
	copy(hdr[0:8], contentMagic)
	binary.LittleEndian.PutUint32(hdr[8:12], contentStoreVersion)
	binary.LittleEndian.PutUint64(hdr[16:24], 1) // numBlobs
	binary.LittleEndian.PutUint64(hdr[24:32], uint64(dirOff))
	if _, err := f.WriteAt(hdr, 0); err != nil {
		t.Fatal(err)
	}
	// Directory at dirOff (sparse hole between header and dirOff — no 5 GiB written).
	if _, err := f.WriteAt(dirSec, dirOff); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	app, err := OpenContentStoreAppender(path)
	if err != nil {
		t.Fatalf("OpenContentStoreAppender (large offsets): %v", err)
	}
	ref, ok := app.Ref(sha)
	if !ok {
		t.Fatal("blob with >4 GiB offset missing after open (likely a truncated dirOff read the directory from the wrong place)")
	}
	// Content-relative offset == absolute - header. With the full uint64 this is
	// blobAbsOff-32; a 32-bit-truncating decode would yield a different value.
	wantRel := blobAbsOff - contentHeaderSize
	if ref.Offset != wantRel {
		t.Errorf("Ref(%s).Offset = %d, want %d (full-uint64 content-relative offset; a 32-bit truncation would differ)", sha, ref.Offset, wantRel)
	}
	if ref.Len != blobLen {
		t.Errorf("Ref(%s).Len = %d, want %d", sha, ref.Len, blobLen)
	}
	// BytesStored is the existing content footprint (== dirOff - header), which must
	// also be the full-uint64 value.
	if got := app.BytesStored(); got != dirOff-contentHeaderSize {
		t.Errorf("BytesStored = %d, want %d (full-uint64)", got, dirOff-contentHeaderSize)
	}
}
