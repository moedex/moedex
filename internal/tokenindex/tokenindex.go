// Package tokenindex is a persistent word/token inverted index over the corpus,
// providing the per-document and global term statistics BM25 ranking needs.
//
// A "document" is a deduped blob, addressed by index.Blob.ID (uint64). The
// index records, for every term: its document frequency (how many blobs contain
// it) and, per blob, its term frequency; plus each blob's length in tokens and
// the corpus average. That is exactly the BM25 input set.
//
// # Tokenization rule (Tokenize)
//
// Tokenize is the single source of truth for the canonical term set; Build and
// the query side MUST both call it so they agree byte-for-byte. The rule:
//
//  1. The input is scanned as UTF-8 runes. A "word run" is a maximal run of
//     runes that are Unicode letters or digits (unicode.IsLetter ||
//     unicode.IsDigit). Everything else (punctuation, whitespace, symbols,
//     underscores, etc.) is a separator and is discarded. So `ssl_api.client`
//     and `ssl-api/client` both yield the runs ssl, api, client.
//
//  2. Each word run is further split on case boundaries to break apart
//     camelCase / PascalCase / acronym-prefixed identifiers. Within a run a new
//     subtoken starts at:
//     - a lower→upper transition (fooBar  -> foo, Bar), and
//     - the last upper in an UPPER→upper-lower transition, i.e. an acronym
//     followed by a word (HTTPServer -> HTTP, Server; SslAPIClient with run
//     "SslAPIClient" -> Ssl, API, Client).
//     Digits stay attached to the alphabetic piece they border (utf8Decode is
//     one subtoken; v2 stays v2; sha256sum stays sha256sum). Digit/letter
//     boundaries do NOT split, matching how identifiers like base64 read as one
//     word.
//
//  3. The canonical tokens emitted for a run are ALL CONTIGUOUS JOINS of its
//     case-split subtokens s1..sk, lowercased. For every window [i..j] (i<=j)
//     we emit lowercase(concat(s_i..s_j)). That includes the length-1 subtokens
//     (the old behaviour) AND every intermediate join, not just the full run.
//     Concretely `ReIssueCertificate` -> subtokens [re, issue, certificate] and
//     emits (in order) re, reissue, reissuecertificate, issue, issuecertificate,
//     certificate.
//     This fixes a recall asymmetry: a query `reissue certificate` tokenizes to
//     [reissue, certificate], and `reissue` is a contiguous join of the
//     identifier's subtokens, so it now matches; previously only the length-1
//     parts and the whole run were emitted, so the join `reissue` was missing.
//
//     Window order is deterministic: by window START i ascending, then (for a
//     fixed start) by window LENGTH ascending. So for start i we emit s_i, then
//     s_i s_{i+1}, then s_i s_{i+1} s_{i+2}, ... before moving to start i+1. A
//     plain word like `client` is a single subtoken (k=1): its only window is
//     [0..0], so it emits exactly [client] with no duplicate. Within a run we
//     de-dup so a window that collapses to an already-emitted token (e.g. when
//     the full run equals a single subtoken) is not emitted twice.
//
//     CAP: emitting all O(k^2) windows is bounded by capping the maximum window
//     LENGTH at maxJoinLen (4). Identifiers with k<=4 subtokens (the vast
//     majority) are unaffected. For longer runs we emit all windows up to length
//     4 PLUS the full-run window (so the whole identifier stays queryable even
//     when k>4). This keeps per-run emission O(4*k) instead of O(k^2), bounding
//     index inflation on pathological long runs while preserving full-identifier
//     and short-join recall.
//
// Keeping the parts, joins, and whole token lets a query for "client",
// "reissue", or "sslapiclient" hit the document, at the cost of slightly
// inflating doc length. Term frequencies count every emission, so a single
// `ReIssueCertificate` contributes tf=1 to each emitted join.
//
// CONTRACT FREEZE (Lane A implements behind these signatures; do not change
// their shape — internal/rank's BM25 arm is written against them):
//
//	func Build(ix *index.Index) *TokenIndex
//	func Tokenize(text []byte) []string
//	(*TokenIndex) NumDocs() int
//	(*TokenIndex) AvgDocLen() float64
//	(*TokenIndex) DocLen(blob uint64) int
//	(*TokenIndex) DocFreq(term string) int
//	(*TokenIndex) TermFreq(term string, blob uint64) int
//	func Save(ti *TokenIndex, path string) error
//	func Load(path string) (*TokenIndex, error)
package tokenindex

import (
	"io"
	"sort"
	"unicode"
	"unicode/utf8"

	"moedex/internal/index"
)

// TokenIndex holds term statistics over all blobs in a corpus in a compressed
// sparse row (CSR) layout that is identical in memory and on disk.
//
// Storage layout — six parallel arrays, no maps:
//   - docLen:   blob ID -> token count. Dense; 0 for an unused ID, which matches
//     DocLen's "0 if unknown" contract exactly.
//   - termOff:  numTerms+1 offsets into termText. Term i is
//     termText[termOff[i]:termOff[i+1]].
//   - termText: every term's bytes, concatenated in ascending lexicographic
//     order so a term lookup is a binary search.
//   - postOff:  numTerms+1 offsets into postBlob/postTF. Term i's postings are
//     the range [postOff[i], postOff[i+1]).
//   - postBlob: blob IDs, ascending within each term's range.
//   - postTF:   term frequencies, parallel to postBlob.
//
// This replaces a map[string]map[uint64]int that cost 115 bytes per posting.
// Average document frequency is 2.3, so nearly every term paid a whole Go map
// header plus a bucket allocation to hold two entries.
//
// A TokenIndex is either BUILT (arrays on the Go heap, mm == nil) or LOADED
// (arrays aliasing an mmap, mm != nil). Every accessor is identical for both;
// only Close differs. See ADR 0005 for the same discipline applied to postings.
type TokenIndex struct {
	numDocs  int
	totalLen int

	docLen   []uint32
	termOff  []uint32
	termText []byte
	postOff  []uint32
	postBlob []uint32
	postTF   []uint32

	// mm is the mapping backing the arrays above, or nil when they are on the
	// Go heap. Close unmaps it; reading any array after that will crash.
	mm io.Closer
}

// Build tokenizes every blob in ix and accumulates term statistics.
func Build(ix *index.Index) *TokenIndex {
	if ix == nil {
		return fromMaps(0, 0, nil, nil)
	}
	postings := make(map[string]map[uint64]int)
	docLen := make(map[uint64]int)
	numDocs, totalLen := 0, 0
	n := ix.NumBlobs()
	for id := uint64(0); id < uint64(n); id++ {
		b := ix.Blob(id)
		if b == nil {
			continue
		}
		numDocs++
		toks := Tokenize(b.Content)
		docLen[b.ID] = len(toks)
		totalLen += len(toks)
		for _, t := range toks {
			post := postings[t]
			if post == nil {
				post = make(map[uint64]int)
				postings[t] = post
			}
			post[b.ID]++
		}
	}
	return fromMaps(numDocs, totalLen, docLen, postings)
}

// fromMaps converts accumulated maps into the CSR arrays. Task 4 removes the
// map stage entirely; until then this keeps Build's output shape correct while
// the representation changes underneath it.
func fromMaps(numDocs, totalLen int, docLen map[uint64]int, postings map[string]map[uint64]int) *TokenIndex {
	ti := &TokenIndex{numDocs: numDocs, totalLen: totalLen}

	var maxBlob uint64
	for id := range docLen {
		if id > maxBlob {
			maxBlob = id
		}
	}
	if len(docLen) > 0 {
		ti.docLen = make([]uint32, maxBlob+1)
		for id, l := range docLen {
			ti.docLen[id] = uint32(l)
		}
	}

	terms := make([]string, 0, len(postings))
	for t := range postings {
		terms = append(terms, t)
	}
	sort.Strings(terms)

	numPost := 0
	for _, p := range postings {
		numPost += len(p)
	}
	ti.termOff = make([]uint32, len(terms)+1)
	ti.postOff = make([]uint32, len(terms)+1)
	ti.termText = make([]byte, 0, 16*len(terms))
	ti.postBlob = make([]uint32, 0, numPost)
	ti.postTF = make([]uint32, 0, numPost)

	for i, t := range terms {
		ti.termOff[i] = uint32(len(ti.termText))
		ti.postOff[i] = uint32(len(ti.postBlob))
		ti.termText = append(ti.termText, t...)

		post := postings[t]
		blobs := make([]uint64, 0, len(post))
		for b := range post {
			blobs = append(blobs, b)
		}
		sort.Slice(blobs, func(a, b int) bool { return blobs[a] < blobs[b] })
		for _, b := range blobs {
			ti.postBlob = append(ti.postBlob, uint32(b))
			ti.postTF = append(ti.postTF, uint32(post[b]))
		}
	}
	ti.termOff[len(terms)] = uint32(len(ti.termText))
	ti.postOff[len(terms)] = uint32(len(ti.postBlob))
	return ti
}

// Tokenize splits text into the canonical term set Build uses. See the package
// doc for the exact, frozen tokenization rule. Exported so the query side
// tokenizes identically.
func Tokenize(text []byte) []string {
	var out []string
	i := 0
	n := len(text)
	for i < n {
		r, size := utf8.DecodeRune(text[i:])
		if !isWordRune(r) {
			i += size
			continue
		}
		// Accumulate a maximal word run as runes.
		start := i
		for i < n {
			r, size = utf8.DecodeRune(text[i:])
			if !isWordRune(r) {
				break
			}
			i += size
		}
		out = appendRunTokens(out, text[start:i])
	}
	return out
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// maxJoinLen caps the maximum number of adjacent subtokens joined into a single
// emitted token (see package doc, rule 3). Windows longer than this are skipped,
// except the full-run window which is always emitted so the whole identifier
// stays queryable. Bounds per-run emission at O(maxJoinLen*k) instead of O(k^2).
const maxJoinLen = 4

// appendRunTokens splits a single word run (already known to contain only
// letters/digits) on case boundaries, then appends every contiguous join of
// adjacent subtokens (up to maxJoinLen, plus the full run), lowercased and
// de-duplicated within this run. See the package doc for the exact rule.
func appendRunTokens(out []string, run []byte) []string {
	runes := []rune(string(run))
	if len(runes) == 0 {
		return out
	}

	// Find subtoken boundaries, recording them as [start,end) rune ranges so we
	// can join contiguous subtokens without re-concatenating strings.
	type span struct{ start, end int }
	var subs []span
	subStart := 0
	for i := 1; i < len(runes); i++ {
		prev := runes[i-1]
		cur := runes[i]
		boundary := false
		// lower/digit -> upper : fooBar, foo2Bar
		if unicode.IsUpper(cur) && !unicode.IsUpper(prev) {
			boundary = true
		}
		// UPPER -> Upper followed by lower : HTTPServer -> HTTP|Server.
		// At position i (the upper that begins the next word) the boundary is
		// before i, only if i+1 is lower and prev is upper.
		if unicode.IsUpper(prev) && unicode.IsUpper(cur) && i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
			boundary = true
		}
		if boundary {
			subs = append(subs, span{subStart, i})
			subStart = i
		}
	}
	subs = append(subs, span{subStart, len(runes)})

	k := len(subs)
	// De-dup within this run only (deterministic; preserves insertion order via
	// the append below). A window collapsing to an already-emitted token (e.g.
	// the full run equals a single subtoken when k==1) is emitted once.
	seen := make(map[string]struct{}, k*2)
	emit := func(s string) {
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}

	// Emit all contiguous joins, ordered by window start then window length.
	// Cap window length at maxJoinLen.
	for i := 0; i < k; i++ {
		maxJ := i + maxJoinLen
		if maxJ > k {
			maxJ = k
		}
		for j := i + 1; j <= maxJ; j++ {
			emit(lower(runes[subs[i].start:subs[j-1].end]))
		}
	}
	// Always emit the full run so the whole identifier stays queryable even when
	// k > maxJoinLen (the loop above would have skipped it). De-dup handles the
	// common case where it was already emitted.
	emit(lower(runes))
	return out
}

func lower(runes []rune) string {
	b := make([]rune, len(runes))
	for i, r := range runes {
		b[i] = unicode.ToLower(r)
	}
	return string(b)
}

// NumDocs returns the number of documents (blobs) indexed.
func (ti *TokenIndex) NumDocs() int { return ti.numDocs }

// AvgDocLen returns the mean document length in tokens. Zero for an empty corpus.
func (ti *TokenIndex) AvgDocLen() float64 {
	if ti.numDocs == 0 {
		return 0
	}
	return float64(ti.totalLen) / float64(ti.numDocs)
}

// DocLen returns the token count of the given blob (0 if unknown).
func (ti *TokenIndex) DocLen(blob uint64) int {
	if blob >= uint64(len(ti.docLen)) {
		return 0
	}
	return int(ti.docLen[blob])
}

// DocFreq returns how many documents contain term. term must already be a
// single canonical token (the caller runs Tokenize on the query). Returns 0 if
// the term is absent.
func (ti *TokenIndex) DocFreq(term string) int { return ti.Postings(term).Len() }

// TermFreq returns the number of occurrences of term within blob. term must
// already be a single canonical token. Returns 0 if absent.
//
// This resolves the term on every call. A caller looping over many blobs for
// the same term should hold a PostingList from Postings instead.
func (ti *TokenIndex) TermFreq(term string, blob uint64) int {
	return ti.Postings(term).TFOf(blob)
}

// Docs returns the blob IDs that contain term, in ascending order. term must
// already be a single canonical token. Returns nil if the term is absent. This
// is the inverted-index candidate set BM25 ranking actually needs — exact (every
// returned blob has tf>0), so a ranker can generate candidates from the same
// index it scores against instead of from a separate trigram index. (Used by the
// corpus ranker, whose content-only index carries no positional postings.)
func (ti *TokenIndex) Docs(term string) []uint64 {
	p := ti.Postings(term)
	if p.Len() == 0 {
		return nil
	}
	out := make([]uint64, p.Len())
	for i := range out {
		out[i] = p.Blob(i)
	}
	return out
}

// Close releases the mapping backing a loaded index. It is a no-op for an index
// produced by Build, whose arrays are on the Go heap. Close is idempotent.
// Reading any accessor after Close on a loaded index reads unmapped memory.
func (ti *TokenIndex) Close() error {
	if ti == nil || ti.mm == nil {
		return nil
	}
	err := ti.mm.Close()
	ti.mm = nil
	return err
}
