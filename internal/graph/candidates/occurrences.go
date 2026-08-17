package candidates

// occurrences.go is the source side of candidate generation: everywhere in the
// corpus a name occurs. It unions the symbol arm (classified, incomplete) with
// the trigram arm (unclassified, complete), which is the step the plan calls the
// trigram fan-out.
//
// The trigram arm reuses the engine's positional-literal machinery: the begin-gram
// and end-gram posting lists are both sorted by (Blob, Offset), so intersecting
// them at a fixed positional distance is a single forward merge-join with no map
// and no allocation, and every surviving position is confirmed byte-exact against
// the blob's content. This is the same reduction internal/search runs for a
// literal query, restricted here to returning (blob, offset) instead of
// line-granular matches: a graph edge is anchored at a byte offset, and the line
// a reference sits on is not enough to point a verifier at it.
//
// Soundness follows the engine's rule that a filter may only ever WIDEN. Three
// cases cannot be answered by the postings, and each falls back to a full content
// scan rather than to an empty answer:
//
//   - a name shorter than a trigram (no gram to look up),
//   - a selective build that deselected the begin or end gram (its postings are
//     UNKNOWN, not zero — see index.IndexedGram),
//   - an index carrying no postings at all (see Corpus.postings).
//
// All three produce identical results to the positional path, just slower, which
// is exactly the trade the parity core makes everywhere else.

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

// positionalOccurrences calls add for every position where needle occurs in ix,
// found by intersecting the begin-gram and end-gram posting lists at the fixed
// positional distance between them and confirming the full byte string. It
// reports false without calling add when the index cannot answer soundly (a
// deselected gram), leaving the caller to scan instead.
//
// needle must be at least trigram.N bytes.
func positionalOccurrences(ix *index.Index, needle []byte, add func(blob uint64, content []byte, off int)) bool {
	begin := trigram.Trigram{needle[0], needle[1], needle[2]}
	end := trigram.Trigram{needle[len(needle)-3], needle[len(needle)-2], needle[len(needle)-1]}
	// Selective-index gate: a deselected gram's posting list is UNKNOWN, so
	// intersecting it would under-approximate — the one thing a recall-complete
	// pass may not do. On the default all-trigram build this never triggers.
	if ix.Selective() && (!ix.IndexedGram(begin) || !ix.IndexedGram(end)) {
		return false
	}

	dist := len(needle) - trigram.N
	begins := ix.Postings(begin)
	ends := ix.Postings(end)
	// Both lists are sorted by (Blob, Offset) (see index.AddFile), and the target
	// key (p.Blob, p.Offset+dist) is therefore monotonically non-decreasing as we
	// walk begins — so one forward cursor over ends suffices: O(n+m), no map.
	j := 0
	var cur *index.Blob
	for _, p := range begins {
		want := p.Offset + dist
		for j < len(ends) && (ends[j].Blob < p.Blob || (ends[j].Blob == p.Blob && ends[j].Offset < want)) {
			j++
		}
		if j >= len(ends) || ends[j].Blob != p.Blob || ends[j].Offset != want {
			continue
		}
		if cur == nil || cur.ID != p.Blob {
			if cur = ix.Blob(p.Blob); cur == nil {
				continue
			}
		}
		// The gram intersection is a necessary condition, not a sufficient one
		// (the interior bytes were never checked), so confirm the whole string.
		// Guard the upper bound: a truncated blob must not panic the sweep.
		if p.Offset+len(needle) <= len(cur.Content) && bytes.Equal(cur.Content[p.Offset:p.Offset+len(needle)], needle) {
			add(p.Blob, cur.Content, p.Offset)
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
