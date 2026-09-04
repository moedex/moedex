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
	"slices"
	"strings"
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

// postRec is one (term, blob, tf) triple during a build. 12 bytes, so 40.6M
// postings cost 487 MB rather than the 3.5 GB of map overhead the previous
// map[string]map[uint64]int paid for the same information.
type postRec struct {
	term uint32
	blob uint32
	tf   uint32
}

// Build tokenizes every blob in ix and accumulates term statistics.
//
// The build is sort-based rather than map-of-maps: intern each term to a dense
// id, emit one flat 12-byte record per posting, sort the dictionary
// lexicographically so ids ARE ranks, then counting-scatter the records into
// CSR order. Term ids are dense after the remap, so no comparison sort is
// needed; records are appended in ascending blob order, so a stable scatter
// preserves the ascending-blob invariant for free.
//
// Measured on the 492-shard reference corpus (56,978 blobs, 17.6M distinct
// terms, 40.6M postings): peak heap 7,593 MB and max RSS 8.78 GB, against
// 11,281 MB and 12.86 GB for the map-of-maps shape it replaced. That RSS
// reduction is what lets a 16 GB machine build the index rather than only
// serve it. The dominant remaining lever is GC headroom rather than this
// algorithm: GOGC=25 alone takes peak to 4,974 MB and RSS to 6.39 GB, at
// about 12% more wall time. Wall time is about 38s for that corpus; it was
// 4m21s before the per-blob scratch map became a dense array, because a
// reused Go map walks its whole table on every range and one blob held 63%
// of the corpus vocabulary.
func Build(ix *index.Index) *TokenIndex {
	ti := &TokenIndex{}
	if ix == nil {
		ti.termOff = []uint32{0}
		ti.postOff = []uint32{0}
		return ti
	}

	dict := make(map[string]uint32)
	var terms []string
	var recs []postRec
	// Per-blob term frequencies live in a dense scratch array indexed by term
	// id, with touched listing the ids this blob actually hit, rather than in a
	// reused map[uint32]uint32. A Go map never gives capacity back: clear keeps
	// the table (golang/go#70617), so a map reused across blobs stays sized for
	// the LARGEST blob in the corpus forever, and both clear and range walk the
	// whole table rather than the live entries. One generated file with ~11M
	// distinct terms therefore taxed every one of the other 57k blobs, and
	// `for tid, tf := range perBlob` alone was 74% of the whole build. Indexing
	// an array costs 4 bytes per distinct term (70 MB on the reference corpus)
	// and makes the flush O(distinct terms in THIS blob).
	var termTF []uint32
	var touched []uint32

	n := ix.NumBlobs()
	for id := uint64(0); id < uint64(n); id++ {
		b := ix.Blob(id)
		if b == nil {
			continue
		}
		ti.numDocs++
		toks := Tokenize(b.Content)
		for uint64(len(ti.docLen)) <= b.ID {
			ti.docLen = append(ti.docLen, 0)
		}
		ti.docLen[b.ID] = uint32(len(toks))
		ti.totalLen += len(toks)

		touched = touched[:0]
		for _, t := range toks {
			tid, ok := dict[t]
			if !ok {
				tid = uint32(len(terms))
				dict[t] = tid
				terms = append(terms, t)
				termTF = append(termTF, 0)
			}
			if termTF[tid] == 0 {
				touched = append(touched, tid)
			}
			termTF[tid]++
		}
		// Flush this blob's postings and reset only the slots it used, so the
		// scratch array is clean for the next blob without a full sweep.
		for _, tid := range touched {
			recs = append(recs, postRec{term: tid, blob: uint32(b.ID), tf: termTF[tid]})
			termTF[tid] = 0
		}
	}
	// dict is dead the moment tokenizing ends: every term it can produce is
	// already captured in terms, and recs already holds interned (unsorted)
	// ids. Dropping it here (~1.2 GB on the reference corpus) frees that
	// memory before the CSR output arrays are allocated below, rather than
	// letting it sit live through the whole emit phase.
	dict = nil
	termTF = nil
	touched = nil

	// Sort the dictionary, then remap ids so that a term's final rank is
	// known without a second pass over the text.
	order := make([]uint32, len(terms))
	for i := range order {
		order[i] = uint32(i)
	}
	slices.SortFunc(order, func(a, b uint32) int { return strings.Compare(terms[a], terms[b]) })
	remap := make([]uint32, len(terms))
	for rank, old := range order {
		remap[old] = uint32(rank)
	}
	for i := range recs {
		recs[i].term = remap[recs[i].term]
	}
	remap = nil

	// Counting scatter instead of a sort over all of recs: after the remap,
	// term ids are dense in [0, len(terms)), which is exactly the case a
	// counting sort is built for and a comparison sort (even on a dense key)
	// wastes O(log n) passes on. Count postings per term, prefix-sum those
	// counts into postOff, then scatter each record directly to its slot.
	//
	// Correctness of the scatter without a per-term sort: recs was appended
	// blob-by-blob in ascending blob id (the outer loop above walks blob ids
	// ascending, and each blob contributes at most one record per term), so
	// recs is already globally ordered by blob. Scattering in that same
	// order — advancing each term's cursor by one on every hit — places a
	// term's postings into postBlob/postTF in the order they were visited,
	// i.e. ascending blob id. That is the same "stable scatter" a bucket
	// sort performs; it reproduces the (term, blob) sort's output without
	// ever comparing or moving records against each other.
	counts := make([]uint32, len(terms))
	for _, rec := range recs {
		counts[rec.term]++
	}
	ti.postOff = make([]uint32, len(terms)+1)
	for i, c := range counts {
		ti.postOff[i+1] = ti.postOff[i] + c
	}
	cursor := append([]uint32(nil), ti.postOff[:len(terms)]...)

	ti.postBlob = make([]uint32, len(recs))
	ti.postTF = make([]uint32, len(recs))
	for _, rec := range recs {
		slot := cursor[rec.term]
		ti.postBlob[slot] = rec.blob
		ti.postTF[slot] = rec.tf
		cursor[rec.term]++
	}
	recs = nil
	cursor = nil
	counts = nil

	ti.termOff = make([]uint32, len(terms)+1)
	ti.termText = make([]byte, 0, len(terms)*12)
	for rank, old := range order {
		ti.termOff[rank] = uint32(len(ti.termText))
		ti.termText = append(ti.termText, terms[old]...)
	}
	ti.termOff[len(terms)] = uint32(len(ti.termText))
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
