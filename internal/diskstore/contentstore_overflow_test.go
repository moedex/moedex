package diskstore

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// writeContentStoreWithDirEntry synthesizes a minimal MOECONT1 file with a
// single directory entry (sha, off, clen) and no content section, returning
// the file path. It lets tests construct directory records the writer would
// never produce, to probe OpenContentStore's bounds validation directly.
func writeContentStoreWithDirEntry(t *testing.T, sha string, off, clen uint64) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ContentStoreName)

	var dirSec []byte
	dirSec = appendU32LenBytes(dirSec, []byte(sha))
	dirSec = binary.LittleEndian.AppendUint64(dirSec, off)
	dirSec = binary.LittleEndian.AppendUint64(dirSec, clen)

	dirOff := uint64(contentHeaderSize)
	hdr := make([]byte, contentHeaderSize)
	copy(hdr[0:8], contentMagic)
	binary.LittleEndian.PutUint32(hdr[8:12], contentStoreVersion)
	binary.LittleEndian.PutUint64(hdr[16:24], 1) // numBlobs
	binary.LittleEndian.PutUint64(hdr[24:32], dirOff)

	if err := os.WriteFile(path, append(hdr, dirSec...), 0o644); err != nil {
		t.Fatalf("write content store: %v", err)
	}
	return path
}

// TestOpenContentStoreRejectsOverflowingDirEntry is the regression for the
// off+clen integer-overflow gap: OpenContentStore's bounds check computed
// off+clen as uint64 BEFORE comparing it to len(data). Both off and clen come
// straight from an untrusted blobs.dat, so a crafted pair whose sum wraps
// around uint64 back to a small value sails through the guard even though off
// alone is far past EOF. The wrapped off, cast to int64, goes negative and a
// later cs.region.data[ref.Offset:ref.Offset+ref.Len] slice panics instead of
// failing cleanly. OpenContentStore must reject this at load time with an
// error, never a panic, and never an accepted (corrupt) entry.
func TestOpenContentStoreRejectsOverflowingDirEntry(t *testing.T) {
	cases := []struct {
		name string
		off  uint64
		clen uint64
	}{
		{
			name: "off near max, clen wraps sum small",
			off:  math.MaxUint64 - 4, // far past EOF on its own
			clen: 10,                 // off+clen overflows uint64 to 5
		},
		{
			name: "small off, huge clen wraps sum small",
			off:  10,
			clen: math.MaxUint64 - 4, // off+clen overflows uint64 to 5
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Sanity: this case only exercises the bug if the raw sum actually
			// overflows uint64 (wraps below the file size).
			if tc.off+tc.clen >= tc.off && tc.off+tc.clen >= tc.clen {
				// no overflow occurred — would also overflow check via off/clen
				// alone, so this case wouldn't isolate the wraparound bug.
				if tc.off <= 64 && tc.clen <= 64 {
					t.Fatalf("test premise: off+clen must overflow uint64 to exercise the bug")
				}
			}

			path := writeContentStoreWithDirEntry(t, "evil", tc.off, tc.clen)

			cs, err := OpenContentStore(path)
			if err == nil {
				cs.Close()
				t.Fatalf("OpenContentStore(off=%d, clen=%d) = nil error, want a rejection of the out-of-bounds directory entry", tc.off, tc.clen)
			}
		})
	}
}

// TestDedupedStoreOrderRejectsHugeNumBlobsWithoutPanic is the regression for the
// numBlobs capacity-hint gap: dedupedStoreOrder (the engine behind
// ContentStoreSHAs and content-store compaction) read the header's numBlobs and
// validated only dirOff against len(data) before doing
// make([]string, 0, numBlobs). A corrupt store with an enormous numBlobs (e.g.
// ~1.8e19, near math.MaxUint64) makes that eager capacity hint attempt a huge
// allocation and panic/OOM, even though the bounds-checked read loop right
// below it would have cleanly rejected the same file once it ran out of
// directory bytes. This runs against a header whose numBlobs vastly exceeds
// what the (empty) directory section could ever hold, so the function must
// reject it with a clean error and must never panic.
func TestDedupedStoreOrderRejectsHugeNumBlobsWithoutPanic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ContentStoreName)

	dirOff := uint64(contentHeaderSize)
	hdr := make([]byte, contentHeaderSize)
	copy(hdr[0:8], contentMagic)
	binary.LittleEndian.PutUint32(hdr[8:12], contentStoreVersion)
	binary.LittleEndian.PutUint64(hdr[16:24], math.MaxUint64/8) // numBlobs: corrupt
	binary.LittleEndian.PutUint64(hdr[24:32], dirOff)

	// No directory section at all: the header lies about numBlobs but the file
	// has zero bytes of actual directory data after dirOff.
	if err := os.WriteFile(path, hdr, 0o644); err != nil {
		t.Fatalf("write content store: %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ContentStoreSHAs panicked on corrupt numBlobs header: %v", r)
		}
	}()

	if _, err := ContentStoreSHAs(path); err == nil {
		t.Fatalf("ContentStoreSHAs(corrupt numBlobs) = nil error, want rejection")
	}
}

// TestContentStoreContentRejectsInvalidRef is the defense-in-depth regression
// for F-045: today OpenContentStore is the ONLY place that validates a
// ContentRef's offset/length before it lands in bySHA, so Content() blindly
// trusts every ref it looks up and slices the mmap directly. That concentrates
// all of the content store's bounds safety in a single check — if it ever
// regresses (or a ContentStore is built some other way), Content() has no
// second line of defense and slices straight into a panic or, worse, an
// out-of-bounds read of mmap'd memory.
//
// Content() must re-validate ref.Offset/ref.Len itself — rejecting a negative
// offset/length or one that runs past the mapped data — and report ok=false
// rather than ever panicking.
func TestContentStoreContentRejectsInvalidRef(t *testing.T) {
	cases := []struct {
		name string
		ref  ContentRef
	}{
		{"negative offset", ContentRef{Offset: -1, Len: 1}},
		{"negative len", ContentRef{Offset: 0, Len: -1}},
		{"offset past end", ContentRef{Offset: 100, Len: 1}},
		{"len past end", ContentRef{Offset: 0, Len: 100}},
		{"offset+len overflow", ContentRef{Offset: math.MaxInt64 - 4, Len: 10}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := &ContentStore{
				region: &mmapRegion{data: []byte("hello")},
				bySHA:  map[string]ContentRef{"sha": tc.ref},
			}
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Content panicked on invalid ref %+v: %v", tc.ref, r)
				}
			}()
			if content, ok := cs.Content("sha"); ok {
				t.Fatalf("Content(ref=%+v) = (%q, true), want ok=false for an invalid ref", tc.ref, content)
			}
		})
	}
}
