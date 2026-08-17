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

// LineAt returns the 1-based line number containing byte offset off together
// with that line's bytes (excluding the trailing '\n'). The numbering matches
// LineOf, and the bounds match forEachLine's, so verifying a match at a posting
// offset yields the same (file, line) result a line scan would. off is assumed
// in range (trigram posting offsets always are).
func (b *Blob) LineAt(off int) (int, []byte) {
	i := sort.Search(len(b.lineStarts), func(i int) bool { return b.lineStarts[i] > off }) - 1
	if i < 0 {
		i = 0
	}
	start := b.lineStarts[i]
	end := len(b.Content)
	if i+1 < len(b.lineStarts) {
		end = b.lineStarts[i+1] - 1 // exclude the '\n' that begins the next line
	}
	return i + 1, b.Content[start:end]
}

// Index is a content-addressed trigram index. Postings are served either from
// an in-RAM map (the builder path) or, when pp is set, on demand from a
// PostingProvider such as an mmap-backed loader (see RestoreLazy).
//
// Membership (the selective-index hinge): the default (all-trigram) build
// materializes a posting list for EVERY trigram of the corpus, so a missing
// posting list provably means "zero occurrences" and is therefore a safe
// filter. A selective build (see Builder) deliberately omits some grams; for
// those, a missing posting list means "UNKNOWN", not zero, and the query layer
// must treat the gram as All (force-scan) instead of intersecting it to empty.
// The authoritative answer comes from IndexedGram: when selected==nil the index
// is all-trigram and IndexedGram is universally true at zero cost; when selected
// is set it is the exact keep-set the selector chose.
type Index struct {
	blobs    []*Blob
	bySHA    map[string]uint64
	postings map[trigram.Trigram][]Posting
	pp       PostingProvider
	// selected, when non-nil, is the authoritative set of grams this index
	// materialized (a selective build). nil means "all trigrams indexed" — the
	// default build and every legacy/back-compat shard. A lazily-loaded index
	// (pp != nil) defers IndexedGram to the provider when it implements
	// gramMember; otherwise pp-without-selected is treated as all-indexed.
	selected map[trigram.Trigram]struct{}
}

// gramMember is an optional PostingProvider capability: report whether a gram is
// a trustworthy filter (materialized). A lazily/mmap-loaded selective shard
// implements it so IndexedGram answers correctly without the postings on heap.
// A provider that does NOT implement it is treated as all-indexed (legacy
// shards, the default path), preserving back-compat.
type gramMember interface {
	IndexedGram(t trigram.Trigram) bool
}

// New returns an empty index.
func New() *Index {
	return &Index{
		bySHA:    map[string]uint64{},
		postings: map[trigram.Trigram][]Posting{},
	}
}

// IndexedGram reports whether trigram t is a trustworthy filter in this index —
// i.e. whether a query may rely on t's posting list (an empty list meaning
// "genuinely zero occurrences"). It returns:
//
//   - true  for the default (all-trigram) build: every length-3 byte sequence
//     is considered indexed, so a missing list is a real zero. This is the
//     universal-true fast path and costs nothing (selected==nil).
//   - true  for a selective build iff t is in the selector's keep-set.
//   - false for a deselected gram in a selective build: its postings are UNKNOWN
//     and the query layer must treat t as All (force-scan), never as zero.
//
// This is the single load-bearing safety primitive for selective indexing: the
// query/search layers consult it before ever reading a gram's postings as a
// filter, so a deselected gram can only ever WIDEN the candidate set.
func (ix *Index) IndexedGram(t trigram.Trigram) bool {
	if ix.selected != nil {
		_, ok := ix.selected[t]
		return ok
	}
	if ix.pp != nil {
		if gm, ok := ix.pp.(gramMember); ok {
			return gm.IndexedGram(t)
		}
	}
	// Default / legacy: all trigrams indexed.
	return true
}

// Selective reports whether this index omits some grams (a selective build). It
// is a cheap hint for callers that want to skip the per-gram IndexedGram check
// on the universal-true fast path. When false, IndexedGram is universally true.
func (ix *Index) Selective() bool {
	if ix.selected != nil {
		return true
	}
	if ix.pp != nil {
		if _, ok := ix.pp.(gramMember); ok {
			return true
		}
	}
	return false
}

// AddFile indexes content identified by sha under (repo, rel, abs). Files
// sharing a SHA are deduplicated: the content is indexed once and the new
// FileRef is appended to the existing blob.
func (ix *Index) AddFile(repo, rel, abs, sha string, content []byte) {
	ix.addFile(repo, rel, abs, sha, content, true)
}

// AddFileUnindexed stores a content-addressed blob and its file reference
// without building trigram postings. It is for derived offline passes that need
// Index's blob iteration contract (for example embed.BuildStore) but will never
// query the temporary index. Normal searchable indices must use AddFile.
func (ix *Index) AddFileUnindexed(repo, rel, abs, sha string, content []byte) {
	ix.addFile(repo, rel, abs, sha, content, false)
}

func (ix *Index) addFile(repo, rel, abs, sha string, content []byte, postings bool) {
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

	if !postings {
		return
	}

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

// postingCounter is an optional PostingProvider capability: report a trigram's
// posting count cheaply (without decoding the list). The mmap loader implements
// it via a varint walk; providers that don't are handled by the decode fallback
// in PostingCount.
type postingCounter interface {
	PostingCount(t trigram.Trigram) int
}

// PostingCount returns the number of MATERIALIZED postings for t. It is the
// cheap selectivity probe used by the positional search path to choose the
// rarest driver trigram before decoding only that one; a lazy provider that
// exposes a count uses it, otherwise we fall back to decoding (eager builds just
// read the slice length).
//
// Selective-index caveat (parity-critical): for a DESELECTED gram (IndexedGram
// false) this returns 0 — the same value a genuinely-zero gram returns. A
// deselected gram's true occurrence count is UNKNOWN, so 0 here must NOT be read
// as "maximally selective". Every selectivity caller (search.positionalTrigrams,
// regexPositional's cap check) therefore guards with IndexedGram before trusting
// a count; a deselected gram is routed to the scan fallback first.
func (ix *Index) PostingCount(t trigram.Trigram) int {
	if ix.pp != nil {
		if pc, ok := ix.pp.(postingCounter); ok {
			return pc.PostingCount(t)
		}
		return len(ix.pp.Postings(t))
	}
	return len(ix.postings[t])
}
