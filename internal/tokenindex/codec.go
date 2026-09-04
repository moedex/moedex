package tokenindex

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// TKI2 on-disk format (all integers little-endian).
//
// The format IS the memory layout: every section is a fixed-width array that a
// loader maps and reinterprets rather than parses. See ADR 0005 for the same
// decision applied to positional postings.
//
//	HEADER (128 bytes, zero-padded)
//	  0   magic       [4]byte "TKI2"
//	  4   version     uint32 = 2
//	  8   numDocs     uint64   documents with content (== NumDocs)
//	  16  totalLen    uint64   sum of all document lengths
//	  24  docLenCount uint64   length of the docLen array (maxBlobID+1)
//	  32  numTerms    uint64   distinct terms
//	  40  numPostings uint64   total (term, blob) pairs
//	  48  docLenOff   uint64
//	  56  termOffOff  uint64
//	  64  termTextOff uint64
//	  72  termTextLen uint64
//	  80  postOffOff  uint64
//	  88  postBlobOff uint64
//	  96  postTFOff   uint64
//	  104 fileLen     uint64   total file size, for validation
//	  112 (padding to 128)
//
//	SECTIONS, each starting on a 4-byte boundary, in this order:
//	  docLen   docLenCount   x uint32
//	  termOff  numTerms+1    x uint32
//	  termText termTextLen   x byte
//	  postOff  numTerms+1    x uint32
//	  postBlob numPostings   x uint32
//	  postTF   numPostings   x uint32
//
// Fixed width costs disk (656 MB against the previous varint format's 372 MB on
// the reference corpus) and buys addressability: a loader hands out sub-slices
// instead of decoding 40.6M varints onto the heap.
const (
	tkiMagic      = "TKI2"
	tkiVersion    = 2
	tkiHeaderSize = 128
)

// ErrLegacyFormat means the file is a readable older moedex format that this
// build no longer parses. Callers hold a CACHE: serve's loadPersistedTokens
// already treats any Load error as a miss and rebuilds, so no converter exists
// or is needed.
var ErrLegacyFormat = errors.New("tokenindex: legacy format; rebuild required")

// maxU32 guards every uint32 offset field at write time. Exceeding it means the
// corpus outgrew the format rather than that anything is corrupt, so the error
// names the limit.
const maxU32 = 1<<32 - 1

type tkiHeader struct {
	numDocs     uint64
	totalLen    uint64
	docLenCount uint64
	numTerms    uint64
	numPostings uint64
	docLenOff   uint64
	termOffOff  uint64
	termTextOff uint64
	termTextLen uint64
	postOffOff  uint64
	postBlobOff uint64
	postTFOff   uint64
	fileLen     uint64
}

// align4 rounds n up to the next 4-byte boundary. Every uint32 section must
// start aligned or mmapslice.Uint32s refuses to alias it.
func align4(n uint64) uint64 { return (n + 3) &^ 3 }

// Save writes ti to path in TKI2, via a temp sibling and an atomic rename so a
// crash never leaves a truncated sidecar where a valid one was.
func Save(ti *TokenIndex, path string) error {
	numTerms := uint64(len(ti.termOff))
	if numTerms > 0 {
		numTerms--
	}
	numPostings := uint64(len(ti.postBlob))
	if uint64(len(ti.termText)) > maxU32 {
		return fmt.Errorf("tokenindex: term text %d bytes exceeds the %d-byte TKI2 limit", len(ti.termText), maxU32)
	}
	if numPostings > maxU32 {
		return fmt.Errorf("tokenindex: %d postings exceeds the %d TKI2 limit", numPostings, maxU32)
	}

	h := tkiHeader{
		numDocs:     uint64(ti.numDocs),
		totalLen:    uint64(ti.totalLen),
		docLenCount: uint64(len(ti.docLen)),
		numTerms:    numTerms,
		numPostings: numPostings,
		termTextLen: uint64(len(ti.termText)),
	}
	off := uint64(tkiHeaderSize)
	h.docLenOff = off
	off = align4(off + h.docLenCount*4)
	h.termOffOff = off
	off = align4(off + (h.numTerms+1)*4)
	h.termTextOff = off
	off = align4(off + h.termTextLen)
	h.postOffOff = off
	off = align4(off + (h.numTerms+1)*4)
	h.postBlobOff = off
	off = align4(off + h.numPostings*4)
	h.postTFOff = off
	off = align4(off + h.numPostings*4)
	h.fileLen = off

	buf := make([]byte, h.fileLen)
	copy(buf, tkiMagic)
	le := binary.LittleEndian
	le.PutUint32(buf[4:], tkiVersion)
	le.PutUint64(buf[8:], h.numDocs)
	le.PutUint64(buf[16:], h.totalLen)
	le.PutUint64(buf[24:], h.docLenCount)
	le.PutUint64(buf[32:], h.numTerms)
	le.PutUint64(buf[40:], h.numPostings)
	le.PutUint64(buf[48:], h.docLenOff)
	le.PutUint64(buf[56:], h.termOffOff)
	le.PutUint64(buf[64:], h.termTextOff)
	le.PutUint64(buf[72:], h.termTextLen)
	le.PutUint64(buf[80:], h.postOffOff)
	le.PutUint64(buf[88:], h.postBlobOff)
	le.PutUint64(buf[96:], h.postTFOff)
	le.PutUint64(buf[104:], h.fileLen)

	putU32s(buf[h.docLenOff:], ti.docLen)
	putU32s(buf[h.termOffOff:], ti.termOff)
	copy(buf[h.termTextOff:], ti.termText)
	putU32s(buf[h.postOffOff:], ti.postOff)
	putU32s(buf[h.postBlobOff:], ti.postBlob)
	putU32s(buf[h.postTFOff:], ti.postTF)

	return writeAtomic(path, buf)
}

func putU32s(dst []byte, src []uint32) {
	le := binary.LittleEndian
	for i, v := range src {
		le.PutUint32(dst[i*4:], v)
	}
}

// writeAtomic writes buf to a temp sibling of path, fsyncs it, then renames.
func writeAtomic(path string, buf []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	committed := false
	defer func() {
		if !committed {
			f.Close()
			os.Remove(tmp)
		}
	}()
	// os.CreateTemp makes the file 0600; match os.Create's umask-respecting 0644.
	if err := f.Chmod(0o644); err != nil {
		return err
	}
	if _, err := f.Write(buf); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	committed = true
	return nil
}

// parseHeader validates every section offset against size before any of them is
// used to slice. An mmap'd reader that trusts a bad offset segfaults where a
// parser would have returned an error, so this is the safety boundary.
func parseHeader(b []byte, size uint64) (tkiHeader, error) {
	var h tkiHeader
	if uint64(len(b)) < tkiHeaderSize {
		return h, fmt.Errorf("tokenindex: file too small (%d bytes)", len(b))
	}
	magic := string(b[:4])
	if magic == "TKI1" {
		return h, fmt.Errorf("%w: found TKI1", ErrLegacyFormat)
	}
	if magic != tkiMagic {
		return h, fmt.Errorf("tokenindex: bad magic %q", magic)
	}
	le := binary.LittleEndian
	if v := le.Uint32(b[4:]); v != tkiVersion {
		return h, fmt.Errorf("%w: version %d", ErrLegacyFormat, v)
	}
	h = tkiHeader{
		numDocs:     le.Uint64(b[8:]),
		totalLen:    le.Uint64(b[16:]),
		docLenCount: le.Uint64(b[24:]),
		numTerms:    le.Uint64(b[32:]),
		numPostings: le.Uint64(b[40:]),
		docLenOff:   le.Uint64(b[48:]),
		termOffOff:  le.Uint64(b[56:]),
		termTextOff: le.Uint64(b[64:]),
		termTextLen: le.Uint64(b[72:]),
		postOffOff:  le.Uint64(b[80:]),
		postBlobOff: le.Uint64(b[88:]),
		postTFOff:   le.Uint64(b[96:]),
		fileLen:     le.Uint64(b[104:]),
	}
	if h.fileLen != size {
		return h, fmt.Errorf("tokenindex: header says %d bytes, file is %d", h.fileLen, size)
	}
	sections := []struct {
		name string
		off  uint64
		n    uint64
	}{
		{"docLen", h.docLenOff, h.docLenCount * 4},
		{"termOff", h.termOffOff, (h.numTerms + 1) * 4},
		{"termText", h.termTextOff, h.termTextLen},
		{"postOff", h.postOffOff, (h.numTerms + 1) * 4},
		{"postBlob", h.postBlobOff, h.numPostings * 4},
		{"postTF", h.postTFOff, h.numPostings * 4},
	}
	for _, s := range sections {
		end := s.off + s.n
		if s.off < tkiHeaderSize || end < s.off || end > size {
			return h, fmt.Errorf("tokenindex: section %s [%d,%d) out of bounds for %d-byte file", s.name, s.off, end, size)
		}
	}
	return h, nil
}

// Load reads path into a heap-backed TokenIndex. Task 3 replaces the body with
// an mmap; the signature and semantics are identical either way.
func Load(path string) (*TokenIndex, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	h, err := parseHeader(b, uint64(len(b)))
	if err != nil {
		return nil, err
	}
	ti := &TokenIndex{
		numDocs:  int(h.numDocs),
		totalLen: int(h.totalLen),
		termText: b[h.termTextOff : h.termTextOff+h.termTextLen],
	}
	ti.docLen = readU32s(b[h.docLenOff:], int(h.docLenCount))
	ti.termOff = readU32s(b[h.termOffOff:], int(h.numTerms+1))
	ti.postOff = readU32s(b[h.postOffOff:], int(h.numTerms+1))
	ti.postBlob = readU32s(b[h.postBlobOff:], int(h.numPostings))
	ti.postTF = readU32s(b[h.postTFOff:], int(h.numPostings))
	if err := validateCSR(ti); err != nil {
		return nil, err
	}
	return ti, nil
}

func readU32s(b []byte, n int) []uint32 {
	out := make([]uint32, n)
	le := binary.LittleEndian
	for i := range out {
		out[i] = le.Uint32(b[i*4:])
	}
	return out
}

// validateCSR checks the invariants the accessors rely on but the section
// bounds cannot express: offsets must be non-decreasing and must end exactly at
// their array's length. Without this a corrupt file produces a slice expression
// that panics deep inside a query instead of failing at Load.
func validateCSR(ti *TokenIndex) error {
	n := len(ti.termOff) - 1
	if n < 0 || len(ti.postOff) != n+1 {
		return errors.New("tokenindex: term/posting offset arrays disagree")
	}
	for i := 0; i < n; i++ {
		if ti.termOff[i] > ti.termOff[i+1] || ti.postOff[i] > ti.postOff[i+1] {
			return fmt.Errorf("tokenindex: non-monotonic offsets at term %d", i)
		}
	}
	if n >= 0 && len(ti.termOff) > 0 && int(ti.termOff[n]) != len(ti.termText) {
		return errors.New("tokenindex: termOff does not end at termText length")
	}
	if n >= 0 && len(ti.postOff) > 0 && int(ti.postOff[n]) != len(ti.postBlob) {
		return errors.New("tokenindex: postOff does not end at postBlob length")
	}
	if len(ti.postTF) != len(ti.postBlob) {
		return errors.New("tokenindex: postTF and postBlob lengths differ")
	}
	return nil
}
