// Package diskstore persists a moedex positional-trigram index to a single
// file and reloads it — either fully into RAM (Load) or with posting lists left
// on disk and mapped lazily (LoadMmap).
//
// # Why a custom binary format
//
// encoding/gob would be simpler but is Go-specific and not mmap-friendly. The
// 2026 redesign wants posting lists that a loader can mmap directly off NVMe so
// RAM holds only content and hot trigram maps (~1.2x corpus), not the postings
// (the 16-byte-per-rune memory wall). So this format is little-endian, uses
// 64-bit offsets throughout, and stores each trigram's posting list as a
// contiguous, individually-addressable byte range — compressed with the
// index package's grouped varint delta codec (see index/codec.go).
//
// # On-disk byte layout (all integers little-endian)
//
//	HEADER (fixed 48 bytes)
//	  magic        [8]byte   "MOEDEX03"
//	  version      uint32    = formatVersion (3)
//	  _reserved    uint32    = 0
//	  numBlobs     uint64    blob count
//	  numTrigrams  uint64    distinct-trigram count
//	  blobOff      uint64    absolute byte offset of the BLOB SECTION
//	  postOff      uint64    absolute byte offset of the POSTINGS SECTION
//
//	BLOB SECTION (starts at blobOff; numBlobs records, blob ID == index order)
//	  per blob:
//	    shaLen     uint32 ; sha [shaLen]byte
//	    contentLen uint64 ; content [contentLen]byte   (indexed UTF-8 content)
//	    numFiles   uint32
//	    per file ref: repoLen u32+bytes, relLen u32+bytes, absLen u32+bytes
//
//	POSTINGS SECTION (starts at postOff; numTrigrams records)
//	  per trigram:
//	    b0,b1,b2   byte byte byte        the three trigram bytes
//	    encLen     uint64                length of the encoded posting list
//	    enc        [encLen]byte          index.EncodePostings output (varint delta)
//
// Because each trigram's encoded list is a self-contained byte range, LoadMmap
// maps the whole file and hands the index sub-slices of the mapping; a query
// decodes only the few trigrams it touches, and the postings never enter the
// Go heap.
package diskstore

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"syscall"

	"moedex/internal/index"
	"moedex/internal/trigram"
)

const (
	magic         = "MOEDEX03"
	formatVersion = 3
	headerSize    = 48
)

// Save writes ix to path in the format described in the package doc.
func Save(ix *index.Index, path string) error {
	blobs := ix.Snapshot()
	trigrams := ix.Trigrams()

	// Serialize the blob section first so we know where the postings section
	// begins.
	blobBuf := make([]byte, 0, 1<<16)
	for _, b := range blobs {
		blobBuf = appendBlob(blobBuf, b)
	}

	blobOff := uint64(headerSize)
	postOff := blobOff + uint64(len(blobBuf))

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)

	hdr := make([]byte, headerSize)
	copy(hdr[0:8], magic)
	binary.LittleEndian.PutUint32(hdr[8:12], formatVersion)
	binary.LittleEndian.PutUint32(hdr[12:16], 0)
	binary.LittleEndian.PutUint64(hdr[16:24], uint64(len(blobs)))
	binary.LittleEndian.PutUint64(hdr[24:32], uint64(len(trigrams)))
	binary.LittleEndian.PutUint64(hdr[32:40], blobOff)
	binary.LittleEndian.PutUint64(hdr[40:48], postOff)
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if _, err := w.Write(blobBuf); err != nil {
		return err
	}

	scratch := make([]byte, 3+8) // 3 trigram bytes + uint64 encLen
	for _, t := range trigrams {
		enc := index.EncodePostings(ix.Postings(t))
		scratch[0], scratch[1], scratch[2] = t[0], t[1], t[2]
		binary.LittleEndian.PutUint64(scratch[3:11], uint64(len(enc)))
		if _, err := w.Write(scratch); err != nil {
			return err
		}
		if _, err := w.Write(enc); err != nil {
			return err
		}
	}
	return w.Flush()
}

func appendBlob(buf []byte, b index.BlobData) []byte {
	buf = appendU32LenBytes(buf, []byte(b.SHA))
	buf = binary.LittleEndian.AppendUint64(buf, uint64(len(b.Content)))
	buf = append(buf, b.Content...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(b.Files)))
	for _, fr := range b.Files {
		buf = appendU32LenBytes(buf, []byte(fr.Repo))
		buf = appendU32LenBytes(buf, []byte(fr.RelPath))
		buf = appendU32LenBytes(buf, []byte(fr.AbsPath))
	}
	return buf
}

func appendU32LenBytes(buf, p []byte) []byte {
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(p)))
	return append(buf, p...)
}

// Load reads a file written by Save fully into memory and reconstructs the
// index (postings materialized into a map). Convenient and self-contained; use
// LoadMmap to keep postings off the heap.
func Load(path string) (*index.Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	hdr, err := parseHeader(data)
	if err != nil {
		return nil, err
	}
	blobs, err := loadBlobs(data[hdr.blobOff:hdr.postOff], int(hdr.numBlobs))
	if err != nil {
		return nil, err
	}
	postings := make(map[trigram.Trigram][]index.Posting, hdr.numTrigrams)
	err = walkPostings(data[hdr.postOff:], int(hdr.numTrigrams), func(t trigram.Trigram, enc []byte) {
		postings[t] = index.DecodePostings(enc)
	})
	if err != nil {
		return nil, err
	}
	return index.Restore(blobs, postings), nil
}

// LoadBlobs reads only the blob section of a shard — every blob's content and
// file refs, in ID order — and skips the postings section entirely. It is for
// callers that need corpus content but not trigram search: the corpus ranker
// concatenates LoadBlobs across shards into one content index (with global IDs)
// and builds BM25/symbol stats over it, never paying the positional-postings
// heap cost that Load would. result[i] is the blob with shard-local ID i.
func LoadBlobs(path string) ([]index.BlobData, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	hdr, err := parseHeader(data)
	if err != nil {
		return nil, err
	}
	return loadBlobs(data[hdr.blobOff:hdr.postOff], int(hdr.numBlobs))
}

// LoadMmap memory-maps a file written by Save and returns an index whose
// posting lists are decoded on demand from the mapping. Blob content is copied
// into RAM; only the postings stay mapped. Call the returned Closer when done —
// querying the index after Close reads unmapped memory and will crash.
func LoadMmap(path string) (*index.Index, io.Closer, error) {
	region, err := mmapOpen(path)
	if err != nil {
		return nil, nil, err
	}
	data := region.data
	hdr, err := parseHeader(data)
	if err != nil {
		region.Close()
		return nil, nil, err
	}
	blobs, err := loadBlobs(data[hdr.blobOff:hdr.postOff], int(hdr.numBlobs))
	if err != nil {
		region.Close()
		return nil, nil, err
	}
	// Map each trigram to a sub-slice of the mapping — no copy.
	raw := make(map[trigram.Trigram][]byte, hdr.numTrigrams)
	err = walkPostings(data[hdr.postOff:], int(hdr.numTrigrams), func(t trigram.Trigram, enc []byte) {
		raw[t] = enc
	})
	if err != nil {
		region.Close()
		return nil, nil, err
	}
	ix := index.RestoreLazy(blobs, &mmapProvider{raw: raw})
	return ix, region, nil
}

// mmapProvider decodes posting lists from sub-slices of an mmap'd file.
type mmapProvider struct {
	raw map[trigram.Trigram][]byte
}

func (p *mmapProvider) Postings(t trigram.Trigram) []index.Posting {
	return index.DecodePostings(p.raw[t])
}

// PostingCount returns how many postings t has without materializing them: it
// walks the grouped-varint encoding summing per-blob counts (and skipping the
// offset deltas). Driver selection in the positional search path uses this to
// pick the rarest trigram before decoding only the winner, so a common trigram
// is never fully decoded just to be measured.
func (p *mmapProvider) PostingCount(t trigram.Trigram) int {
	b := p.raw[t]
	n, pos := 0, 0
	for pos < len(b) {
		_, k := binary.Uvarint(b[pos:]) // blob delta
		if k <= 0 {
			break
		}
		pos += k
		c, k2 := binary.Uvarint(b[pos:]) // count in this blob group
		if k2 <= 0 {
			break
		}
		pos += k2
		n += int(c)
		for i := uint64(0); i < c; i++ {
			_, k3 := binary.Uvarint(b[pos:]) // skip offset delta
			if k3 <= 0 {
				return n
			}
			pos += k3
		}
	}
	return n
}

func (p *mmapProvider) Trigrams() []trigram.Trigram {
	out := make([]trigram.Trigram, 0, len(p.raw))
	for t := range p.raw {
		out = append(out, t)
	}
	return out
}

type header struct {
	numBlobs    uint64
	numTrigrams uint64
	blobOff     uint64
	postOff     uint64
}

func parseHeader(data []byte) (header, error) {
	if len(data) < headerSize {
		return header{}, fmt.Errorf("diskstore: file too small (%d bytes)", len(data))
	}
	if string(data[0:8]) != magic {
		return header{}, fmt.Errorf("diskstore: bad magic %q", data[0:8])
	}
	if v := binary.LittleEndian.Uint32(data[8:12]); v != formatVersion {
		return header{}, fmt.Errorf("diskstore: unsupported version %d", v)
	}
	h := header{
		numBlobs:    binary.LittleEndian.Uint64(data[16:24]),
		numTrigrams: binary.LittleEndian.Uint64(data[24:32]),
		blobOff:     binary.LittleEndian.Uint64(data[32:40]),
		postOff:     binary.LittleEndian.Uint64(data[40:48]),
	}
	if h.blobOff > uint64(len(data)) || h.postOff > uint64(len(data)) || h.blobOff > h.postOff {
		return header{}, fmt.Errorf("diskstore: corrupt section offsets")
	}
	return h, nil
}

// walkPostings iterates the postings section, calling fn with each trigram and
// its encoded byte range (a sub-slice of sec — do not retain past the backing
// buffer's lifetime unless the caller copies).
func walkPostings(sec []byte, n int, fn func(t trigram.Trigram, enc []byte)) error {
	r := &reader{b: sec}
	for i := 0; i < n; i++ {
		tb, err := r.bytes(trigram.N)
		if err != nil {
			return fmt.Errorf("diskstore: trigram %d bytes: %w", i, err)
		}
		encLen, err := r.u64()
		if err != nil {
			return fmt.Errorf("diskstore: trigram %d encLen: %w", i, err)
		}
		enc, err := r.bytes(int(encLen))
		if err != nil {
			return fmt.Errorf("diskstore: trigram %d postings: %w", i, err)
		}
		fn(trigram.Trigram{tb[0], tb[1], tb[2]}, enc)
	}
	return nil
}

func loadBlobs(sec []byte, n int) ([]index.BlobData, error) {
	r := &reader{b: sec}
	blobs := make([]index.BlobData, n)
	for i := 0; i < n; i++ {
		sha, err := r.lenBytes()
		if err != nil {
			return nil, fmt.Errorf("diskstore: blob %d sha: %w", i, err)
		}
		contentLen, err := r.u64()
		if err != nil {
			return nil, fmt.Errorf("diskstore: blob %d content len: %w", i, err)
		}
		content, err := r.bytes(int(contentLen))
		if err != nil {
			return nil, fmt.Errorf("diskstore: blob %d content: %w", i, err)
		}
		numFiles, err := r.u32()
		if err != nil {
			return nil, fmt.Errorf("diskstore: blob %d numFiles: %w", i, err)
		}
		files := make([]index.FileRef, numFiles)
		for j := range files {
			repo, err := r.lenBytes()
			if err != nil {
				return nil, fmt.Errorf("diskstore: blob %d file %d repo: %w", i, j, err)
			}
			rel, err := r.lenBytes()
			if err != nil {
				return nil, fmt.Errorf("diskstore: blob %d file %d rel: %w", i, j, err)
			}
			abs, err := r.lenBytes()
			if err != nil {
				return nil, fmt.Errorf("diskstore: blob %d file %d abs: %w", i, j, err)
			}
			files[j] = index.FileRef{Repo: string(repo), RelPath: string(rel), AbsPath: string(abs)}
		}
		// Copy content out of the backing slice so it survives an munmap and does
		// not pin the whole file buffer.
		blobs[i] = index.BlobData{
			SHA:     string(sha),
			Content: append([]byte(nil), content...),
			Files:   files,
		}
	}
	return blobs, nil
}

// reader is a tiny bounds-checked little-endian cursor over a byte slice.
type reader struct {
	b   []byte
	pos int
}

func (r *reader) need(n int) error {
	if n < 0 || r.pos+n > len(r.b) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func (r *reader) u32() (uint32, error) {
	if err := r.need(4); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint32(r.b[r.pos:])
	r.pos += 4
	return v, nil
}

func (r *reader) u64() (uint64, error) {
	if err := r.need(8); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint64(r.b[r.pos:])
	r.pos += 8
	return v, nil
}

func (r *reader) bytes(n int) ([]byte, error) {
	if err := r.need(n); err != nil {
		return nil, err
	}
	out := r.b[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}

func (r *reader) lenBytes() ([]byte, error) {
	n, err := r.u32()
	if err != nil {
		return nil, err
	}
	return r.bytes(int(n))
}

// mmapRegion is a read-only memory mapping that also serves as the io.Closer
// returned by LoadMmap.
type mmapRegion struct {
	data []byte
}

func mmapOpen(path string) (*mmapRegion, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := int(fi.Size())
	if size < headerSize {
		return nil, fmt.Errorf("diskstore: file too small (%d bytes)", size)
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, size, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("diskstore: mmap: %w", err)
	}
	return &mmapRegion{data: data}, nil
}

func (m *mmapRegion) Close() error {
	if m.data == nil {
		return nil
	}
	err := syscall.Munmap(m.data)
	m.data = nil
	return err
}
