package diskstore

// This file regression-tests F-15: LoadBlobs and LoadBlobsDeduped must read
// only the header and the blob-section byte range off disk, never the
// (often far larger) postings section their own doc comments say they skip.
//
// Both tests build a SPARSE shard file whose logical size is dominated by a
// huge "postings section" tail that is never actually written to disk (no
// real bytes, no real trigram records — numTrigrams is 0), then assert
// LoadBlobs/LoadBlobsDeduped return within a generous deadline. A prior
// implementation that (as the finding described) os.ReadFile'd the whole
// file would have to allocate and copy that entire tail into the Go heap —
// measured at ~2.6s locally for a 32 GiB tail on this machine's SSD, vs
// ~13µs for the header+blob-section-only read the fix performs — so a
// regression back to whole-file reads is expected to blow well past the
// deadline below rather than run merely a little slower.

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"moedex/internal/index"
)

// sparseTailBytes is the logical size of the (never-written) tail appended
// past the end of the real section data in both tests below. It is large
// enough that a whole-file read taking low seconds is a reliable, low-noise
// signal, while costing negligible real disk space as a sparse hole.
const sparseTailBytes = 32 << 30 // 32 GiB

// loadBlobsDeadline bounds how long a header+blob-section-only read may take.
// The fixed implementation completes in microseconds regardless of the
// sparse tail's size; a whole-file read of sparseTailBytes takes low
// seconds (measured ~2.7s locally), so this deadline sits far above the
// former and comfortably below the latter (see the file doc comment).
const loadBlobsDeadline = 500 * time.Millisecond

// sparseShardBlob is the single blob written into both synthetic shards
// below.
var sparseShardBlob = index.BlobData{
	SHA:     "deadbeef",
	Content: []byte("package main\n\nfunc main() {}\n"),
	Files:   []index.FileRef{{Repo: "repo", RelPath: "main.go", AbsPath: "/repo/main.go"}},
}

// extendSparse grows f to size total bytes by writing a single byte at the
// last offset, leaving everything in between an unwritten (sparse) hole on
// any filesystem that supports one — so a multi-GiB logical tail costs no
// real disk space.
func extendSparse(t *testing.T, f *os.File, total int64) {
	t.Helper()
	if _, err := f.WriteAt([]byte{0}, total-1); err != nil {
		t.Fatalf("extendSparse: %v", err)
	}
}

// runWithDeadline calls fn in a goroutine and fails the test if it has not
// returned within d. Used instead of a bare call so a regression to a
// whole-file read fails the test deterministically instead of just making
// it slow.
func runWithDeadline(t *testing.T, d time.Duration, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("load failed: %v", err)
		}
	case <-time.After(d):
		t.Fatalf("load did not return within %s — it is reading past the blob section (likely the whole file) instead of just the [blobOff,postOff) byte range", d)
	}
}

// TestLoadBlobsSkipsPostingsSection is the MOEDEX03 regression: LoadBlobs must
// not read the postings section (here, a sparse multi-GiB tail past postOff)
// just to reach the small blob section that precedes it.
func TestLoadBlobsSkipsPostingsSection(t *testing.T) {
	blobBuf := appendBlob(nil, sparseShardBlob)
	blobOff := uint64(headerSize)
	postOff := blobOff + uint64(len(blobBuf))

	hdr := make([]byte, headerSize)
	copy(hdr[0:8], magic)
	binary.LittleEndian.PutUint32(hdr[8:12], formatVersion)
	binary.LittleEndian.PutUint64(hdr[16:24], 1)       // numBlobs
	binary.LittleEndian.PutUint64(hdr[24:32], 0)       // numTrigrams: no real postings records
	binary.LittleEndian.PutUint64(hdr[32:40], blobOff) // blobOff
	binary.LittleEndian.PutUint64(hdr[40:48], postOff) // postOff

	path := filepath.Join(t.TempDir(), "sparse.moedex")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(hdr, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(blobBuf, int64(blobOff)); err != nil {
		t.Fatal(err)
	}
	extendSparse(t, f, int64(postOff)+sparseTailBytes)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	var got []index.BlobData
	runWithDeadline(t, loadBlobsDeadline, func() error {
		var loadErr error
		got, loadErr = LoadBlobs(path)
		return loadErr
	})

	if len(got) != 1 {
		t.Fatalf("LoadBlobs returned %d blobs, want 1", len(got))
	}
	if got[0].SHA != sparseShardBlob.SHA || string(got[0].Content) != string(sparseShardBlob.Content) {
		t.Errorf("LoadBlobs blob = %+v, want SHA=%q Content=%q", got[0], sparseShardBlob.SHA, sparseShardBlob.Content)
	}
	if len(got[0].Files) != 1 || got[0].Files[0] != sparseShardBlob.Files[0] {
		t.Errorf("LoadBlobs blob Files = %+v, want %+v", got[0].Files, sparseShardBlob.Files)
	}
}

// TestLoadBlobsDedupedSkipsPostingsSection is the MOEDEX05 (deduped)
// counterpart of TestLoadBlobsSkipsPostingsSection: LoadBlobsDeduped must not
// read the postings section either.
func TestLoadBlobsDedupedSkipsPostingsSection(t *testing.T) {
	blobBuf := appendDedupedBlob(nil, sparseShardBlob)
	blobOff := uint64(headerSizeV5)
	postOff := blobOff + uint64(len(blobBuf))

	hdr := make([]byte, headerSizeV5)
	copy(hdr[0:8], magicDeduped)
	binary.LittleEndian.PutUint32(hdr[8:12], formatVersionV5)
	binary.LittleEndian.PutUint64(hdr[16:24], 1)       // numBlobs
	binary.LittleEndian.PutUint64(hdr[24:32], 0)       // numTrigrams: no real postings records
	binary.LittleEndian.PutUint64(hdr[32:40], blobOff) // blobOff
	binary.LittleEndian.PutUint64(hdr[40:48], postOff) // postOff

	path := filepath.Join(t.TempDir(), "sparse-deduped.moedex")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(hdr, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(blobBuf, int64(blobOff)); err != nil {
		t.Fatal(err)
	}
	extendSparse(t, f, int64(postOff)+sparseTailBytes)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	cw := newTestContentStoreWriter(t)
	cw.PutContent(sparseShardBlob.SHA, sparseShardBlob.Content)
	csPath := filepath.Join(t.TempDir(), ContentStoreName)
	if err := cw.Write(csPath); err != nil {
		t.Fatalf("content store Write: %v", err)
	}
	cs, err := OpenContentStore(csPath)
	if err != nil {
		t.Fatalf("OpenContentStore: %v", err)
	}
	defer cs.Close()

	var got []index.BlobData
	runWithDeadline(t, loadBlobsDeadline, func() error {
		var loadErr error
		got, loadErr = LoadBlobsDeduped(path, cs)
		return loadErr
	})

	if len(got) != 1 {
		t.Fatalf("LoadBlobsDeduped returned %d blobs, want 1", len(got))
	}
	if got[0].SHA != sparseShardBlob.SHA || string(got[0].Content) != string(sparseShardBlob.Content) {
		t.Errorf("LoadBlobsDeduped blob = %+v, want SHA=%q Content=%q", got[0], sparseShardBlob.SHA, sparseShardBlob.Content)
	}
	if len(got[0].Files) != 1 || got[0].Files[0] != sparseShardBlob.Files[0] {
		t.Errorf("LoadBlobsDeduped blob Files = %+v, want %+v", got[0].Files, sparseShardBlob.Files)
	}
}
