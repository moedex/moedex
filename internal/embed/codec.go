package embed

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
)

// On-disk format (little-endian, encoding/binary):
//
//	magic    [4]byte = "MDXE"
//	version  uint32  = 1
//	dim      uint32
//	count    uint32             // number of chunks
//	repeat count times:
//	  blob      uint64
//	  startLine uint32
//	  endLine   uint32
//	  startByte uint64
//	  endByte   uint64
//	repeat count times:
//	  dim float32 values        // the normalized vector for chunk i
//
// Vectors are stored as a contiguous block after the chunk metadata so a future
// loader can mmap/stream them; here we just read sequentially. Round-trippable.

var storeMagic = [4]byte{'M', 'D', 'X', 'E'}

const storeVersion uint32 = 1

// Save writes the store (chunks + vectors) to path.
func (s *Store) Save(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

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

	for _, v := range s.vectors {
		if len(v) != s.dim {
			return fmt.Errorf("embed: vector dim %d != store dim %d", len(v), s.dim)
		}
		for _, x := range v {
			le.PutUint32(u32[:], math.Float32bits(x))
			if _, err := w.Write(u32[:]); err != nil {
				return err
			}
		}
	}
	return w.Flush()
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
	if v := le.Uint32(u32[:]); v != storeVersion {
		return nil, fmt.Errorf("embed: unsupported version %d", v)
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
	s.vectors = make([]Vector, count)

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

	for i := 0; i < count; i++ {
		v := make(Vector, dim)
		for j := 0; j < dim; j++ {
			if _, err := io.ReadFull(r, u32[:]); err != nil {
				return nil, err
			}
			v[j] = math.Float32frombits(le.Uint32(u32[:]))
		}
		s.vectors[i] = v
	}
	return s, nil
}
