package embed

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
)

// On-disk format (little-endian, encoding/binary):
//
//	magic    [4]byte = "MDXE"
//	version  uint32  = 2
//	dim      uint32
//	count    uint32             // number of chunks
//	repeat count times:
//	  blob      uint64
//	  startLine uint32
//	  endLine   uint32
//	  startByte uint64
//	  endByte   uint64
//	(v2 only) repeat count times:
//	  key       [16]byte         // content key = sha256(chunk text)[:16]
//	repeat count times:
//	  dim float32 values        // the normalized vector for chunk i
//
// Vectors are stored as a contiguous block after the chunk metadata so a future
// loader can mmap/stream them; here we just read sequentially. Round-trippable.
//
// Version history:
//   - v1: chunk metadata + vectors (no keys). Still loadable; such a store cannot
//     seed incremental reuse until its keys are filled (see Store.FillKeys).
//   - v2: adds a content key per chunk, between the chunk metadata and the vectors,
//     enabling incremental rebuilds that re-embed only changed chunks.

var storeMagic = [4]byte{'M', 'D', 'X', 'E'}

const storeVersion uint32 = 2

// Save writes the store (chunks + content keys + vectors) to path in format v2.
// A non-empty store MUST carry a content key per chunk (HasKeys); Save refuses an
// inconsistent store rather than persisting one that cannot seed incremental reuse.
func (s *Store) Save(path string) error {
	if len(s.chunks) > 0 && !s.HasKeys() {
		return fmt.Errorf("embed: refusing to save store without content keys (%d chunks, %d keys); call FillKeys first", len(s.chunks), len(s.keys))
	}
	// Write to a temp sibling and atomically rename over path: a crash or torn
	// write must never leave a truncated multi-GB store where a valid one was — the
	// daemon would load that corrupt sidecar on its next reload and silently drop to
	// lexical. Rename is atomic within a filesystem; the temp sits in the same dir so
	// it shares path's filesystem. The original is untouched until the rename.
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

	w := bufio.NewWriter(f)
	le := binary.LittleEndian

	if _, err := w.Write(storeMagic[:]); err != nil {
		return err
	}
	var u32 [4]byte
	le.PutUint32(u32[:], storeVersion)
	if _, err := w.Write(u32[:]); err != nil {
		return err
	}
	le.PutUint32(u32[:], uint32(s.dim))
	if _, err := w.Write(u32[:]); err != nil {
		return err
	}
	le.PutUint32(u32[:], uint32(len(s.chunks)))
	if _, err := w.Write(u32[:]); err != nil {
		return err
	}

	var u64 [8]byte
	for _, c := range s.chunks {
		le.PutUint64(u64[:], c.Blob)
		if _, err := w.Write(u64[:]); err != nil {
			return err
		}
		le.PutUint32(u32[:], uint32(c.StartLine))
		if _, err := w.Write(u32[:]); err != nil {
			return err
		}
		le.PutUint32(u32[:], uint32(c.EndLine))
		if _, err := w.Write(u32[:]); err != nil {
			return err
		}
		le.PutUint64(u64[:], uint64(c.StartByte))
		if _, err := w.Write(u64[:]); err != nil {
			return err
		}
		le.PutUint64(u64[:], uint64(c.EndByte))
		if _, err := w.Write(u64[:]); err != nil {
			return err
		}
	}

	// v2 key block: one content key per chunk, in chunk order.
	for _, k := range s.keys {
		if _, err := w.Write(k[:]); err != nil {
			return err
		}
	}

	// s.vec is exactly len(chunks)*dim by construction (see vecAt), so it already
	// writes out as len(chunks) consecutive dim-float32 vectors in chunk order.
	for _, x := range s.vec {
		le.PutUint32(u32[:], math.Float32bits(x))
		if _, err := w.Write(u32[:]); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
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
	if [4]byte{hdr[0], hdr[1], hdr[2], hdr[3]} != storeMagic {
		return StoreHeader{}, fmt.Errorf("embed: bad magic %q", hdr[0:4])
	}
	le := binary.LittleEndian
	return StoreHeader{
		Version: le.Uint32(hdr[4:8]),
		Dim:     int(le.Uint32(hdr[8:12])),
		Count:   int(le.Uint32(hdr[12:16])),
	}, nil
}

// LoadStore reads a Store previously written by Save.
func LoadStore(path string) (*Store, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := bufio.NewReader(f)
	le := binary.LittleEndian

	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil {
		return nil, err
	}
	if magic != storeMagic {
		return nil, fmt.Errorf("embed: bad magic %q", magic[:])
	}

	var u32 [4]byte
	if _, err := io.ReadFull(r, u32[:]); err != nil {
		return nil, err
	}
	version := le.Uint32(u32[:])
	if version != 1 && version != storeVersion {
		return nil, fmt.Errorf("embed: unsupported version %d", version)
	}
	if _, err := io.ReadFull(r, u32[:]); err != nil {
		return nil, err
	}
	dim := int(le.Uint32(u32[:]))
	if _, err := io.ReadFull(r, u32[:]); err != nil {
		return nil, err
	}
	count := int(le.Uint32(u32[:]))

	s := &Store{dim: dim}
	if count == 0 {
		return s, nil
	}
	s.chunks = make([]Chunk, count)

	var u64 [8]byte
	for i := 0; i < count; i++ {
		if _, err := io.ReadFull(r, u64[:]); err != nil {
			return nil, err
		}
		blob := le.Uint64(u64[:])
		if _, err := io.ReadFull(r, u32[:]); err != nil {
			return nil, err
		}
		startLine := int(le.Uint32(u32[:]))
		if _, err := io.ReadFull(r, u32[:]); err != nil {
			return nil, err
		}
		endLine := int(le.Uint32(u32[:]))
		if _, err := io.ReadFull(r, u64[:]); err != nil {
			return nil, err
		}
		startByte := int(le.Uint64(u64[:]))
		if _, err := io.ReadFull(r, u64[:]); err != nil {
			return nil, err
		}
		endByte := int(le.Uint64(u64[:]))
		s.chunks[i] = Chunk{
			Blob:      blob,
			StartLine: startLine,
			EndLine:   endLine,
			StartByte: startByte,
			EndByte:   endByte,
		}
	}

	// v2 key block: one content key per chunk. A v1 store has none, so it loads with
	// keys=nil — it can still be served and searched, but must be re-keyed (FillKeys)
	// before it can seed or be re-saved by an incremental rebuild.
	if version >= 2 {
		s.keys = make([]ChunkKey, count)
		for i := 0; i < count; i++ {
			if _, err := io.ReadFull(r, s.keys[i][:]); err != nil {
				return nil, err
			}
		}
	}

	s.vec = make([]float32, count*dim)
	for i := range s.vec {
		if _, err := io.ReadFull(r, u32[:]); err != nil {
			return nil, err
		}
		s.vec[i] = math.Float32frombits(le.Uint32(u32[:]))
	}
	if err := s.checkVecLen(); err != nil {
		return nil, err
	}
	return s, nil
}
