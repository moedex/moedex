// Package index holds moedex's in-memory positional-trigram index.
//
// Slice 1 scope: prove the retrieval kernel is correct, not fast. The document
// model is content-addressed (keyed by git blob SHA) so that dedup and delta
// indexing stay first-class, and distribution remains a future seam — but slice
// 1 pays nothing for either. Offsets are 64-bit byte offsets (since slice 4,
// content is stored and indexed as bytes, aligning with ripgrep).
package index

import (
	"sort"

	"moedex/internal/trigram"
)

// FileRef names one place a blob's content appears. Multiple refs per blob is
// the content-dedup win: identical content is indexed once, paths accumulate.
type FileRef struct {
	Repo    string
	RelPath string
	AbsPath string
}

// Posting records that a trigram begins at Offset (byte index) within Blob.
type Posting struct {
	Blob   uint64
	Offset int
}

// Blob is a unit of unique content, identified by its git blob SHA.
type Blob struct {
	ID      uint64
	SHA     string
	Content []byte
	Files   []FileRef

	lineStarts []int // byte offset of the first byte of each line
}

// LineOf returns the 1-based line number containing the given byte offset.
func (b *Blob) LineOf(off int) int {
	i := sort.Search(len(b.lineStarts), func(i int) bool { return b.lineStarts[i] > off }) - 1
	if i < 0 {
		i = 0
	}
	return i + 1
}

// Index is a content-addressed trigram index. Postings are served either from
// an in-RAM map (the builder path) or, when pp is set, on demand from a
// PostingProvider such as an mmap-backed loader (see RestoreLazy).
type Index struct {
	blobs    []*Blob
	bySHA    map[string]uint64
	postings map[trigram.Trigram][]Posting
	pp       PostingProvider
}

// New returns an empty index.
func New() *Index {
	return &Index{
		bySHA:    map[string]uint64{},
		postings: map[trigram.Trigram][]Posting{},
	}
}

// AddFile indexes content identified by sha under (repo, rel, abs). Files
// sharing a SHA are deduplicated: the content is indexed once and the new
// FileRef is appended to the existing blob.
func (ix *Index) AddFile(repo, rel, abs, sha string, content []byte) {
	ref := FileRef{Repo: repo, RelPath: rel, AbsPath: abs}
	if id, ok := ix.bySHA[sha]; ok {
		ix.blobs[id].Files = append(ix.blobs[id].Files, ref)
		return
	}

	id := uint64(len(ix.blobs))
	// Copy content so the index owns it independently of the caller's buffer.
	buf := append([]byte(nil), content...)
	b := &Blob{
		ID:         id,
		SHA:        sha,
		Content:    buf,
		Files:      []FileRef{ref},
		lineStarts: lineStarts(buf),
	}
	ix.blobs = append(ix.blobs, b)
	ix.bySHA[sha] = id

	// Positional byte-trigrams. Because blobs are added in increasing ID order
	// and a blob's trigrams are emitted in increasing offset order, each posting
	// list stays sorted by (Blob, Offset) without an explicit sort.
	for i := 0; i+trigram.N <= len(buf); i++ {
		t := trigram.Trigram{buf[i], buf[i+1], buf[i+2]}
		ix.postings[t] = append(ix.postings[t], Posting{Blob: id, Offset: i})
	}
}

func lineStarts(content []byte) []int {
	starts := []int{0}
	for i, c := range content {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// NumBlobs returns the number of distinct blobs indexed.
func (ix *Index) NumBlobs() int { return len(ix.blobs) }

// Blob returns the blob with the given id, or nil if id is out of range.
func (ix *Index) Blob(id uint64) *Blob {
	if id >= uint64(len(ix.blobs)) {
		return nil
	}
	return ix.blobs[id]
}

// Postings returns the (sorted) posting list for a trigram, or nil. When the
// index is lazily loaded, the list is decoded from the provider on demand.
func (ix *Index) Postings(t trigram.Trigram) []Posting {
	if ix.pp != nil {
		return ix.pp.Postings(t)
	}
	return ix.postings[t]
}
