package index

import "moedex/internal/trigram"

// BlobData is a serialization-friendly snapshot of one blob: its identity,
// indexed content, and the files that share it. Posting lists are persisted
// separately and keyed by blob ID, which equals the blob's position in a
// Snapshot slice.
//
// Note: Content is the *indexed* content (a leading UTF-8 BOM stripped at
// ingest), which is what search operates on; persistence round-trips it
// verbatim.
type BlobData struct {
	SHA     string
	Content []byte
	Files   []FileRef
}

// Snapshot returns blob data in ID order: result[i] is the blob with ID i, the
// same ID used in posting lists.
func (ix *Index) Snapshot() []BlobData {
	out := make([]BlobData, len(ix.blobs))
	for i, b := range ix.blobs {
		out[i] = BlobData{
			SHA:     b.SHA,
			Content: append([]byte(nil), b.Content...),
			Files:   append([]FileRef(nil), b.Files...),
		}
	}
	return out
}

// Trigrams returns every indexed trigram, in unspecified order. When the index
// is lazily loaded, the set comes from the provider.
func (ix *Index) Trigrams() []trigram.Trigram {
	if ix.pp != nil {
		return ix.pp.Trigrams()
	}
	out := make([]trigram.Trigram, 0, len(ix.postings))
	for t := range ix.postings {
		out = append(out, t)
	}
	return out
}

// PostingProvider supplies posting lists on demand — e.g. decoding from an
// mmap'd region. A lazily-loaded index decodes only the trigrams a query
// touches, so the bulk of the postings never enter the Go heap.
type PostingProvider interface {
	Postings(t trigram.Trigram) []Posting
	Trigrams() []trigram.Trigram
}

// Restore rebuilds an index from blob data and a posting map without
// re-tokenizing. blobs[i] must be the blob with ID i (the IDs referenced by
// postings). Intended for persistence loaders.
//
// Restore takes ownership of each BlobData's Content and Files: they are
// aliased directly into the index, not copied (unlike AddFile, which
// defensively copies). Callers must not mutate them after the call.
func Restore(blobs []BlobData, postings map[trigram.Trigram][]Posting) *Index {
	ix := restoreBlobs(blobs)
	ix.postings = postings
	return ix
}

// RestoreLazy rebuilds an index whose postings are served on demand by pp.
// Blob content and metadata are materialized as in Restore (same
// take-ownership, no-copy contract on BlobData.Content/Files); only the
// postings stay lazy, which is what keeps a loaded index's heap near content
// size.
//
// Membership: the resulting index's IndexedGram defers to pp when pp implements
// gramMember (a selective shard's provider); otherwise the index is treated as
// all-indexed (legacy / all-trigram shards), preserving exact back-compat.
func RestoreLazy(blobs []BlobData, pp PostingProvider) *Index {
	ix := restoreBlobs(blobs)
	ix.pp = pp
	return ix
}

func restoreBlobs(blobs []BlobData) *Index {
	ix := New()
	ix.blobs = make([]*Blob, len(blobs))
	for i, bd := range blobs {
		ix.blobs[i] = &Blob{
			ID:         uint64(i),
			SHA:        bd.SHA,
			Content:    bd.Content,
			Files:      bd.Files,
			lineStarts: lineStarts(bd.Content),
		}
		ix.bySHA[bd.SHA] = uint64(i)
	}
	return ix
}
