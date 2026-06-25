package diskstore

import (
	"bytes"
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
	// [contentHeaderSize, baselineDirOff). Re-derive baselineDirOff from its header.
	if string(baselineBytes[0:8]) != contentMagic {
		t.Fatal("baseline magic wrong")
	}
	// dirOff is at bytes [24:32] of the header.
	baselineDirOff := int64(baselineBytes[24]) | int64(baselineBytes[25])<<8 | int64(baselineBytes[26])<<16 | int64(baselineBytes[27])<<24
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
