package index

import "moedex/internal/trigram"

// Builder is the selective (workload-aware) index build path. It is the opt-in
// alternative to New()+AddFile, which emits a posting list for EVERY trigram.
//
// Two-pass selection (the FREE-style lineage, arXiv 2504.12251): AddFile records
// each blob's content and, in one pass over its bytes, the SET of distinct
// trigrams it contains (so we can count per-gram DOCUMENT frequency without
// retaining all positions). Finalize then asks the GramSelector which grams to
// keep, and emits positional postings — exactly as the all-trigram builder
// would — for the kept grams ONLY. Every dropped gram is recorded as
// NOT-INDEXED in the resulting index (selected set), so the query layer treats
// it as All (force-scan) rather than as zero occurrences. Dropping a gram thus
// only ever WIDENS the candidate set; ripgrep parity is preserved by the
// IndexedGram membership gate.
//
// Memory: the builder retains blob content (the content store needs it anyway,
// exactly like New) plus one per-gram document-frequency counter (bounded by the
// number of distinct trigrams, <= 2^24). It does NOT retain every position
// across the corpus; positions are emitted in the second pass straight into the
// final posting map, so peak posting memory is no larger than the all-trigram
// builder's — and the kept-set is a subset, so it is typically smaller.
type Builder struct {
	sel      GramSelector
	blobs    []*Blob
	bySHA    map[string]uint64
	docFreq  map[trigram.Trigram]int // distinct-blob count per gram (pass 1)
	finished bool
}

// NewSelective returns a Builder that will keep only the grams sel chooses.
// Passing a nil selector keeps every gram (equivalent to the all-trigram build,
// but routed through the selective path and back-compatibly recorded as
// all-indexed). Callers add content with AddFile, then call Finalize to obtain
// the *Index.
func NewSelective(sel GramSelector) *Builder {
	return &Builder{
		sel:     sel,
		bySHA:   map[string]uint64{},
		docFreq: map[trigram.Trigram]int{},
	}
}

// AddFile records content identified by sha under (repo, rel, abs), mirroring
// Index.AddFile's dedup-by-SHA semantics exactly: files sharing a SHA are
// deduplicated (content stored once, FileRef appended). It additionally tallies,
// once per blob, every distinct trigram the blob contains so Finalize can apply
// the document-frequency policy. It does not emit positional postings yet.
func (b *Builder) AddFile(repo, rel, abs, sha string, content []byte) {
	if b.finished {
		panic("index: Builder.AddFile after Finalize")
	}
	ref := FileRef{Repo: repo, RelPath: rel, AbsPath: abs}
	if id, ok := b.bySHA[sha]; ok {
		b.blobs[id].Files = append(b.blobs[id].Files, ref)
		return
	}

	id := uint64(len(b.blobs))
	buf := append([]byte(nil), content...) // own the content (matches AddFile)
	blob := &Blob{
		ID:         id,
		SHA:        sha,
		Content:    buf,
		Files:      []FileRef{ref},
		lineStarts: lineStarts(buf),
	}
	b.blobs = append(b.blobs, blob)
	b.bySHA[sha] = id

	// Count document frequency: each distinct trigram of this blob increments by
	// one (not once per occurrence). A small per-blob set dedups occurrences.
	if len(buf) >= trigram.N {
		seen := make(map[trigram.Trigram]struct{}, len(buf))
		for i := 0; i+trigram.N <= len(buf); i++ {
			t := trigram.Trigram{buf[i], buf[i+1], buf[i+2]}
			seen[t] = struct{}{}
		}
		for t := range seen {
			b.docFreq[t]++
		}
	}
}

// NumBlobs reports how many distinct blobs have been added so far.
func (b *Builder) NumBlobs() int { return len(b.blobs) }

// Finalize applies the selector and returns the selective *Index. It emits
// positional postings only for the kept grams, in (Blob, Offset) order so the
// lists stay sorted exactly as Index.AddFile produces them. The returned index's
// IndexedGram reports true precisely for the kept grams; every other gram is
// NOT-INDEXED (forced to scan by the query layer). Finalize may be called once.
//
// When the selector is nil, every gram is kept; the result records selected==nil
// so it is byte-identical in behavior to a default all-trigram build (universal
// IndexedGram), with no per-gram membership overhead.
func (b *Builder) Finalize() *Index {
	if b.finished {
		panic("index: Builder.Finalize called twice")
	}
	b.finished = true

	ix := &Index{
		blobs:    b.blobs,
		bySHA:    b.bySHA,
		postings: map[trigram.Trigram][]Posting{},
	}

	if b.sel == nil {
		// All-trigram, via the selective path. Record selected==nil so the index
		// is the universal-true (all-indexed) fast path, then emit every gram.
		b.emitAll(ix)
		return ix
	}

	keep := b.sel.Select(b.docFreq, len(b.blobs))
	ix.selected = keep

	// Second pass: emit positional postings for kept grams only, in ID then
	// offset order so each list is sorted by (Blob, Offset) without a re-sort.
	for id, blob := range b.blobs {
		buf := blob.Content
		for i := 0; i+trigram.N <= len(buf); i++ {
			t := trigram.Trigram{buf[i], buf[i+1], buf[i+2]}
			if _, ok := keep[t]; !ok {
				continue
			}
			ix.postings[t] = append(ix.postings[t], Posting{Blob: uint64(id), Offset: i})
		}
	}
	return ix
}

// emitAll materializes every trigram (the nil-selector / all-trigram path).
func (b *Builder) emitAll(ix *Index) {
	for id, blob := range b.blobs {
		buf := blob.Content
		for i := 0; i+trigram.N <= len(buf); i++ {
			t := trigram.Trigram{buf[i], buf[i+1], buf[i+2]}
			ix.postings[t] = append(ix.postings[t], Posting{Blob: uint64(id), Offset: i})
		}
	}
}

// RestoreSelective rebuilds a selective index from blob data, a posting map, and
// the authoritative keep-set. It is the loader counterpart to Restore for shards
// that carry a selection section. A nil or empty selected set is treated as
// all-indexed only when explicitly signaled by the caller passing nil; an empty
// non-nil map means "nothing indexed" (every gram forced to scan), which is a
// legitimate (degenerate) selective shard.
func RestoreSelective(blobs []BlobData, postings map[trigram.Trigram][]Posting, selected map[trigram.Trigram]struct{}) *Index {
	ix := restoreBlobs(blobs)
	ix.postings = postings
	ix.selected = selected
	return ix
}

// SelectedGrams returns the authoritative keep-set for a selective index, or nil
// for an all-indexed (default/legacy) index. It is used by the persistence layer
// (diskstore) to serialize the selection section, and by tests. The returned map
// must not be mutated.
func (ix *Index) SelectedGrams() map[trigram.Trigram]struct{} {
	return ix.selected
}
