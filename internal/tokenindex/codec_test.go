package tokenindex

import (
	"bytes"
	"encoding/binary"
	"slices"
	"testing"
)

// decodedLayout is the on-disk write order of a TKI1 blob: the order writeIndex
// actually emitted entries in, which a Go map range does not guarantee is
// reproducible across calls (the runtime randomizes range start). This is a
// thin re-decode of the format documented at the top of codec.go, kept
// separate from readIndex (which reconstructs maps and so discards the
// written order we need to assert on).
type decodedLayout struct {
	docLenBlobIDs []uint64
	terms         []string
	// postingsBlobIDs maps each term to its written blobID order.
	postingsBlobIDs map[string][]uint64
}

func decodeLayout(t *testing.T, data []byte) decodedLayout {
	t.Helper()
	r := bytes.NewReader(data)
	hdr := make([]byte, len(magic))
	if _, err := r.Read(hdr); err != nil {
		t.Fatalf("read magic: %v", err)
	}
	if !bytes.Equal(hdr, magic) {
		t.Fatalf("bad magic %q", hdr)
	}
	readU := func() uint64 {
		v, err := binary.ReadUvarint(r)
		if err != nil {
			t.Fatalf("read uvarint: %v", err)
		}
		return v
	}
	_ = readU() // numDocs
	_ = readU() // totalLen

	docCount := readU()
	var out decodedLayout
	out.postingsBlobIDs = make(map[string][]uint64)
	for range docCount {
		id := readU()
		_ = readU() // length
		out.docLenBlobIDs = append(out.docLenBlobIDs, id)
	}

	termCount := readU()
	for range termCount {
		termLen := readU()
		termBytes := make([]byte, termLen)
		if _, err := r.Read(termBytes); err != nil {
			t.Fatalf("read term: %v", err)
		}
		term := string(termBytes)
		out.terms = append(out.terms, term)
		df := readU()
		var blobs []uint64
		for range df {
			id := readU()
			_ = readU() // tf
			blobs = append(blobs, id)
		}
		out.postingsBlobIDs[term] = blobs
	}
	return out
}

// TestWriteIndex_DeterministicOrder proves writeIndex emits docLen and
// postings entries in a fixed (sorted) order rather than Go's randomized map
// range order, so the TKI1 bytes are reproducible across runs/builds for the
// same logical TokenIndex (build reproducibility / artifact diffing).
func TestWriteIndex_DeterministicOrder(t *testing.T) {
	ti := &TokenIndex{
		docLen: map[uint64]int{
			50: 3, 3: 7, 1000: 1, 7: 9, 12: 2, 999: 4,
		},
		postings: map[string]map[uint64]int{
			"zebra": {5: 1, 2: 3, 100: 1},
			"apple": {9: 2, 1: 1},
			"mango": {4: 1, 3: 2, 8: 1},
			"kiwi":  {1: 1},
		},
		numDocs:  6,
		totalLen: 26,
	}

	var buf1, buf2 bytes.Buffer
	if err := writeIndex(&buf1, ti); err != nil {
		t.Fatalf("writeIndex (1st): %v", err)
	}
	if err := writeIndex(&buf2, ti); err != nil {
		t.Fatalf("writeIndex (2nd): %v", err)
	}
	if !bytes.Equal(buf1.Bytes(), buf2.Bytes()) {
		t.Fatalf("writeIndex produced different bytes across two calls on the same TokenIndex")
	}

	got := decodeLayout(t, buf1.Bytes())

	wantDocIDs := slices.Clone(got.docLenBlobIDs)
	slices.Sort(wantDocIDs)
	if !slices.Equal(got.docLenBlobIDs, wantDocIDs) {
		t.Errorf("docLen blobIDs written as %v, want ascending %v", got.docLenBlobIDs, wantDocIDs)
	}

	wantTerms := slices.Clone(got.terms)
	slices.Sort(wantTerms)
	if !slices.Equal(got.terms, wantTerms) {
		t.Errorf("terms written as %v, want lexicographically ascending %v", got.terms, wantTerms)
	}

	for term, blobs := range got.postingsBlobIDs {
		want := slices.Clone(blobs)
		slices.Sort(want)
		if !slices.Equal(blobs, want) {
			t.Errorf("postings[%q] blobIDs written as %v, want ascending %v", term, blobs, want)
		}
	}
}
