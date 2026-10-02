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
//	magic     : 4 bytes  "SYM2"
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
//	refBlobCount : uvarint              (number of blobs with REFERENCE occs)
//	repeated per blob (ascending blob ID):
//	    blobID   : uvarint
//	    refCount : uvarint
//	    repeated per reference occurrence (in stored, Start-sorted order):
//	        nameLen   : uvarint, name bytes
//	        kind      : uvarint
//	        role      : uvarint          (Role; always Reference in this section)
//	        start     : uvarint
//	        end       : uvarint
//
// Offsets are non-negative byte offsets, so uvarint is safe. The format is
// self-describing and fully round-trippable: Load reconstructs every Symbol and
// reference Occurrence Save wrote, so Enclosing/References/Definitions yield
// identical results before and after a round trip.
//
// Backward compatibility: a legacy "SYM1" sidecar (no references section) loads
// cleanly with zero references — readIndex stops after the symbols section when
// it sees the SYM1 magic. New writes always use SYM2; the server's .meta
// freshness sidecar forces a rebuild on any corpus change, so a stale SYM1 file
// is rewritten as SYM2 on the next BuildSidecars regardless.
var magic = []byte("SYM2")

// magicV1 is the legacy symbols-only sidecar magic, still readable.
var magicV1 = []byte("SYM1")

// Encode writes the current symbol format without taking ownership of w.
func Encode(w io.Writer, ix *Index) error { return writeIndex(w, ix) }

// Decode reads exactly size bytes in the current format. Unlike Load's legacy
// compatibility path, this rejects older formats, trailing bytes, invalid enums,
// offsets, duplicate blob records, and noncanonical record ordering. Callers
// must additionally validate offsets and blob IDs against their source corpus.
func Decode(r io.Reader, size int64) (*Index, error) {
	if size < 0 {
		return nil, fmt.Errorf("symbol: negative size")
	}
	limited := &io.LimitedReader{R: r, N: size}
	ix, err := readIndexChecked(bufio.NewReader(limited), size, true)
	if err == nil && limited.N != 0 {
		return nil, io.ErrUnexpectedEOF
	}
	return ix, err
}

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

	// References section (SYM2). Deterministic blob order.
	refIDs := make([]uint64, 0, len(ix.refsByBlob))
	for id := range ix.refsByBlob {
		refIDs = append(refIDs, id)
	}
	sort.Slice(refIDs, func(i, j int) bool { return refIDs[i] < refIDs[j] })

	if err := putU(uint64(len(refIDs))); err != nil {
		return err
	}
	for _, id := range refIDs {
		occs := ix.refsByBlob[id]
		if err := putU(id); err != nil {
			return err
		}
		if err := putU(uint64(len(occs))); err != nil {
			return err
		}
		for _, o := range occs {
			if err := putU(uint64(len(o.Name))); err != nil {
				return err
			}
			if _, err := io.WriteString(w, o.Name); err != nil {
				return err
			}
			if err := putU(uint64(o.Kind)); err != nil {
				return err
			}
			if err := putU(uint64(o.Role)); err != nil {
				return err
			}
			if err := putU(uint64(o.Start)); err != nil {
				return err
			}
			if err := putU(uint64(o.End)); err != nil {
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
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	r := bufio.NewReader(f)
	return readIndex(r, info.Size())
}

// minBlobHeaderSize is the smallest possible on-disk encoding of one
// per-blob record in the symbols section: blobID + symCount, each a minimum
// 1-byte uvarint (a blob with zero symbols is a valid record).
const minBlobHeaderSize = 1 + 1

// minSymbolRecordSize is the smallest possible on-disk encoding of one
// Symbol: nameLen (permitting a zero-length name) + kind + nameStart +
// nameEnd + bodyStart + bodyEnd, each a minimum 1-byte uvarint.
const minSymbolRecordSize = 1 + 1 + 1 + 1 + 1 + 1

// minOccurrenceRecordSize is the analogous minimum for one reference
// Occurrence: nameLen + kind + role + start + end.
const minOccurrenceRecordSize = 1 + 1 + 1 + 1 + 1

// checkCount rejects an untrusted element count or byte length read from a
// SYM1/SYM2 sidecar before a caller does an eager make([]T, 0, n) or
// make([]byte, n) sized off it. minItemSize is the smallest possible
// on-disk encoding of one element (1 for a raw byte length such as
// nameLen): n elements of at least minItemSize bytes each could never fit
// inside a file of size total bytes, so a count exceeding that bound is
// corrupt. Without this, a corrupted count field near the uint64 max drives
// a multi-GB allocation attempt before the per-item bounds-checked reads
// below ever get a chance to fail cleanly on EOF — reproduced as a
// panic-worthy allocation that would crash a live-serving daemon on SIGHUP
// reload, since the only recover() in internal/app/servecmd wraps HTTP handlers,
// not the reload goroutine.
//
// This is necessarily a coarser bound than diskstore's checkCount (which
// checks against bytes remaining in a fully-buffered slice): readIndex
// consumes a streaming bufio.Reader with no cheap way to know how many bytes
// remain at the current position, so it is checked against the whole file's
// size instead. That is still a valid — if slightly loose — bound, because
// no section can ever hold more than the entire file, and it is more than
// enough to reject the wildly-inflated counts this guards against.
func checkCount(n uint64, minItemSize int, total int64) error {
	if total < 0 {
		total = 0
	}
	if n > uint64(total)/uint64(minItemSize) {
		return fmt.Errorf("count %d exceeds file size (%d bytes)", n, total)
	}
	return nil
}

func readIndex(r *bufio.Reader, size int64) (*Index, error) {
	return readIndexChecked(r, size, false)
}

func readIndexChecked(r *bufio.Reader, size int64, strict bool) (*Index, error) {
	hdr := make([]byte, len(magic))
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, fmt.Errorf("symbol: reading magic: %w", err)
	}
	isV2 := string(hdr) == string(magic)
	isV1 := string(hdr) == string(magicV1)
	if strict && !isV2 {
		return nil, fmt.Errorf("symbol: current format required")
	}
	if !isV2 && !isV1 {
		return nil, fmt.Errorf("symbol: bad magic %q", hdr)
	}

	getU := func() (uint64, error) { return binary.ReadUvarint(r) }

	ix := NewIndex()
	blobCount, err := getU()
	if err != nil {
		return nil, err
	}
	if err := checkCount(blobCount, minBlobHeaderSize, size); err != nil {
		return nil, fmt.Errorf("symbol: blobCount: %w", err)
	}
	for i := uint64(0); i < blobCount; i++ {
		id, err := getU()
		if err != nil {
			return nil, err
		}
		if strict {
			if _, exists := ix.byBlob[id]; exists {
				return nil, fmt.Errorf("symbol: duplicate blob %d", id)
			}
		}
		symCount, err := getU()
		if err != nil {
			return nil, err
		}
		if err := checkCount(symCount, minSymbolRecordSize, size); err != nil {
			return nil, fmt.Errorf("symbol: blob %d symCount: %w", id, err)
		}
		capacity := symCount
		if strict {
			capacity = 0 // Never reserve memory from an untrusted cache count.
		}
		syms := make([]Symbol, 0, capacity)
		for j := uint64(0); j < symCount; j++ {
			nameLen, err := getU()
			if err != nil {
				return nil, err
			}
			if err := checkCount(nameLen, 1, size); err != nil {
				return nil, fmt.Errorf("symbol: blob %d symbol %d nameLen: %w", id, j, err)
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
			if strict {
				maxInt := uint64(^uint(0) >> 1)
				if kind > uint64(Service) || ns > ne || ne > maxInt || bs > be || be > maxInt {
					return nil, fmt.Errorf("symbol: invalid symbol range or kind")
				}
				if len(syms) > 0 {
					prev := syms[len(syms)-1]
					if bs < uint64(prev.BodyStart) || bs == uint64(prev.BodyStart) && be > uint64(prev.BodyEnd) {
						return nil, fmt.Errorf("symbol: unordered symbols")
					}
				}
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

	// References section (SYM2 only). A SYM1 file ends after the symbols section,
	// so no refs are read and References/Definitions surface definitions only.
	if isV2 {
		refBlobCount, err := getU()
		if err != nil {
			return nil, err
		}
		if strict {
			if err := checkCount(refBlobCount, minBlobHeaderSize, size); err != nil {
				return nil, err
			}
		}
		for i := uint64(0); i < refBlobCount; i++ {
			id, err := getU()
			if err != nil {
				return nil, err
			}
			if strict {
				if _, exists := ix.refsByBlob[id]; exists {
					return nil, fmt.Errorf("symbol: duplicate reference blob %d", id)
				}
			}
			refCount, err := getU()
			if err != nil {
				return nil, err
			}
			if err := checkCount(refCount, minOccurrenceRecordSize, size); err != nil {
				return nil, fmt.Errorf("symbol: ref blob %d refCount: %w", id, err)
			}
			capacity := refCount
			if strict {
				capacity = 0
			}
			occs := make([]Occurrence, 0, capacity)
			for j := uint64(0); j < refCount; j++ {
				nameLen, err := getU()
				if err != nil {
					return nil, err
				}
				if err := checkCount(nameLen, 1, size); err != nil {
					return nil, fmt.Errorf("symbol: ref blob %d occurrence %d nameLen: %w", id, j, err)
				}
				nb := make([]byte, nameLen)
				if _, err := io.ReadFull(r, nb); err != nil {
					return nil, err
				}
				kind, err := getU()
				if err != nil {
					return nil, err
				}
				role, err := getU()
				if err != nil {
					return nil, err
				}
				start, err := getU()
				if err != nil {
					return nil, err
				}
				end, err := getU()
				if err != nil {
					return nil, err
				}
				if strict && (kind > uint64(Service) || role != uint64(Reference) || start > end || end > uint64(^uint(0)>>1) || len(occs) > 0 && start < uint64(occs[len(occs)-1].Start)) {
					return nil, fmt.Errorf("symbol: invalid reference range, kind, role or order")
				}
				occs = append(occs, Occurrence{
					Name:  string(nb),
					Kind:  Kind(kind),
					Role:  Role(role),
					Start: int(start),
					End:   int(end),
				})
			}
			ix.refsByBlob[id] = occs
		}
	}
	if strict {
		if _, err := r.ReadByte(); err != io.EOF {
			return nil, fmt.Errorf("symbol: trailing bytes or read error: %v", err)
		}
	}

	// Rebuild the byName inverted view for every blob touched by the load
	// (definitions + references) so References/Definitions work post-load. Done
	// once here rather than via Set/SetRefs to keep the stored BodyStart order
	// untouched.
	ix.rebuildNames()
	return ix, nil
}
