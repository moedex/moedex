package embed

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"moedex/internal/mmapslice"
)

// MDXE v3 on-disk format (all integers little-endian).
//
//	HEADER (64 bytes, zero-padded)
//	  0  magic     [4]byte "MDXE"
//	  4  version   uint32 = 3
//	  8  dim       uint32
//	  12 count     uint32   number of chunks
//	  16 quant     uint8    0 = float32, 1 = int8 + per-vector scale
//	  17 (7 bytes reserved, zero)
//	  24 chunksOff uint64
//	  32 keysOff   uint64
//	  40 vecOff    uint64   ALWAYS a multiple of 64
//	  48 scaleOff  uint64   quant==1 only; 0 when quant==0; a multiple of 4
//	  56 fileLen   uint64
//
//	SECTIONS
//	  chunks count x 32 bytes: blob u64, startLine u32, endLine u32,
//	                           startByte u64, endByte u64
//	  keys   count x 16 bytes
//	  vec    quant==0: count*dim x float32   (64-byte aligned)
//	         quant==1: count*dim x int8
//	  scale  quant==1: count x float32       (4-byte aligned)
//
// vecOff is padded to 64 bytes so mmapslice.Float32s can alias the block and
// each vector starts on a cache line. scaleOff is padded to 4 bytes for the
// same reason at a smaller grain: it is itself a float32 array that
// mmapslice.Float32s aliases, and that alias fails outright (rather than
// silently misreading) when the offset isn't 4-byte aligned. The int8 vec
// block ahead of it has no alignment of its own, so when count*dim isn't a
// multiple of 4, scaleOff needs explicit padding — it does not inherit
// vecOff's 64-byte alignment for free. Versions 1 and 2 load as
// ErrLegacyFormat: the store is a CACHE whose .meta validator already forces
// a rebuild on any load failure, so no converter exists.
const (
	storeMagic4     = "MDXE"
	storeVersion    = 3
	storeHeaderSize = 64
	quantF32        = 0
	quantInt8       = 1
	chunkRecordSize = 32
	keyRecordSize   = 16
	vecAlign        = 64
	scaleAlign      = 4

	// maxStoreDim and maxStoreCount ceiling the header's dim/count BEFORE any
	// multiplication in parseStoreHeader's section-bounds math, so that math
	// itself can never overflow uint64. Real embedding models run 384-4096
	// dims, so 2^16 is generous; 2^31 chunks is likewise far past any corpus
	// this engine indexes. Bounding each individually bounds their product at
	// 2^47, and the *4 (float32) or *4 (scale) that bounds check applies to
	// it at 2^49 — comfortably inside uint64 (2^64), so no subsequent
	// multiplication of a validated dim/count can wrap. Without this, a
	// crafted or corrupt header's count*dim*4 could wrap uint64 and pass the
	// bounds check with a falsely-small size — survivable against an
	// os.ReadFile buffer today, but Task 10 hands parseStoreHeader a mapped
	// view, where a wrapped check lets a read walk off the mapping (a
	// page-guarded SIGSEGV, not silent corruption, but still a daemon crash
	// instead of a clean load error).
	maxStoreDim   = 1 << 16
	maxStoreCount = 1 << 31
)

// ErrLegacyFormat means the file is an older MDXE version this build no longer
// parses. Callers treat it as a cache miss and re-embed.
var ErrLegacyFormat = errors.New("embed: legacy store format; rebuild required")

type storeHeader struct {
	dim       uint32
	count     uint32
	quant     uint8
	chunksOff uint64
	keysOff   uint64
	vecOff    uint64
	scaleOff  uint64
	fileLen   uint64
}

func alignUp(n, to uint64) uint64 { return (n + to - 1) &^ (to - 1) }

// Save writes the store (chunks + content keys + vectors) to path in format v3.
// A non-empty store MUST carry a content key per chunk (HasKeys); Save refuses an
// inconsistent store rather than persisting one that cannot seed incremental reuse.
func (s *Store) Save(path string) error {
	if len(s.chunks) > 0 && !s.HasKeys() {
		return fmt.Errorf("embed: refusing to save store without content keys (%d chunks, %d keys); call FillKeys first", len(s.chunks), len(s.keys))
	}
	count := uint64(len(s.chunks))
	dim := uint64(s.dim)

	h := storeHeader{dim: uint32(s.dim), count: uint32(count), quant: quantF32}
	off := uint64(storeHeaderSize)
	h.chunksOff = off
	off += count * chunkRecordSize
	h.keysOff = off
	off += count * keyRecordSize
	h.vecOff = alignUp(off, vecAlign)
	off = h.vecOff + count*dim*4
	h.fileLen = off

	buf := make([]byte, h.fileLen)
	le := binary.LittleEndian
	copy(buf, storeMagic4)
	le.PutUint32(buf[4:], storeVersion)
	le.PutUint32(buf[8:], h.dim)
	le.PutUint32(buf[12:], h.count)
	buf[16] = h.quant
	le.PutUint64(buf[24:], h.chunksOff)
	le.PutUint64(buf[32:], h.keysOff)
	le.PutUint64(buf[40:], h.vecOff)
	le.PutUint64(buf[48:], h.scaleOff)
	le.PutUint64(buf[56:], h.fileLen)

	for i, c := range s.chunks {
		r := buf[h.chunksOff+uint64(i)*chunkRecordSize:]
		le.PutUint64(r[0:], c.Blob)
		le.PutUint32(r[8:], uint32(c.StartLine))
		le.PutUint32(r[12:], uint32(c.EndLine))
		le.PutUint64(r[16:], uint64(c.StartByte))
		le.PutUint64(r[24:], uint64(c.EndByte))
	}
	for i, k := range s.keys {
		copy(buf[h.keysOff+uint64(i)*keyRecordSize:], k[:])
	}
	for i, v := range s.vec {
		le.PutUint32(buf[h.vecOff+uint64(i)*4:], math.Float32bits(v))
	}
	return writeStoreAtomic(path, buf)
}

// SaveQuantized writes the store in int8 format (MDXE v3, quant=1): each
// vector is quantized independently (Quantize) with its own scale, cutting
// the mapped vector block to roughly a quarter of Save's float32 size. This
// is a separate entry point from Save, which always writes float32 —
// int8 is opt-in and never affects what Save produces.
func (s *Store) SaveQuantized(path string) error {
	if len(s.chunks) > 0 && !s.HasKeys() {
		return fmt.Errorf("embed: refusing to save store without content keys (%d chunks, %d keys); call FillKeys first", len(s.chunks), len(s.keys))
	}
	count := uint64(len(s.chunks))
	dim := uint64(s.dim)

	h := storeHeader{dim: uint32(s.dim), count: uint32(count), quant: quantInt8}
	off := uint64(storeHeaderSize)
	h.chunksOff = off
	off += count * chunkRecordSize
	h.keysOff = off
	off += count * keyRecordSize
	h.vecOff = alignUp(off, vecAlign)
	off = h.vecOff + count*dim
	h.scaleOff = alignUp(off, scaleAlign)
	off = h.scaleOff + count*4
	h.fileLen = off

	buf := make([]byte, h.fileLen)
	le := binary.LittleEndian
	copy(buf, storeMagic4)
	le.PutUint32(buf[4:], storeVersion)
	le.PutUint32(buf[8:], h.dim)
	le.PutUint32(buf[12:], h.count)
	buf[16] = h.quant
	le.PutUint64(buf[24:], h.chunksOff)
	le.PutUint64(buf[32:], h.keysOff)
	le.PutUint64(buf[40:], h.vecOff)
	le.PutUint64(buf[48:], h.scaleOff)
	le.PutUint64(buf[56:], h.fileLen)

	for i, c := range s.chunks {
		r := buf[h.chunksOff+uint64(i)*chunkRecordSize:]
		le.PutUint64(r[0:], c.Blob)
		le.PutUint32(r[8:], uint32(c.StartLine))
		le.PutUint32(r[12:], uint32(c.EndLine))
		le.PutUint64(r[16:], uint64(c.StartByte))
		le.PutUint64(r[24:], uint64(c.EndByte))
	}
	for i, k := range s.keys {
		copy(buf[h.keysOff+uint64(i)*keyRecordSize:], k[:])
	}
	for i := range s.chunks {
		q, scale := Quantize(s.vecAt(i))
		dst := buf[h.vecOff+uint64(i)*dim:]
		for j, x := range q {
			dst[j] = byte(x)
		}
		le.PutUint32(buf[h.scaleOff+uint64(i)*4:], math.Float32bits(scale))
	}
	return writeStoreAtomic(path, buf)
}

// writeStoreAtomic writes buf to path via a temp sibling and atomic rename: a
// crash or torn write must never leave a truncated multi-GB store where a
// valid one was — the daemon would load that corrupt sidecar on its next
// reload and silently drop to lexical. Rename is atomic within a filesystem;
// the temp sits in the same dir so it shares path's filesystem. The original
// is untouched until the rename.
func writeStoreAtomic(path string, buf []byte) error {
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
	// os.CreateTemp makes the file 0600; match os.Create's umask-respecting 0644 so a
	// refresh doesn't silently tighten the sidecar's permissions.
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

// StoreHeader is the cheap, vectors-free identity of a persisted store: enough for
// `doctor` to report format version and chunk count without loading gigabytes.
type StoreHeader struct {
	Version uint32
	Dim     int
	Count   int
}

// PeekStore reads only a store's header (magic/version/dim/count) — O(1), no
// vectors — so a checker can report what's on disk without mmapping the whole file.
func PeekStore(path string) (StoreHeader, error) {
	f, err := os.Open(path)
	if err != nil {
		return StoreHeader{}, err
	}
	defer f.Close()

	var hdr [16]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return StoreHeader{}, err
	}
	if string(hdr[0:4]) != storeMagic4 {
		return StoreHeader{}, fmt.Errorf("embed: bad magic %q", hdr[0:4])
	}
	le := binary.LittleEndian
	return StoreHeader{
		Version: le.Uint32(hdr[4:8]),
		Dim:     int(le.Uint32(hdr[8:12])),
		Count:   int(le.Uint32(hdr[12:16])),
	}, nil
}

// parseStoreHeader validates every section against size before any offset is
// used to slice. Same safety boundary as the token index's parseHeader.
func parseStoreHeader(b []byte, size uint64) (storeHeader, error) {
	var h storeHeader
	if uint64(len(b)) < storeHeaderSize {
		return h, fmt.Errorf("embed: file too small (%d bytes)", len(b))
	}
	if string(b[:4]) != storeMagic4 {
		return h, fmt.Errorf("embed: bad magic %q", b[:4])
	}
	le := binary.LittleEndian
	if v := le.Uint32(b[4:]); v != storeVersion {
		return h, fmt.Errorf("%w: version %d", ErrLegacyFormat, v)
	}
	h = storeHeader{
		dim:       le.Uint32(b[8:]),
		count:     le.Uint32(b[12:]),
		quant:     b[16],
		chunksOff: le.Uint64(b[24:]),
		keysOff:   le.Uint64(b[32:]),
		vecOff:    le.Uint64(b[40:]),
		scaleOff:  le.Uint64(b[48:]),
		fileLen:   le.Uint64(b[56:]),
	}
	if h.fileLen != size {
		return h, fmt.Errorf("embed: header says %d bytes, file is %d", h.fileLen, size)
	}
	if h.dim > maxStoreDim {
		return h, fmt.Errorf("embed: corrupt header: dim %d exceeds %d", h.dim, maxStoreDim)
	}
	if h.count > maxStoreCount {
		return h, fmt.Errorf("embed: corrupt header: count %d exceeds %d", h.count, maxStoreCount)
	}
	if h.quant != quantF32 && h.quant != quantInt8 {
		return h, fmt.Errorf("embed: unknown quantization %d", h.quant)
	}
	if h.vecOff%vecAlign != 0 {
		return h, fmt.Errorf("embed: vector block at %d is not %d-byte aligned", h.vecOff, vecAlign)
	}
	if h.quant == quantInt8 && h.scaleOff%scaleAlign != 0 {
		return h, fmt.Errorf("embed: scale block at %d is not %d-byte aligned", h.scaleOff, scaleAlign)
	}
	n := uint64(h.count)
	d := uint64(h.dim)
	vecBytes := n * d * 4
	if h.quant == quantInt8 {
		vecBytes = n * d
	}
	sections := []struct {
		name string
		off  uint64
		n    uint64
	}{
		{"chunks", h.chunksOff, n * chunkRecordSize},
		{"keys", h.keysOff, n * keyRecordSize},
		{"vec", h.vecOff, vecBytes},
	}
	if h.quant == quantInt8 {
		sections = append(sections, struct {
			name string
			off  uint64
			n    uint64
		}{"scale", h.scaleOff, n * 4})
	}
	for _, s := range sections {
		end := s.off + s.n
		if s.off < storeHeaderSize || end < s.off || end > size {
			return h, fmt.Errorf("embed: section %s [%d,%d) out of bounds for %d-byte file", s.name, s.off, end, size)
		}
	}
	return h, nil
}

// LoadStore maps path and returns a store whose vector block aliases the
// mapping. On the reference corpus that moves 2.93 GB off the Go heap.
//
// The caller MUST Close the returned store.
func LoadStore(path string) (*Store, error) {
	m, err := mmapslice.Open(path)
	if err != nil {
		return nil, err
	}
	b := m.Bytes()
	h, err := parseStoreHeader(b, uint64(len(b)))
	if err != nil {
		m.Close()
		return nil, err
	}
	s, err := storeFrom(h, b, m)
	if err != nil {
		m.Close()
		return nil, err
	}
	return s, nil
}

// storeFrom decodes a validated header + backing bytes into a Store. Chunks
// and keys decode eagerly into slices; only the vector block is a candidate
// for aliasing (Task 10 changes only how vec is obtained here). mm is the
// mapping that owns b's lifetime, or nil when b is a heap-owned []byte (e.g.
// from os.ReadFile) that needs no separate release.
func storeFrom(h storeHeader, b []byte, mm io.Closer) (*Store, error) {
	count := int(h.count)
	le := binary.LittleEndian

	s := &Store{dim: int(h.dim), quant: h.quant, mm: mm}
	if count == 0 {
		return s, nil
	}

	s.chunks = make([]Chunk, count)
	for i := 0; i < count; i++ {
		r := b[h.chunksOff+uint64(i)*chunkRecordSize:]
		s.chunks[i] = Chunk{
			Blob:      le.Uint64(r[0:]),
			StartLine: int(le.Uint32(r[8:])),
			EndLine:   int(le.Uint32(r[12:])),
			StartByte: int(le.Uint64(r[16:])),
			EndByte:   int(le.Uint64(r[24:])),
		}
	}

	s.keys = make([]ChunkKey, count)
	for i := 0; i < count; i++ {
		copy(s.keys[i][:], b[h.keysOff+uint64(i)*keyRecordSize:])
	}

	dim := int(h.dim)
	if h.quant == quantInt8 {
		if mm != nil {
			vecI8, err := mmapslice.Int8s(b[h.vecOff:], count*dim)
			if err != nil {
				return nil, err
			}
			s.vecI8 = vecI8
			scales, err := mmapslice.Float32s(b[h.scaleOff:], count)
			if err != nil {
				return nil, err
			}
			s.scales = scales
		} else {
			s.vecI8 = make([]int8, count*dim)
			for i := range s.vecI8 {
				s.vecI8[i] = int8(b[h.vecOff+uint64(i)])
			}
			s.scales = make([]float32, count)
			for i := range s.scales {
				s.scales[i] = math.Float32frombits(le.Uint32(b[h.scaleOff+uint64(i)*4:]))
			}
		}
	} else if mm != nil {
		vec, err := mmapslice.Float32s(b[h.vecOff:], count*dim)
		if err != nil {
			return nil, err
		}
		s.vec = vec
	} else {
		s.vec = make([]float32, count*dim)
		for i := range s.vec {
			s.vec[i] = math.Float32frombits(le.Uint32(b[h.vecOff+uint64(i)*4:]))
		}
	}

	if err := s.checkVecLen(); err != nil {
		return nil, err
	}
	return s, nil
}
