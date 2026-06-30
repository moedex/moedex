package diskstore

import (
	"encoding/binary"
	"testing"
)

// TestLoadBlobsRejectsImplausibleNumFiles is the regression for the
// eager-make-from-untrusted-count gap in loadBlobs (MOEDEX03/04 inlined-content
// format): numFiles is an attacker-controlled uint32 read straight off disk and
// drove make([]index.FileRef, numFiles) before a single file ref was read. A
// corrupt record claiming far more file refs than the remaining section bytes
// could ever encode (each ref needs >= minFileRefSize bytes) must be rejected
// before that allocation happens, not discovered only once the inner
// bounds-checked reads run out of data.
//
// numFiles is chosen well above what a real test machine could safely
// over-allocate by mistake (an unfixed loadBlobs would actually attempt the
// make() for this many elements), while staying far below the uint32 max the
// real finding describes (~4e9, a ~190GB allocation) so the red phase of this
// test cannot itself stress the host running it.
func TestLoadBlobsRejectsImplausibleNumFiles(t *testing.T) {
	var sec []byte
	sec = appendU32LenBytes(sec, []byte("deadbeef"))       // sha
	sec = binary.LittleEndian.AppendUint64(sec, 0)         // contentLen = 0
	sec = binary.LittleEndian.AppendUint32(sec, 5_000_000) // numFiles: no file-ref bytes follow

	_, err := loadBlobs(sec, 1)
	if err == nil {
		t.Fatal("loadBlobs with implausible numFiles = nil error, want rejection")
	}
	if !contains(err.Error(), "numFiles") || !contains(err.Error(), "exceeds remaining data") {
		t.Errorf("loadBlobs err = %q, want it to reject numFiles against remaining data before allocating", err)
	}
}

// TestLoadDedupedBlobsRejectsImplausibleNumFiles is the loadDedupedBlobs
// (MOEDEX05) counterpart of TestLoadBlobsRejectsImplausibleNumFiles: the
// content-less record layout still encodes numFiles as an untrusted uint32
// ahead of the file-ref bytes, and the same eager make([]index.FileRef, ...)
// pattern applies before the content store is ever consulted.
func TestLoadDedupedBlobsRejectsImplausibleNumFiles(t *testing.T) {
	var sec []byte
	sec = appendU32LenBytes(sec, []byte("deadbeef"))       // sha
	sec = binary.LittleEndian.AppendUint32(sec, 5_000_000) // numFiles: no file-ref bytes follow

	_, err := loadDedupedBlobs(sec, 1, nil)
	if err == nil {
		t.Fatal("loadDedupedBlobs with implausible numFiles = nil error, want rejection")
	}
	if !contains(err.Error(), "numFiles") || !contains(err.Error(), "exceeds remaining data") {
		t.Errorf("loadDedupedBlobs err = %q, want it to reject numFiles against remaining data before allocating", err)
	}
}
