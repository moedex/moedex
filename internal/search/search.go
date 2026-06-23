// Package search runs queries against the index and returns line-granular
// matches, defined to be identical to ripgrep's default line-oriented matching
// so correctness can be checked by parity.
package search

import (
	"bytes"
	"regexp"
	"regexp/syntax"
	"sort"
	"unicode"

	"moedex/internal/index"
	"moedex/internal/query"
	"moedex/internal/trigram"
)

// Match is one matching line in one file.
type Match struct {
	AbsPath string
	Repo    string
	RelPath string
	Line    int
}

// Literal finds every line containing the literal q. It uses the positional
// begin-gram/end-gram intersection to pick candidate (blob, offset) pairs —
// intersecting just two posting lists and verifying positional distance — then
// confirms the full substring before recording a match.
func Literal(ix *index.Index, q string) []Match {
	qb := []byte(q)
	var matches []Match

	if len(qb) < trigram.N {
		// No trigram available; scan every blob line by line. Operate on bytes
		// (bytes.Contains is SIMD-accelerated in the stdlib) and avoid the
		// per-blob string conversion + slice allocation that strings.Split did.
		for id := 0; id < ix.NumBlobs(); id++ {
			b := ix.Blob(uint64(id))
			forEachLine(b.Content, func(li int, line []byte) {
				if bytes.Contains(line, qb) {
					matches = appendRefs(matches, b, li+1)
				}
			})
		}
		return dedupe(matches)
	}

	begin := trigram.Trigram{qb[0], qb[1], qb[2]}
	end := trigram.Trigram{qb[len(qb)-3], qb[len(qb)-2], qb[len(qb)-1]}
	off := len(qb) - trigram.N

	// Positional intersection via merge-join. Both posting lists are sorted by
	// (Blob, Offset) (see index.AddFile), so instead of building a cache-hostile
	// map[uint64]map[int]bool we walk them in lockstep: for each begin-gram we
	// look for an end-gram at the same blob with Offset == begin.Offset+off. The
	// target key (begin.Blob, begin.Offset+off) is monotonically non-decreasing
	// as we advance through the sorted begin list, so a single forward cursor
	// over the end list suffices — O(n+m) time, zero map allocation.
	begins := ix.Postings(begin)
	ends := ix.Postings(end)
	j := 0
	for _, p := range begins {
		want := p.Offset + off
		// Advance the end cursor to the first posting that is >= (p.Blob, want)
		// in (Blob, Offset) order.
		for j < len(ends) && (ends[j].Blob < p.Blob || (ends[j].Blob == p.Blob && ends[j].Offset < want)) {
			j++
		}
		if j < len(ends) && ends[j].Blob == p.Blob && ends[j].Offset == want {
			b := ix.Blob(p.Blob)
			// bytes.Equal is SIMD-optimized in the stdlib on amd64 and arm64.
			// Guard the upper bound; the positional intersection guarantees the
			// begin-gram fits, but a malformed/truncated end could overrun.
			if pos := p.Offset; pos+len(qb) <= len(b.Content) && bytes.Equal(b.Content[pos:pos+len(qb)], qb) {
				matches = appendRefs(matches, b, b.LineOf(pos))
			}
		}
	}
	return dedupe(matches)
}

// Regex finds every line matching pattern. The trigram query selects candidate
// blobs; the real regex engine then verifies, line by line, to match ripgrep's
// default semantics exactly.
func Regex(ix *index.Index, pattern string) ([]Match, error) {
	q, err := query.FromRegexp(pattern)
	if err != nil {
		return nil, err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}

	// Literal prefilter: every matching line must contain at least one literal
	// from a set that is REQUIRED by the pattern (see requiredLiterals). A line
	// containing none of them cannot match, so we skip it with a cheap,
	// SIMD-accelerated bytes.Contains instead of entering the (much heavier) RE2
	// engine. When no required literals can be proven (empty set), the prefilter
	// is disabled and every line goes to the full engine — soundness over speed.
	lits := requiredLiterals(pattern)

	var matches []Match
	for _, id := range q.Eval(ix) {
		b := ix.Blob(id)
		forEachLine(b.Content, func(li int, line []byte) {
			if !lits.maybe(line) {
				return // cannot match: no required literal present
			}
			if re.Match(line) {
				matches = appendRefs(matches, b, li+1)
			}
		})
	}
	return dedupe(matches), nil
}

// litSet is a disjunctive prefilter: a line "maybe matches" if it contains at
// least one of the literals. An empty set means "no prefilter" (always maybe).
type litSet [][]byte

func (s litSet) maybe(line []byte) bool {
	if len(s) == 0 {
		return true
	}
	for _, lit := range s {
		if bytes.Contains(line, lit) {
			return true
		}
	}
	return false
}

// requiredLiterals returns a set of literals such that ANY match of pattern
// must contain at least ONE of them, or an empty set if no such guarantee can
// be proven (in which case the caller does not prefilter).
//
// Soundness is the whole game: returning a literal that is NOT actually required
// would drop true matches and break ripgrep parity. We therefore only handle two
// shapes we can prove:
//
//  1. A concatenation with a required literal run somewhere in it (e.g.
//     "func_[0-9]+" -> "func_", "public\s+class" -> "public"/"class"): any match
//     must contain that run verbatim. We pick the single longest required run.
//  2. A top-level alternation where EVERY branch has its own required literal
//     run (e.g. "handler|response|payload"): any match goes through one branch,
//     so it must contain that branch's required literal — hence at least one of
//     the set. If any branch lacks a required literal, the whole alternation is
//     un-prefilterable and we return empty.
//
// Anything else (leading char class with no later literal, optional/star-only
// content, anchors, etc.) yields an empty set: we do not prefilter.
func requiredLiterals(pattern string) litSet {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil
	}
	re = re.Simplify()

	if re.Op == syntax.OpAlternate {
		var set litSet
		for _, sub := range re.Sub {
			s := requiredAny(sub)
			if len(s) == 0 {
				return nil // a branch with no required literal kills the filter
			}
			set = append(set, s...)
		}
		return set
	}
	return requiredAny(re)
}

// requiredAny returns a disjunctive litSet for re: EVERY match of re contains at
// least one member, or nil when none can be proven. A plain literal contributes
// itself; a case-folded literal contributes the case-variant trigrams of a clean
// position (foldedLiteralPrefilter); a concat contributes its most selective
// child's set. It deliberately does NOT descend OpAlternate / OpStar / OpQuest /
// zero-min OpRepeat, since a literal under those is not guaranteed in a match.
func requiredAny(re *syntax.Regexp) litSet {
	switch re.Op {
	case syntax.OpLiteral:
		// A case-folded literal (e.g. from (?i)) matches several concrete byte
		// strings; a single case-sensitive run would under-approximate. Instead we
		// require one of its case-variant trigrams (sound; see foldedLiteralPrefilter).
		if re.Flags&syntax.FoldCase != 0 {
			return foldedLiteralPrefilter(re.Rune)
		}
		// Runes -> UTF-8 bytes (the index and content are byte-oriented).
		s := []byte(string(re.Rune))
		if len(s) == 0 {
			return nil
		}
		return litSet{s}
	case syntax.OpCapture:
		return requiredAny(re.Sub[0])
	case syntax.OpConcat:
		var best litSet
		for _, sub := range re.Sub {
			if s := requiredAny(sub); moreSelective(s, best) {
				best = s
			}
		}
		return best
	case syntax.OpPlus:
		// x+ requires at least one x, so x's required literals are required.
		return requiredAny(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min >= 1 {
			return requiredAny(re.Sub[0])
		}
		return nil
	default:
		// OpCharClass, OpAnyChar, OpStar, OpQuest, OpEmpty, anchors, OpAlternate,
		// etc.: no literal is guaranteed.
		return nil
	}
}

// moreSelective reports whether prefilter set a beats b: any non-empty set beats
// empty; otherwise prefer the set whose SHORTEST member is longer (a longer
// required substring skips more lines), then the one with fewer alternatives.
func moreSelective(a, b litSet) bool {
	switch {
	case len(a) == 0:
		return false
	case len(b) == 0:
		return true
	case minLen(a) != minLen(b):
		return minLen(a) > minLen(b)
	default:
		return len(a) < len(b)
	}
}

func minLen(s litSet) int {
	m := -1
	for _, x := range s {
		if m < 0 || len(x) < m {
			m = len(x)
		}
	}
	return m
}

// foldedLiteralPrefilter returns the case-variant trigrams of the FIRST clean
// trigram position of a case-insensitive ASCII literal: a sound disjunctive
// prefilter, since every (?i)-match of the literal contains a case variant and
// thus one of these 3-byte sequences. It returns nil — disabling the prefilter,
// always safe — when the literal is non-ASCII, shorter than a trigram, or has no
// clean position (every trigram touches a rune whose fold orbit leaves ASCII, e.g.
// k<->U+212A Kelvin or s<->U+017F long-s). This mirrors the query reduction's fold
// soundness boundary (internal/query) exactly.
func foldedLiteralPrefilter(runes []rune) litSet {
	for _, r := range runes {
		if r >= 0x80 {
			return nil // multi-byte fold orbits can't be a single aligned byte run
		}
	}
	if len(runes) < trigram.N {
		return nil
	}
	variants := make([][]byte, len(runes))
	clean := make([]bool, len(runes))
	for i, r := range runes {
		variants[i], clean[i] = asciiFoldVariants(r)
	}
	for i := 0; i+trigram.N <= len(runes); i++ {
		if !clean[i] || !clean[i+1] || !clean[i+2] {
			continue
		}
		var set litSet
		for _, a := range variants[i] {
			for _, b := range variants[i+1] {
				for _, c := range variants[i+2] {
					set = append(set, []byte{a, b, c})
				}
			}
		}
		return set // first clean position is the tightest sound disjunction
	}
	return nil
}

// asciiFoldVariants returns the distinct ASCII bytes an ASCII rune can take under
// Go's (?i) folding, plus whether its ENTIRE fold orbit stays ASCII (false for
// k/s, whose orbits include U+212A / U+017F).
func asciiFoldVariants(r rune) (bytes []byte, allASCII bool) {
	allASCII = true
	for c := r; ; {
		if c < 0x80 {
			bytes = append(bytes, byte(c))
		} else {
			allASCII = false
		}
		c = unicode.SimpleFold(c)
		if c == r {
			break
		}
	}
	return bytes, allASCII
}

func appendRefs(matches []Match, b *index.Blob, line int) []Match {
	for _, f := range b.Files {
		matches = append(matches, Match{AbsPath: f.AbsPath, Repo: f.Repo, RelPath: f.RelPath, Line: line})
	}
	return matches
}

// forEachLine invokes fn(lineIndex, line) for each line of content, scanning on
// bytes (bytes.IndexByte is SIMD-accelerated) and never allocating a per-blob
// string or line slice. lineIndex is 0-based; line excludes the '\n'.
//
// It reproduces ripgrep's line counting EXACTLY, matching the old splitLines:
// an empty file has no lines, and a trailing newline does NOT produce a final
// empty line. Concretely, splitting "a\nb\n" by '\n' yields ["a","b",""] and the
// old code dropped the trailing "" — here we simply stop emitting once we pass
// the final newline, so we emit "a","b" and nothing after, identical behavior.
// Content with no trailing newline ("a\nb") emits "a","b" as well.
func forEachLine(content []byte, fn func(li int, line []byte)) {
	if len(content) == 0 {
		return
	}
	li := 0
	for len(content) > 0 {
		i := bytes.IndexByte(content, '\n')
		if i < 0 {
			// Last line, no trailing newline.
			fn(li, content)
			return
		}
		// content[i] == '\n'. If this newline is the final byte, the segment
		// after it is empty and must NOT be emitted (matches splitLines drop).
		fn(li, content[:i])
		content = content[i+1:]
		li++
	}
}

func dedupe(matches []Match) []Match {
	type key struct {
		path string
		line int
	}
	seen := map[key]Match{}
	for _, m := range matches {
		seen[key{m.AbsPath, m.Line}] = m
	}
	out := make([]Match, 0, len(seen))
	for _, m := range seen {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AbsPath != out[j].AbsPath {
			return out[i].AbsPath < out[j].AbsPath
		}
		return out[i].Line < out[j].Line
	})
	return out
}
