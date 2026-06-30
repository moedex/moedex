package diskstore

import (
	"bytes"
	"os"
	"testing"
)

// TestContentStoreWriterStreamsContentEagerly is the regression for
// CODE_REVIEW_2026-06-30.md F-044: ContentStoreWriter.PutContent used to append
// every unique blob's content into an in-memory w.content [][]byte that was only
// flushed to disk by Write — so a full export held the whole unique-content set on
// the Go heap, the opposite of the off-heap goal the package doc promises. PutContent
// must instead stream each blob's bytes straight to a backing scratch file via a
// small bounded bufio.Writer (the same bounded buffering every other writer in this
// package uses), rather than an accumulator that grows with total content size.
//
// PutContent only writes through scratchBuf, never appending to a slice-of-slices
// field, so flushing that bounded buffer (without calling Write, which additionally
// reassembles the final store) is enough to prove content lands on the scratch file
// and not in an unbounded in-memory buffer.
func TestContentStoreWriterStreamsContentEagerly(t *testing.T) {
	cw, err := NewContentStoreWriter()
	if err != nil {
		t.Fatalf("NewContentStoreWriter: %v", err)
	}
	t.Cleanup(func() { cw.Close() })

	a := []byte("alpha content streamed eagerly")
	b := []byte("bravo content streamed eagerly, a bit longer")
	cw.PutContent("sha-a", a)
	cw.PutContent("sha-b", b)

	// Re-Put of an already-present sha must add no new bytes to the scratch file.
	cw.PutContent("sha-a", a)

	if cw.scratchPath == "" {
		t.Fatal("writer has no backing scratch file path")
	}
	if err := cw.scratchBuf.Flush(); err != nil {
		t.Fatalf("flush scratch buffer: %v", err)
	}
	got, err := os.ReadFile(cw.scratchPath)
	if err != nil {
		t.Fatalf("read scratch file before Write: %v", err)
	}
	want := append(append([]byte(nil), a...), b...)
	if !bytes.Equal(got, want) {
		t.Errorf("scratch file content before Write = %q, want %q (PutContent must stream through scratchBuf, never accumulate in a separate in-memory buffer)", got, want)
	}
}

// TestContentStoreWriterCloseRemovesScratchFile asserts Close releases the backing
// scratch file (both the handle and the temp file on disk), so an abandoned writer
// (e.g. on an export error path that never reaches Write) doesn't leak a temp file.
func TestContentStoreWriterCloseRemovesScratchFile(t *testing.T) {
	cw, err := NewContentStoreWriter()
	if err != nil {
		t.Fatalf("NewContentStoreWriter: %v", err)
	}
	cw.PutContent("sha-a", []byte("some content"))
	path := cw.scratchPath
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("scratch file missing before Close: %v", err)
	}
	if err := cw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("scratch file %s still exists after Close: err=%v", path, err)
	}
}
