package index

import (
	"encoding/binary"
	"math"
	"reflect"
	"testing"
)

func TestPostingsCodecRoundTrip(t *testing.T) {
	cases := [][]Posting{
		nil,
		{{Blob: 0, Offset: 0}},
		{{Blob: 0, Offset: 0}, {Blob: 0, Offset: 1}, {Blob: 0, Offset: 100}},
		{{Blob: 0, Offset: 5}, {Blob: 3, Offset: 0}, {Blob: 3, Offset: 9}, {Blob: 250, Offset: 1 << 20}},
		// many blobs, single posting each
		func() []Posting {
			var ps []Posting
			for b := uint64(0); b < 1000; b++ {
				ps = append(ps, Posting{Blob: b, Offset: int(b * 7)})
			}
			return ps
		}(),
	}
	for i, ps := range cases {
		got := DecodePostings(EncodePostings(ps))
		if len(ps) == 0 && len(got) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, ps) {
			t.Errorf("case %d: round-trip mismatch\n want %v\n  got %v", i, ps, got)
		}
	}
}

// TestDecodePostingsRejectsOffsetOverflow guards against int(prevOff)
// silently wrapping. prevOff accumulates from untrusted mmap'd varint deltas;
// a crafted/corrupt buffer with an offset beyond math.MaxInt must not produce
// a wrapped (e.g. negative) Offset that would later drive an out-of-bounds
// slice in Blob.LineAt. Like other malformed-input cases in DecodePostings,
// it should stop and return what was decoded so far rather than fabricate a
// bad posting.
func TestDecodePostingsRejectsOffsetOverflow(t *testing.T) {
	buf := make([]byte, 0, 32)
	tmp := make([]byte, binary.MaxVarintLen64)
	put := func(v uint64) {
		n := binary.PutUvarint(tmp, v)
		buf = append(buf, tmp[:n]...)
	}
	put(0)                        // blob delta -> blob 0
	put(1)                        // count: one posting in this group
	put(uint64(math.MaxInt) + 1) // offset delta overflows int

	got := DecodePostings(buf)
	for _, p := range got {
		if p.Offset < 0 {
			t.Fatalf("DecodePostings produced wrapped negative Offset %d from overflowing input", p.Offset)
		}
	}
	if len(got) != 0 {
		t.Fatalf("DecodePostings with overflowing offset = %v, want no postings decoded from the malformed group", got)
	}
}

func TestPostingsCodecCompresses(t *testing.T) {
	// One posting per rune of a dense blob: deltas are 1, so each should encode
	// to roughly a single byte versus 16 in the naive representation.
	var ps []Posting
	for off := 0; off < 10000; off++ {
		ps = append(ps, Posting{Blob: 42, Offset: off})
	}
	enc := EncodePostings(ps)
	naive := len(ps) * 16
	if len(enc) >= naive/4 {
		t.Errorf("expected >4x compression vs naive %d bytes, got %d", naive, len(enc))
	}
}
