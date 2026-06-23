package index

import "encoding/binary"

// Posting lists are the memory wall: stored naively they cost 16 bytes each
// (uint64 blob + int offset) and there is roughly one per rune of corpus. This
// codec compresses them with grouped varint delta coding so they can live on
// disk / in an mmap rather than the Go heap.
//
// A list is sorted by Blob ascending, then Offset ascending within each blob.
// We group by blob and write, per group:
//
//	uvarint(blob - prevBlob)   // blob-id delta; first group's prev is 0
//	uvarint(count)             // postings in this blob
//	uvarint(offset - prevOff)  // offset deltas, prevOff reset to 0 per group
//	...                        // count deltas
//
// Deltas are small (adjacent blob IDs, nearby offsets), so most encode to a
// single byte — typically 1-2 bytes per posting versus 16.

// EncodePostings serializes a sorted posting list to compact bytes.
func EncodePostings(ps []Posting) []byte {
	if len(ps) == 0 {
		return nil
	}
	buf := make([]byte, 0, len(ps)*2)
	tmp := make([]byte, binary.MaxVarintLen64)
	put := func(v uint64) {
		n := binary.PutUvarint(tmp, v)
		buf = append(buf, tmp[:n]...)
	}

	var prevBlob uint64
	for i := 0; i < len(ps); {
		blob := ps[i].Blob
		j := i
		for j < len(ps) && ps[j].Blob == blob {
			j++
		}
		put(blob - prevBlob)
		prevBlob = blob
		put(uint64(j - i))
		var prevOff uint64
		for k := i; k < j; k++ {
			off := uint64(ps[k].Offset)
			put(off - prevOff)
			prevOff = off
		}
		i = j
	}
	return buf
}

// DecodePostings reverses EncodePostings. The input must be exactly one list's
// bytes. A malformed buffer yields a nil list rather than a panic.
func DecodePostings(b []byte) []Posting {
	var out []Posting
	var prevBlob uint64
	pos := 0
	for pos < len(b) {
		d, n := binary.Uvarint(b[pos:])
		if n <= 0 {
			return out
		}
		pos += n
		blob := prevBlob + d
		prevBlob = blob

		count, n := binary.Uvarint(b[pos:])
		if n <= 0 {
			return out
		}
		pos += n

		var prevOff uint64
		for k := uint64(0); k < count; k++ {
			od, n := binary.Uvarint(b[pos:])
			if n <= 0 {
				return out
			}
			pos += n
			prevOff += od
			out = append(out, Posting{Blob: blob, Offset: int(prevOff)})
		}
	}
	return out
}
