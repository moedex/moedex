package candidates

// occurrences.go is the source side of candidate generation: everywhere in the
// corpus a name occurs. It unions the symbol arm (classified, incomplete) with
// the trigram arm (unclassified, complete), which is the step the plan calls the
// trigram fan-out.
//
// The trigram arm uses one required gram as a positional driver, then confirms
// the entire name against the blob. Choosing a sparse driver avoids decoding
// common prefix/suffix lists for every name in a large corpus. A bounded set of
// count-only probes keeps selection work independent of the name's length.
//
// Soundness follows the engine's rule that a filter may only ever WIDEN. A short
// name, a shard without postings, or a selective index without a usable driver
// falls back to scanning content. A deselected gram is UNKNOWN, never zero.

import (
	"bytes"

	"moedex/internal/index"
	"moedex/internal/symbol"
	"moedex/internal/trigram"
)

// occurrence is one located occurrence of a name, tagged with the arm that found
// it. It is the SOURCE side of a candidate edge, before pairing with definitions.
type occurrence struct {
	site Site
	typ  Type
}

// siteKey identifies an occurrence position for dedup across the two arms. It
// deliberately omits End: the arms must agree on where a name STARTS, but an
// extractor's idea of where the token ends is its own, and admitting two entries
// for one position would double every edge out of it.
type siteKey struct {
	shard int
	blob  uint64
	start int
}

// Bounds on hasPostings' probe. A shard either has postings or it does not, so
// the first usable gram settles it; the budgets only cover a selective build
// whose leading content grams were all deselected, and keep the probe O(1) in
// corpus size instead of walking a shard's whole content to prove a negative.
const (
	postingProbeGrams     = 8    // usable grams to test before giving up
	postingProbePositions = 4096 // content positions to examine while finding them
)

// occurrences returns every corpus-wide occurrence of name, symbol arm first so
// its classification wins any position both arms find.
func (c *Corpus) occurrences(name string) []occurrence {
	seen := map[siteKey]struct{}{}
	var out []occurrence

	// Symbol arm. It is the only arm that can tell a call site from a word in a
	// comment, so where the two agree on a position its verdict is the one kept.
	// References returns definitions AND references, sorted with definitions
	// first at a shared offset, so a name that is both resolves as a definition.
	for _, r := range c.syms.References(name) {
		k := siteKey{shard: r.Shard, blob: r.Blob, start: r.Start}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		typ := SymbolReference
		if r.Role == symbol.Definition {
			typ = SiblingDefinition
		}
		out = append(out, occurrence{
			site: Site{Shard: r.Shard, Blob: r.Blob, Start: r.Start, End: r.End},
			typ:  typ,
		})
	}

	if c.textOccurrences != nil {
		entry := c.textOccurrences.find(name)
		if entry.name != "" {
			for _, k := range entry.sites {
				if _, dup := seen[k]; dup {
					continue
				}
				out = append(out, occurrence{site: Site{Shard: k.shard, Blob: k.blob, Start: k.start, End: k.start + len(name)}, typ: TextOccurrence})
			}
			return out
		}
	}

	// Trigram arm, every shard — including the shards the symbol arm already
	// reported, because a shard's extractor sees only the code it understands and
	// the name may also appear in a comment, a string, or a sibling file of a
	// language with no extractor.
	needle := []byte(name)
	for shard := range c.idxs {
		out = c.appendTextOccurrences(out, seen, shard, needle)
	}
	return out
}

// appendTextOccurrences adds every standalone-identifier occurrence of needle in
// shard that the symbol arm did not already claim, choosing the positional path
// when the index can answer it soundly and the content scan when it cannot.
func (c *Corpus) appendTextOccurrences(out []occurrence, seen map[siteKey]struct{}, shard int, needle []byte) []occurrence {
	ix := c.idxs[shard]
	if ix == nil || len(needle) == 0 {
		return out
	}
	add := func(blob uint64, content []byte, off int) {
		if !identifierAt(content, off, len(needle)) {
			return
		}
		k := siteKey{shard: shard, blob: blob, start: off}
		if _, dup := seen[k]; dup {
			return
		}
		seen[k] = struct{}{}
		out = append(out, occurrence{
			site: Site{Shard: shard, Blob: blob, Start: off, End: off + len(needle)},
			typ:  TextOccurrence,
		})
	}

	if c.postings[shard] && len(needle) >= trigram.N && positionalOccurrences(ix, needle, add) {
		return out
	}
	scanOccurrences(ix, needle, add)
	return out
}

// positionalOccurrences calls add for every position where needle occurs in ix.
// Every occurrence must contain the chosen gram at its fixed offset, so a single
// posting list plus byte verification is sufficient; no begin/end intersection
// is needed. Posting order also preserves ascending (blob, occurrence offset).
// It returns false without calling add if no probed gram is trustworthy.
//
// needle must be at least trigram.N bytes.
func positionalOccurrences(ix *index.Index, needle []byte, add func(blob uint64, content []byte, off int)) bool {
	last := len(needle) - trigram.N
	// Probe the middle first: generated names often share both their prefix and
	// suffix. At most three unique grams are counted, and an already sparse
	// driver needs no further probing. Counts do not materialize mmap postings.
	positions := [3]int{last / 2, 0, last}
	var grams [3]trigram.Trigram
	n := 0
	driverOffset, driverCount := -1, 0
	var driver trigram.Trigram
	for _, off := range positions {
		gram := trigram.Trigram{needle[off], needle[off+1], needle[off+2]}
		duplicate := false
		for _, prior := range grams[:n] {
			if gram == prior {
				duplicate = true
				break
			}
		}
		if duplicate || !ix.IndexedGram(gram) {
			continue
		}
		grams[n] = gram
		n++
		count := ix.PostingCount(gram)
		if count == 0 {
			return true // A materialized required gram proves there is no match.
		}
		if driverOffset < 0 || count < driverCount {
			driver, driverOffset, driverCount = gram, off, count
		}
		if driverCount <= 64 {
			break
		}
	}
	if driverOffset < 0 {
		return false
	}
	var cur *index.Blob
	for _, p := range ix.Postings(driver) {
		if p.Offset < driverOffset {
			continue
		}
		off := p.Offset - driverOffset
		if cur == nil || cur.ID != p.Blob {
			if cur = ix.Blob(p.Blob); cur == nil {
				continue
			}
		}
		// Subtraction avoids overflow for malformed out-of-range offsets.
		if off <= len(cur.Content) && len(needle) <= len(cur.Content)-off && bytes.Equal(cur.Content[off:off+len(needle)], needle) {
			add(p.Blob, cur.Content, off)
		}
	}
	return true
}

// scanOccurrences is the fallback: it calls add for every position where needle
// occurs in any blob of ix, found by scanning content directly. It consults no
// posting list, so it is correct for any index — including one with no postings
// and one whose grams were deselected — which is what makes it a safe fallback
// rather than a second implementation to keep in sync.
func scanOccurrences(ix *index.Index, needle []byte, add func(blob uint64, content []byte, off int)) {
	if len(needle) == 0 {
		return
	}
	for id := uint64(0); id < uint64(ix.NumBlobs()); id++ {
		b := ix.Blob(id)
		if b == nil {
			continue
		}
		for off := 0; off+len(needle) <= len(b.Content); {
			i := bytes.Index(b.Content[off:], needle)
			if i < 0 {
				break
			}
			pos := off + i
			add(id, b.Content, pos)
			// Advance one byte, not len(needle): overlapping occurrences of a
			// self-overlapping name (e.g. "AbAb" in "AbAbAb") are distinct
			// positions, and skipping them would under-approximate.
			off = pos + 1
		}
	}
}

// identifierAt reports whether the n bytes at off in content stand alone as an
// identifier token — i.e. neither neighbour is an identifier byte.
//
// This is the ONLY filter candidate generation applies, and it is a necessary
// condition rather than a heuristic: in every language moedex indexes, a use of
// the name Add is spelled Add, so an occurrence embedded in a longer identifier
// (Address, doAddThing) provably is not one. Rejecting those keeps the candidate
// set finite without touching recall — the same "only ever widen" rule the
// trigram->regex reduction follows.
func identifierAt(content []byte, off, n int) bool {
	if off > 0 && identByte(content[off-1]) {
		return false
	}
	if e := off + n; e < len(content) && identByte(content[e]) {
		return false
	}
	return true
}

// EachIdentifier calls fn for every maximal identifier run in content, in
// ascending offset order, stopping early if fn returns false. The run aliases
// content and must not be retained past the call.
//
// This is the inverse of the identifierAt filter and shares its byte rule:
// a name occurs in a blob (as far as candidate generation is concerned) if and
// only if it equals one of these runs.
func EachIdentifier(content []byte, fn func(run []byte) bool) {
	eachIdentifierPosition(content, func(start, end int) bool { return fn(content[start:end]) })
}

// eachIdentifierPosition is the shared byte scanner for incremental dirty-name
// discovery and the scoped occurrence index. False means the callback stopped it.
func eachIdentifierPosition(content []byte, fn func(start, end int) bool) bool {
	for i := 0; i < len(content); {
		if !identByte(content[i]) {
			i++
			continue
		}
		j := i + 1
		for j < len(content) && identByte(content[j]) {
			j++
		}
		if !fn(i, j) {
			return false
		}
		i = j
	}
	return true
}

// identByte reports whether b can continue an identifier.
//
// The ASCII set is the intersection of every language's identifier rule, plus '$'
// (a legal identifier byte in JS/TS, ColdFusion, and shell-ish templating). Any
// byte >= 0x80 counts too: it is a UTF-8 lead or continuation byte, so the token
// continues into a non-ASCII rune and the match is a fragment of a longer name —
// exactly the case this rejects. Treating it as a boundary instead would admit
// every non-ASCII neighbour, and treating multi-byte runes as non-identifier
// would make the check depend on where a rune happens to straddle the range.
func identByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_', b == '$':
		return true
	case b >= 0x80:
		return true
	}
	return false
}

// hasPostings probes whether ix can answer a trigram query at all.
//
// The failure mode this exists for is silent: an index restored without postings
// (index.Restore with a nil map — what server.OpenSymbols builds, because symbol
// extraction never queries trigrams) returns an empty posting list for every
// gram, which is indistinguishable from "this name occurs nowhere". A whole
// arm of the fan-out would vanish with no error. So we take a gram we KNOW occurs
// (one read straight out of a blob's own content) and check it has postings: if it
// does not, the index carries none and the caller scans content instead.
//
// A selective build deliberately drops grams, and a deselected gram's count is 0
// whether or not the index has postings, so only grams IndexedGram vouches for
// are probed. Exhausting either probe budget without a positive answer falls back
// to scanning — slower and always correct, the safe direction.
func hasPostings(ix *index.Index) bool {
	if ix == nil {
		return false
	}
	grams, budget := 0, postingProbePositions
	for id := uint64(0); id < uint64(ix.NumBlobs()) && budget > 0; id++ {
		b := ix.Blob(id)
		budget--
		if b == nil {
			continue
		}
		for i := 0; i+trigram.N <= len(b.Content) && budget > 0; i++ {
			budget--
			t := trigram.Trigram{b.Content[i], b.Content[i+1], b.Content[i+2]}
			if !ix.IndexedGram(t) {
				continue // deselected: a zero count proves nothing either way
			}
			if ix.PostingCount(t) > 0 {
				return true
			}
			if grams++; grams >= postingProbeGrams {
				return false
			}
		}
	}
	return false
}
