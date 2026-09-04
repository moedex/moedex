package tokenindex

import (
	"bytes"
	"sort"
)

// PostingList is one term's posting range, resolved once. Callers that need a
// term's frequencies for many blobs should resolve the term with Postings and
// then use this handle: the alternative, calling TermFreq per (term, blob)
// pair, re-runs the term binary search every single time.
//
// The slices alias the index's storage and must not be modified.
type PostingList struct {
	blobs []uint32
	tfs   []uint32
}

// Len is the term's document frequency.
func (p PostingList) Len() int { return len(p.blobs) }

// Blob returns the i'th blob ID. Blob IDs ascend with i.
func (p PostingList) Blob(i int) uint64 { return uint64(p.blobs[i]) }

// TF returns the term frequency of the i'th posting.
func (p PostingList) TF(i int) int { return int(p.tfs[i]) }

// TFOf returns the term frequency in blob, or 0 if the term does not occur
// there. O(log df) — prefer a merge walk over Blob/TF when scanning an
// already-sorted candidate set.
func (p PostingList) TFOf(blob uint64) int {
	if blob > 0xFFFFFFFF {
		return 0
	}
	want := uint32(blob)
	i := sort.Search(len(p.blobs), func(i int) bool { return p.blobs[i] >= want })
	if i < len(p.blobs) && p.blobs[i] == want {
		return int(p.tfs[i])
	}
	return 0
}

// termBytes returns term i's raw bytes. They alias termText; do not modify.
func (ti *TokenIndex) termBytes(i int) []byte {
	return ti.termText[ti.termOff[i]:ti.termOff[i+1]]
}

// termIndex binary-searches the sorted term dictionary, returning -1 if absent.
func (ti *TokenIndex) termIndex(term string) int {
	n := len(ti.termOff) - 1
	if n <= 0 {
		return -1
	}
	// One allocation per lookup, not one per probe: bytes.Compare against the
	// stored bytes avoids materialising a string for each of the ~24 probes a
	// 17.6M-term dictionary needs.
	want := []byte(term)
	i := sort.Search(n, func(i int) bool { return bytes.Compare(ti.termBytes(i), want) >= 0 })
	if i < n && bytes.Equal(ti.termBytes(i), want) {
		return i
	}
	return -1
}

// Postings resolves term to its posting range. An absent term yields a zero
// PostingList, whose Len is 0 and whose TFOf always returns 0.
func (ti *TokenIndex) Postings(term string) PostingList {
	i := ti.termIndex(term)
	if i < 0 {
		return PostingList{}
	}
	lo, hi := ti.postOff[i], ti.postOff[i+1]
	return PostingList{blobs: ti.postBlob[lo:hi], tfs: ti.postTF[lo:hi]}
}
