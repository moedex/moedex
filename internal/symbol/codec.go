package symbol

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
)

// Binary sidecar format (all multi-byte integers little-endian via
// binary.PutUvarint / Uvarint, i.e. variable-length unsigned for compactness):
//
//	magic     : 4 bytes  "SYM1"
//	blobCount : uvarint                 (number of blobs with symbols)
//	repeated per blob (ascending blob ID):
//	    blobID   : uvarint
//	    symCount : uvarint
//	    repeated per symbol (in stored, BodyStart-sorted order):
//	        nameLen   : uvarint, name bytes
//	        kind      : uvarint          (Kind, 1 byte's worth of range)
//	        nameStart : uvarint
//	        nameEnd   : uvarint
//	        bodyStart : uvarint
//	        bodyEnd   : uvarint
//
// Offsets are non-negative byte offsets, so uvarint is safe. The format is
// self-describing and fully round-trippable: Load reconstructs every Symbol
// field Save wrote, in the same order, so Enclosing yields identical results
// before and after a round trip.
var magic = []byte("SYM1")

// Save writes ix to path.
func Save(ix *Index, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	if err := writeIndex(w, ix); err != nil {
		f.Close()
		return err
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func writeIndex(w io.Writer, ix *Index) error {
	if _, err := w.Write(magic); err != nil {
		return err
	}
	var buf [binary.MaxVarintLen64]byte
	putU := func(v uint64) error {
		n := binary.PutUvarint(buf[:], v)
		_, err := w.Write(buf[:n])
		return err
	}

	// Deterministic blob order so the output is stable.
	ids := make([]uint64, 0, len(ix.byBlob))
	for id := range ix.byBlob {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	if err := putU(uint64(len(ids))); err != nil {
		return err
	}
	for _, id := range ids {
		syms := ix.byBlob[id]
		if err := putU(id); err != nil {
			return err
		}
		if err := putU(uint64(len(syms))); err != nil {
			return err
		}
		for _, s := range syms {
			if err := putU(uint64(len(s.Name))); err != nil {
				return err
			}
			if _, err := io.WriteString(w, s.Name); err != nil {
				return err
			}
			if err := putU(uint64(s.Kind)); err != nil {
				return err
			}
			if err := putU(uint64(s.NameStart)); err != nil {
				return err
			}
			if err := putU(uint64(s.NameEnd)); err != nil {
				return err
			}
			if err := putU(uint64(s.BodyStart)); err != nil {
				return err
			}
			if err := putU(uint64(s.BodyEnd)); err != nil {
				return err
			}
		}
	}
	return nil
}

// Load reads an Index previously written by Save.
func Load(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	return readIndex(r)
}

func readIndex(r *bufio.Reader) (*Index, error) {
	hdr := make([]byte, len(magic))
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, fmt.Errorf("symbol: reading magic: %w", err)
	}
	if string(hdr) != string(magic) {
		return nil, fmt.Errorf("symbol: bad magic %q", hdr)
	}

	getU := func() (uint64, error) { return binary.ReadUvarint(r) }

	ix := NewIndex()
	blobCount, err := getU()
	if err != nil {
		return nil, err
	}
	for i := uint64(0); i < blobCount; i++ {
		id, err := getU()
		if err != nil {
			return nil, err
		}
		symCount, err := getU()
		if err != nil {
			return nil, err
		}
		syms := make([]Symbol, 0, symCount)
		for j := uint64(0); j < symCount; j++ {
			nameLen, err := getU()
			if err != nil {
				return nil, err
			}
			nb := make([]byte, nameLen)
			if _, err := io.ReadFull(r, nb); err != nil {
				return nil, err
			}
			kind, err := getU()
			if err != nil {
				return nil, err
			}
			ns, err := getU()
			if err != nil {
				return nil, err
			}
			ne, err := getU()
			if err != nil {
				return nil, err
			}
			bs, err := getU()
			if err != nil {
				return nil, err
			}
			be, err := getU()
			if err != nil {
				return nil, err
			}
			syms = append(syms, Symbol{
				Name:      string(nb),
				Kind:      Kind(kind),
				NameStart: int(ns),
				NameEnd:   int(ne),
				BodyStart: int(bs),
				BodyEnd:   int(be),
			})
		}
		// Stored order is already BodyStart-sorted; install directly to preserve
		// it (Set would re-sort identically, but this keeps Load independent of
		// Set's tie-break).
		ix.byBlob[id] = syms
	}
	return ix, nil
}
