package blobstore

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// writeIdxWithEntry synthesizes a minimal blobs.idx with one entry, letting
// tests construct (packOff, packLen) pairs the writer would never produce, to
// probe loadIndex's bounds validation directly.
func writeIdxWithEntry(t *testing.T, dir, sha string, packOff, packLen, contentLen, packBytes uint64) {
	t.Helper()
	buf := make([]byte, idxHeader)
	copy(buf[0:8], idxMagic)
	binary.LittleEndian.PutUint32(buf[8:12], idxVersion)
	binary.LittleEndian.PutUint64(buf[16:24], 1) // numBlobs
	binary.LittleEndian.PutUint64(buf[24:32], packBytes)
	buf = appendU32LenBytes(buf, []byte(sha))
	buf = binary.LittleEndian.AppendUint64(buf, packOff)
	buf = binary.LittleEndian.AppendUint64(buf, packLen)
	buf = binary.LittleEndian.AppendUint64(buf, contentLen)
	if err := os.WriteFile(filepath.Join(dir, idxName), buf, 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
}

// TestOpenRejectsOutOfBoundsIndexEntry is the regression for the unvalidated
// packOff+packLen gap: loadIndex read packOff/packLen/contentLen straight from
// blobs.idx with no per-entry check that packOff+packLen <= packBytes (only
// the aggregate pack-file size vs s.packPos was checked in Open). A corrupt or
// truncated index could carry an entry whose declared range overruns the
// pack; that entry would sail into s.byID and later let Get attempt an
// unbounded make([]byte, e.packLen) and an out-of-bounds ReadAt. Open must
// reject such an index at load time instead.
func TestOpenRejectsOutOfBoundsIndexEntry(t *testing.T) {
	cases := []struct {
		name      string
		packOff   uint64
		packLen   uint64
		packBytes uint64
	}{
		{
			name:      "packLen alone exceeds packBytes",
			packOff:   0,
			packLen:   1 << 40,
			packBytes: 5,
		},
		{
			// packOff and packLen are both untrusted uint64 values read straight
			// from the file; their sum can overflow uint64 and wrap back below
			// packBytes, so the check must compare each bound separately rather
			// than summing first.
			name:      "packOff near max, packLen wraps sum small",
			packOff:   math.MaxUint64 - 4,
			packLen:   10,
			packBytes: 5,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, packName), make([]byte, tc.packBytes), 0o644); err != nil {
				t.Fatal(err)
			}
			writeIdxWithEntry(t, dir, "deadbeef", tc.packOff, tc.packLen, tc.packLen, tc.packBytes)

			if _, err := Open(dir); err == nil {
				t.Fatalf("Open(packOff=%d, packLen=%d, packBytes=%d) = nil error, want rejection of the out-of-bounds index entry", tc.packOff, tc.packLen, tc.packBytes)
			}
		})
	}
}

// TestGetRejectsOutOfBoundsEntry is defense-in-depth for the same class of bug
// directly at the Get() call site: even if an out-of-bounds entry somehow
// reached s.byID, Get must reject it instead of doing
// make([]byte, e.packLen) (unbounded alloc) and ReadAt past the pack.
func TestGetRejectsOutOfBoundsEntry(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Put("sha1", []byte("hello")); err != nil {
		t.Fatal(err)
	}

	// Simulate a corrupt entry that slipped past load-time validation.
	e := s.byID["sha1"]
	e.packLen = 1 << 40
	s.byID["sha1"] = e

	if _, err := s.Get("sha1"); err == nil {
		t.Fatal("Get should reject an entry whose pack range exceeds the pack size")
	}
}
