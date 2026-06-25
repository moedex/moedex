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
//
// # MOEDEX04 — selective (workload-aware) shards
//
// A selective index (built via index.Builder, which keeps only the grams a
// GramSelector chose) is written in a superset format, MOEDEX04, with a larger
// 64-byte header (the MOEDEX03 fields plus selOff/selCount) and one extra
// trailing section:
//
//	SELECTION SECTION (starts at selOff; selCount records)
//	  per kept gram: b0,b1,b2  byte byte byte   the three trigram bytes
//
// Only kept grams have posting lists, so the postings section is identical in
// shape to MOEDEX03 (and bounded above by selOff rather than EOF). The selection
// section is the persisted membership oracle: a loaded MOEDEX04 shard answers
// index.IndexedGram(t)==true exactly for the kept grams, and false for every
// deselected gram (which the query layer then treats as force-scan, never as
// zero occurrences). A default all-trigram build still writes MOEDEX03 verbatim,
// and a MOEDEX03 shard loads as all-indexed, so existing servable shard dirs
// keep exact ripgrep parity with no rebuild.
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

	// MOEDEX04 adds a SELECTION section for selective (workload-aware) shards.
	// Its header is 64 bytes: the MOEDEX03 layout (through postOff at 40:48) plus
	// selOff (48:56) and selCount (56:64). The selection section, at selOff, is
	// selCount records of 3 trigram bytes each — the authoritative keep-set the
	// selector chose. A loaded MOEDEX04 shard reports IndexedGram true exactly for
	// those grams and false for every other (deselected → force-scan) gram.
	//
	// Back-compat: a default (all-trigram) build still writes MOEDEX03 verbatim
	// (Save only emits MOEDEX04 when the index carries a selection set), so every
	// existing servable shard dir and the daemon keep reading the old format
	// unchanged, and a MOEDEX03 shard loads as all-indexed (IndexedGram universally
	// true) with no rebuild.
	magicSelective  = "MOEDEX04"
	formatVersionV4 = 4
	headerSizeV4    = 64
)

// Save writes ix to path. A selective index (ix.SelectedGrams() != nil) is
// written in the MOEDEX04 format with a selection section; an all-trigram index
// is written in the back-compatible MOEDEX03 format described in the package doc.
func Save(ix *index.Index, path string) error {
	if sel := ix.SelectedGrams(); sel != nil {
		return saveSelective(ix, sel, path)
	}
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

// saveSelective writes a MOEDEX04 shard: the MOEDEX03 sections plus a SELECTION
// section enumerating the keep-set. Only the kept grams have posting lists (they
// are exactly ix.Trigrams() for a selective index, since dropped grams were
// never materialized), so the postings section is identical in shape to MOEDEX03
// — just smaller. The selection section is what lets the loader answer
// IndexedGram for a gram with NO postings (a kept gram that happens to occur in
// zero blobs cannot arise here, but a deselected gram that DOES occur must read
// as not-indexed, which the keep-set membership gives us).
func saveSelective(ix *index.Index, sel map[trigram.Trigram]struct{}, path string) error {
	blobs := ix.Snapshot()
	trigrams := ix.Trigrams()

	blobBuf := make([]byte, 0, 1<<16)
	for _, b := range blobs {
		blobBuf = appendBlob(blobBuf, b)
	}
	blobOff := uint64(headerSizeV4)
	postOff := blobOff + uint64(len(blobBuf))

	// Postings buffer, so we know where the selection section begins.
	postBuf := make([]byte, 0, 1<<16)
	scratch := make([]byte, 3+8)
	for _, t := range trigrams {
		enc := index.EncodePostings(ix.Postings(t))
		scratch[0], scratch[1], scratch[2] = t[0], t[1], t[2]
		binary.LittleEndian.PutUint64(scratch[3:11], uint64(len(enc)))
		postBuf = append(postBuf, scratch...)
		postBuf = append(postBuf, enc...)
	}
	selOff := postOff + uint64(len(postBuf))

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)

	hdr := make([]byte, headerSizeV4)
	copy(hdr[0:8], magicSelective)
	binary.LittleEndian.PutUint32(hdr[8:12], formatVersionV4)
	binary.LittleEndian.PutUint32(hdr[12:16], 0)
	binary.LittleEndian.PutUint64(hdr[16:24], uint64(len(blobs)))
	binary.LittleEndian.PutUint64(hdr[24:32], uint64(len(trigrams)))
	binary.LittleEndian.PutUint64(hdr[32:40], blobOff)
	binary.LittleEndian.PutUint64(hdr[40:48], postOff)
	binary.LittleEndian.PutUint64(hdr[48:56], selOff)
	binary.LittleEndian.PutUint64(hdr[56:64], uint64(len(sel)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if _, err := w.Write(blobBuf); err != nil {
		return err
	}
	if _, err := w.Write(postBuf); err != nil {
		return err
	}
	// SELECTION section: 3 bytes per kept gram.
	selBuf := make([]byte, 0, len(sel)*3)
	for t := range sel {
		selBuf = append(selBuf, t[0], t[1], t[2])
	}
	if _, err := w.Write(selBuf); err != nil {
		return err
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
	err = walkPostings(data[hdr.postOff:postEnd(data, hdr)], int(hdr.numTrigrams), func(t trigram.Trigram, enc []byte) {
		postings[t] = index.DecodePostings(enc)
	})
	if err != nil {
		return nil, err
	}
	if hdr.selective {
		return index.RestoreSelective(blobs, postings, loadSelection(data, hdr)), nil
	}
	return index.Restore(blobs, postings), nil
}

// postEnd returns the byte offset at which the postings section ends: the start
// of the SELECTION section for a MOEDEX04 shard, or end-of-file for MOEDEX03.
func postEnd(data []byte, hdr header) uint64 {
	if hdr.selective {
		return hdr.selOff
	}
	return uint64(len(data))
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
	err = walkPostings(data[hdr.postOff:postEnd(data, hdr)], int(hdr.numTrigrams), func(t trigram.Trigram, enc []byte) {
		raw[t] = enc
	})
	if err != nil {
		region.Close()
		return nil, nil, err
	}
	base := &mmapProvider{raw: raw}
	if hdr.selective {
		// A selective shard wraps the provider in one that ALSO reports gram
		// membership (index.gramMember), so IndexedGram answers from the small
		// heap-copied keep-set without the postings on heap. A non-selective shard
		// uses the plain mmapProvider, which does NOT implement gramMember, so the
		// loaded index keeps the all-indexed (universal-true) fast path and never
		// pays the per-gram membership check.
		ix := index.RestoreLazy(blobs, &selectiveMmapProvider{
			mmapProvider: base,
			selected:     loadSelection(data, hdr),
		})
		return ix, region, nil
	}
	ix := index.RestoreLazy(blobs, base)
	return ix, region, nil
}

// mmapProvider decodes posting lists from sub-slices of an mmap'd file. It is the
// all-trigram (non-selective) provider; it does NOT implement gram membership, so
// an index restored from it stays on the all-indexed universal-true fast path.
type mmapProvider struct {
	raw map[trigram.Trigram][]byte
}

func (p *mmapProvider) Postings(t trigram.Trigram) []index.Posting {
	return index.DecodePostings(p.raw[t])
}

// selectiveMmapProvider is a MOEDEX04 (selective) shard's provider: it adds the
// authoritative keep-set so IndexedGram can report a deselected gram as
// not-indexed (forcing the query layer to scan) — the membership oracle for a
// lazily-loaded selective shard. The keep-set is small (3 bytes/gram) so it lives
// on the heap for O(1) lookups; the postings still stay mmap'd.
type selectiveMmapProvider struct {
	*mmapProvider
	selected map[trigram.Trigram]struct{}
}

// IndexedGram implements index.gramMember: a gram is a trustworthy filter iff it
// is in the keep-set.
func (p *selectiveMmapProvider) IndexedGram(t trigram.Trigram) bool {
	_, ok := p.selected[t]
	return ok
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
	// selective is true for a MOEDEX04 shard; then selOff/selCount locate the
	// SELECTION section (the authoritative keep-set). For a MOEDEX03 shard
	// selective is false and the index loads as all-indexed (back-compat).
	selective bool
	selOff    uint64
	selCount  uint64
}

func parseHeader(data []byte) (header, error) {
	if len(data) < headerSize {
		return header{}, fmt.Errorf("diskstore: file too small (%d bytes)", len(data))
	}
	switch string(data[0:8]) {
	case magic:
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
	case magicSelective:
		if len(data) < headerSizeV4 {
			return header{}, fmt.Errorf("diskstore: selective file too small (%d bytes)", len(data))
		}
		if v := binary.LittleEndian.Uint32(data[8:12]); v != formatVersionV4 {
			return header{}, fmt.Errorf("diskstore: unsupported version %d", v)
		}
		h := header{
			numBlobs:    binary.LittleEndian.Uint64(data[16:24]),
			numTrigrams: binary.LittleEndian.Uint64(data[24:32]),
			blobOff:     binary.LittleEndian.Uint64(data[32:40]),
			postOff:     binary.LittleEndian.Uint64(data[40:48]),
			selective:   true,
			selOff:      binary.LittleEndian.Uint64(data[48:56]),
			selCount:    binary.LittleEndian.Uint64(data[56:64]),
		}
		if h.blobOff > uint64(len(data)) || h.postOff > uint64(len(data)) ||
			h.selOff > uint64(len(data)) || h.blobOff > h.postOff || h.postOff > h.selOff {
			return header{}, fmt.Errorf("diskstore: corrupt section offsets")
		}
		if h.selOff+h.selCount*trigram.N > uint64(len(data)) {
			return header{}, fmt.Errorf("diskstore: corrupt selection section")
		}
		return h, nil
	default:
		return header{}, fmt.Errorf("diskstore: bad magic %q", data[0:8])
	}
}

// loadSelection reads the SELECTION section (3 bytes per kept gram) into a set.
// Only valid when h.selective.
func loadSelection(data []byte, h header) map[trigram.Trigram]struct{} {
	sel := make(map[trigram.Trigram]struct{}, h.selCount)
	base := h.selOff
	for i := uint64(0); i < h.selCount; i++ {
		off := base + i*trigram.N
		sel[trigram.Trigram{data[off], data[off+1], data[off+2]}] = struct{}{}
	}
	return sel
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
