// Package fmindex is a pure-Go, correct PoC of an FM-index (a compressed
// self-index built on the Burrows-Wheeler Transform) over a byte corpus. It
// answers exact-substring Count and Locate queries — never regex — and exists
// to de-risk the structure and honestly characterize its size/latency as a
// candidate cold/archival exact-match backend.
//
// # Scope and status (read this)
//
// This is a CORRECT, property-tested proof of concept, NOT production code and
// NOT wired into the live retrieval path. It deliberately changes nothing in
// internal/search, internal/query, internal/parity, internal/index, or
// internal/diskstore, so the ripgrep-parity invariant is untouched: an FM-index
// with zero callers cannot under- or over-approximate the live search.
//
// The northstar (zoekt-2026-redesign.md, ADD "Compressed self-indexes") and the
// research synthesis (research/fm-index-cold-tier.md) are explicit that an
// FM-index is NOT warranted at moedex's ~8GB single-node scale and that nothing
// should be wired in now. This package's job is the de-risking the verdict asks
// for: prove the structure correct and report its real size/latency without
// overselling. In particular, the refuted "FM-index is only 44% of the corpus"
// inference (do NOT claim FM < trigram) is honored — the size measurement
// (fmindex_bench_test.go) reports raw, entropy-dependent ratios as-is.
//
// # The count-cheap / locate-expensive asymmetry
//
// Count is the FM-index's sweet spot: backward search is O(m) in the pattern
// length m and independent of corpus size n. Locate is markedly more expensive:
// each reported occurrence requires an LF-walk back to a sampled suffix-array
// position, so locate cost scales with (match count × sample spacing). For a
// literal that occurs many times (common in code: "func", "return"), that tax
// is paid per hit — exactly where positional trigram intersection is already
// cheap. The benchmarks surface this honestly rather than hiding it.
//
// # The 0x00 sentinel invariant
//
// A BWT needs a unique terminator that sorts strictly before every other
// symbol. This PoC uses the byte 0x00. moedex's ingest (internal/ingest) skips
// any blob containing a NUL byte (it treats NUL as ripgrep's binary marker), so
// real indexed content never contains 0x00 and the sentinel is safe. Build
// nonetheless rejects NUL-containing input with an explicit error so the
// precondition is enforced, not assumed.
//
// # The unbuilt CandidateSource seam
//
// research/fm-index-cold-tier.md §2 describes the only parity-preserving way an
// FM-index could ever be wired in: as an alternative candidate-fetch backend
// (literal/n-gram extraction → FM-index Locate → intersect → the SAME,
// authoritative verify pass), selected per-shard by tier behind a shared
// CandidateSource interface, with internal/search/parity_test.go +
// mmap_parity_test.go required green against both backends as the gate. That
// seam is documented here and deliberately NOT built.
//
// # Deferred (enumerated, not faked)
//
//   - Suffix-array construction uses a simple comparison sort (correct, but
//     O(n·m·log n)); production would swap in SA-IS / DC3. PoC fixtures stay
//     small.
//   - Rank uses checkpointed per-symbol counts (correct, space-bounded, but
//     O(alphabet) per checkpoint); a wavelet tree is the production rank
//     structure.
//   - In-memory only: no on-disk serialization / mmap (the diskstore analog).
//   - No CandidateSource / search integration / regex path.
package fmindex

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
)

// sentinel is the BWT terminator. It must be unique in the text and sort
// strictly before every real byte; 0x00 satisfies both because ingest
// guarantees indexed content is NUL-free (see package doc).
const sentinel = 0x00

// alphabetSize is the full byte alphabet plus the sentinel. The sentinel reuses
// value 0, so the alphabet is just the 256 byte values; treating bytes as
// unsigned 0..255 throughout is essential (a signed-byte comparison is the
// classic FM-index correctness bug).
const alphabetSize = 256

// DefaultSACheckpoint and DefaultSASample are the structure's two space/time
// knobs. The rank checkpoint stride trades rank-probe latency for checkpoint
// memory (O(alphabet) per checkpoint). The suffix-array sample stride trades
// Locate latency for sample memory. Both are correctness-invariant: results are
// identical at any stride (asserted by TestSamplingRateInvariance).
const (
	DefaultSACheckpoint = 64
	DefaultSASample     = 32
)

// ErrNULInput is returned by Build when the input contains the 0x00 byte, which
// collides with the BWT sentinel. moedex's ingest never produces such content.
var ErrNULInput = errors.New("fmindex: input contains NUL (0x00), which collides with the BWT sentinel")

// FMIndex is an immutable FM-index over a byte text. Build it with Build; query
// it with Count and Locate. It is safe for concurrent read use after Build.
type FMIndex struct {
	n   int // length of text including the sentinel
	bwt []byte

	// c[ch] is the number of symbols in the text strictly less than ch (the
	// start of ch's run in the conceptual first column F).
	c [alphabetSize + 1]int

	// checkpoint stores, every saCheckpoint BWT positions, the cumulative count
	// of each symbol in bwt[:pos]. checkpoint[k][ch] = occurrences of ch in
	// bwt[:k*saCheckpoint]. checkpoint[0] is all zeros.
	checkpoint   [][alphabetSize]int
	saCheckpoint int

	// sampledSA[row] is the original-text position SA[row] for rows whose
	// suffix-array value is a multiple of saSample; sampledRows[row] reports
	// whether row is sampled. Locate LF-walks to the nearest sampled row.
	sampledSA   map[int]int
	saSample    int
}

// Build constructs an FM-index over a copy of text. It returns ErrNULInput if
// text contains 0x00. The default checkpoint/sample strides are used; see
// BuildWithParams to override them (tests exercise both).
func Build(text []byte) (*FMIndex, error) {
	return BuildWithParams(text, DefaultSACheckpoint, DefaultSASample)
}

// BuildWithParams is Build with explicit checkpoint and sample strides (both
// must be >= 1). It exists so tests can prove correctness is independent of the
// strides; callers should normally use Build.
func BuildWithParams(text []byte, saCheckpoint, saSample int) (*FMIndex, error) {
	if saCheckpoint < 1 {
		return nil, fmt.Errorf("fmindex: saCheckpoint must be >= 1, got %d", saCheckpoint)
	}
	if saSample < 1 {
		return nil, fmt.Errorf("fmindex: saSample must be >= 1, got %d", saSample)
	}
	if bytes.IndexByte(text, sentinel) >= 0 {
		return nil, ErrNULInput
	}

	// t = text + sentinel. Copy so the index owns its bytes.
	n := len(text) + 1
	t := make([]byte, n)
	copy(t, text)
	t[n-1] = sentinel

	sa := buildSuffixArray(t)

	// BWT[i] = t[(SA[i]-1+n) mod n]: the byte preceding the i-th sorted suffix
	// (cyclically). At SA[i]==0 the predecessor wraps to the sentinel at n-1.
	bwt := make([]byte, n)
	for i, s := range sa {
		if s == 0 {
			bwt[i] = t[n-1]
		} else {
			bwt[i] = t[s-1]
		}
	}

	fm := &FMIndex{
		n:            n,
		bwt:          bwt,
		saCheckpoint: saCheckpoint,
		saSample:     saSample,
		sampledSA:    make(map[int]int),
	}

	// C[]: total occurrences of each byte in the BWT (== in t), then prefix-sum
	// into "count of symbols strictly less than ch".
	var counts [alphabetSize]int
	for _, ch := range bwt {
		counts[ch]++
	}
	total := 0
	for ch := 0; ch < alphabetSize; ch++ {
		fm.c[ch] = total
		total += counts[ch]
	}
	fm.c[alphabetSize] = total

	// Checkpointed rank: snapshot cumulative per-symbol counts every
	// saCheckpoint positions. checkpoint[k] covers bwt[:k*saCheckpoint].
	numCheckpoints := n/saCheckpoint + 1
	fm.checkpoint = make([][alphabetSize]int, numCheckpoints)
	var running [alphabetSize]int
	for pos := 0; pos < n; pos++ {
		if pos%saCheckpoint == 0 {
			fm.checkpoint[pos/saCheckpoint] = running
		}
		running[bwt[pos]]++
	}
	// Any trailing checkpoint slot(s) not hit by the loop carry the final totals.
	for k := (n + saCheckpoint - 1) / saCheckpoint; k < numCheckpoints; k++ {
		fm.checkpoint[k] = running
	}

	// Sample the suffix array: store SA[row] for rows whose text position is a
	// multiple of saSample. Locate LF-walks to the nearest such row.
	for row, s := range sa {
		if s%saSample == 0 {
			fm.sampledSA[row] = s
		}
	}

	return fm, nil
}

// buildSuffixArray returns the suffix array of t (which ends in the unique
// sentinel) as the start indices of t's suffixes in ascending lexicographic
// order. It uses a simple comparison sort: correct and easy to audit, but
// O(n·m·log n). Production would swap in SA-IS / DC3; correctness, not speed,
// is the PoC bar (see package doc, "Deferred").
func buildSuffixArray(t []byte) []int {
	n := len(t)
	sa := make([]int, n)
	for i := range sa {
		sa[i] = i
	}
	sort.Slice(sa, func(a, b int) bool {
		// Compare suffixes t[sa[a]:] and t[sa[b]:]. The unique sentinel at the
		// end guarantees a total order with no suffix a prefix of another, so
		// bytes.Compare on the (unequal-length) tails is exact.
		return bytes.Compare(t[sa[a]:], t[sa[b]:]) < 0
	})
	return sa
}

// rank returns the number of occurrences of ch in bwt[:pos] (i.e. positions
// 0..pos-1). pos is in [0, n]. It reads the nearest preceding checkpoint and
// linearly scans the remaining (< saCheckpoint) bytes.
func (fm *FMIndex) rank(ch byte, pos int) int {
	cp := pos / fm.saCheckpoint
	r := fm.checkpoint[cp][ch]
	for i := cp * fm.saCheckpoint; i < pos; i++ {
		if fm.bwt[i] == ch {
			r++
		}
	}
	return r
}

// lf is the LF-mapping: the row in the first column F corresponding to the
// symbol bwt[row]. lf(row) = C[bwt[row]] + rank(bwt[row], row). It is a
// permutation of [0, n) (asserted by TestLFIsPermutation).
func (fm *FMIndex) lf(row int) int {
	ch := fm.bwt[row]
	return fm.c[ch] + fm.rank(ch, row)
}

// backwardSearch returns the half-open suffix-array row range [sp, ep) of all
// suffixes prefixed by pattern. An empty range (sp >= ep) means no match; an
// empty pattern yields an empty range (matching index/suffixarray.Lookup, which
// returns no positions for "").
func (fm *FMIndex) backwardSearch(pattern []byte) (sp, ep int) {
	if len(pattern) == 0 {
		return 0, 0
	}
	// Initialize with the last pattern byte's run in F.
	ch := pattern[len(pattern)-1]
	sp = fm.c[ch]
	ep = fm.c[int(ch)+1]
	for i := len(pattern) - 2; i >= 0 && sp < ep; i-- {
		ch = pattern[i]
		sp = fm.c[ch] + fm.rank(ch, sp)
		ep = fm.c[ch] + fm.rank(ch, ep)
	}
	if sp >= ep {
		return 0, 0
	}
	return sp, ep
}

// Count returns the number of (possibly overlapping) occurrences of pattern in
// the text. It is O(len(pattern)) and independent of corpus size — the
// FM-index's sweet spot. An empty pattern returns 0.
func (fm *FMIndex) Count(pattern []byte) int {
	sp, ep := fm.backwardSearch(pattern)
	return ep - sp
}

// Locate returns the sorted, deduplicated 0-based byte offsets of every
// occurrence of pattern in the original text (sentinel excluded). It is
// markedly more expensive than Count: each occurrence costs an LF-walk to the
// nearest sampled suffix-array position (see package doc, the count/locate
// asymmetry). An empty pattern returns nil.
func (fm *FMIndex) Locate(pattern []byte) []int {
	sp, ep := fm.backwardSearch(pattern)
	if sp >= ep {
		return nil
	}
	out := make([]int, 0, ep-sp)
	for row := sp; row < ep; row++ {
		// Walk LF until we reach a sampled row, counting steps. LF moves one
		// position backward in the text (text pos p -> p-1), so the walked
		// position is sampledValue + steps. Position 0 is a multiple of every
		// stride and is therefore always sampled, so the walk always terminates
		// (in at most saSample steps when sampling is dense, fewer for nearer
		// sampled positions).
		steps := 0
		r := row
		for {
			if s, ok := fm.sampledSA[r]; ok {
				out = append(out, s+steps)
				break
			}
			r = fm.lf(r)
			steps++
		}
	}
	sort.Ints(out)
	return out
}

// Len returns the text length (excluding the sentinel).
func (fm *FMIndex) Len() int { return fm.n - 1 }
