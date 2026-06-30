package parity

import (
	"os"
	"path/filepath"
	"testing"
)

// F-052: writeMirror must create its bucket directory unconditionally. The old
// code only MkdirAll'd when id%1000==0 and relied on id 0 having already
// created the bucket for ids 1..999 — any write that skips id 0 (parallel,
// reordered, or a non-zero starting id) hits a missing directory and fails.
func TestWriteMirror_CreatesBucketDirWithoutID0(t *testing.T) {
	dir := t.TempDir()

	// Write id 1 first, deliberately never writing id 0 in this bucket.
	if err := writeMirror(dir, 1, []byte("hello")); err != nil {
		t.Fatalf("writeMirror(id=1) without id=0 having run: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "000", "1"))
	if err != nil {
		t.Fatalf("reading mirrored file: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q, want %q", got, "hello")
	}
}

func TestWriteMirror_NonZeroStartOfLaterBucket(t *testing.T) {
	dir := t.TempDir()

	// id 2001 is the first write into bucket "002"; id 2000 (the %1000==0
	// trigger) never runs.
	if err := writeMirror(dir, 2001, []byte("world")); err != nil {
		t.Fatalf("writeMirror(id=2001) without id=2000 having run: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "002", "2001"))
	if err != nil {
		t.Fatalf("reading mirrored file: %v", err)
	}
	if string(got) != "world" {
		t.Fatalf("content = %q, want %q", got, "world")
	}
}
