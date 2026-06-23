// Package search runs queries against the index and returns line-granular
// matches, defined to be identical to ripgrep's default line-oriented matching
// so correctness can be checked by parity.
package search

import (
	"bytes"
	"fmt"
	"regexp"
	"regexp/syntax"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

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

// Stats captures search attribution for profiling. Search callers do not need
// it for correctness; the parity harness uses it to explain latency tails.
type Stats struct {
	QueryAll         bool
	CandidateBlobs   int
	CandidateBytes   int64
	CandidateLines   int64
	LinesAfterFilter int64
	LinesRE2         int64
	LineFilter       string
	ParallelWorkers  int
}

func (s *Stats) add(o Stats) {
	s.CandidateBytes += o.CandidateBytes
	s.CandidateLines += o.CandidateLines
	s.LinesAfterFilter += o.LinesAfterFilter
	s.LinesRE2 += o.LinesRE2
	if o.ParallelWorkers > s.ParallelWorkers {
		s.ParallelWorkers = o.ParallelWorkers
	}
}

const verifyParallelThreshold = 32

var verifyPermits = make(chan struct{}, max(1, runtime.NumCPU()-1))

// Literal finds every line containing the literal q. It uses the positional
// begin-gram/end-gram intersection to pick candidate (blob, offset) pairs —
// intersecting just two posting lists and verifying positional distance — then
// confirms the full substring before recording a match.
func Literal(ix *index.Index, q string) []Match {
	matches, _ := LiteralWithStats(ix, q)
	return matches
}

// LiteralWithStats is Literal plus profiling attribution.
func LiteralWithStats(ix *index.Index, q string) ([]Match, Stats) {
	qb := []byte(q)
	var matches []Match
	stats := Stats{LineFilter: "literal-positional", ParallelWorkers: 1}

	if len(qb) < trigram.N {
		// No trigram available; scan every blob line by line. Operate on bytes
		// (bytes.Contains is SIMD-accelerated in the stdlib) and avoid the
		// per-blob string conversion + slice allocation that strings.Split did.
		ids := make([]uint64, ix.NumBlobs())
		for i := range ids {
			ids[i] = uint64(i)
		}
		matches, stats = scanLiteralLines(ix, ids, qb)
		stats.CandidateBlobs = len(ids)
		stats.LineFilter = "literal-subtrigram"
		stats.QueryAll = false
		return dedupe(matches), stats
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
	seenCandidate := map[uint64]bool{}
	for _, p := range begins {
		want := p.Offset + off
		// Advance the end cursor to the first posting that is >= (p.Blob, want)
		// in (Blob, Offset) order.
		for j < len(ends) && (ends[j].Blob < p.Blob || (ends[j].Blob == p.Blob && ends[j].Offset < want)) {
			j++
		}
		if j < len(ends) && ends[j].Blob == p.Blob && ends[j].Offset == want {
			b := ix.Blob(p.Blob)
			if !seenCandidate[p.Blob] {
				seenCandidate[p.Blob] = true
				stats.CandidateBlobs++
				stats.CandidateBytes += int64(len(b.Content))
				stats.CandidateLines += countLines(b.Content)
			}
			// bytes.Equal is SIMD-optimized in the stdlib on amd64 and arm64.
			// Guard the upper bound; the positional intersection guarantees the
			// begin-gram fits, but a malformed/truncated end could overrun.
			if pos := p.Offset; pos+len(qb) <= len(b.Content) && bytes.Equal(b.Content[pos:pos+len(qb)], qb) {
				matches = appendRefs(matches, b, b.LineOf(pos))
			}
		}
	}
	return dedupe(matches), stats
}

// Regex finds every line matching pattern. The trigram query selects candidate
// blobs; the real regex engine then verifies, line by line, to match ripgrep's
// default semantics exactly.
func Regex(ix *index.Index, pattern string) ([]Match, error) {
	matches, _, err := RegexWithStats(ix, pattern)
	return matches, err
}

// RegexWithStats is Regex plus profiling attribution.
func RegexWithStats(ix *index.Index, pattern string) ([]Match, Stats, error) {
	q, err := query.FromRegexp(pattern)
	if err != nil {
		return nil, Stats{}, err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, Stats{}, err
	}

	// Literal prefilter: every matching line must contain at least one literal
	// from a set that is REQUIRED by the pattern (see requiredLiterals). A line
	// containing none of them cannot match, so we skip it with a cheap,
	// SIMD-accelerated bytes.Contains instead of entering the (much heavier) RE2
	// engine. When no required literals can be proven (empty set), the prefilter
	// is disabled and every line goes to the full engine — soundness over speed.
	filter := requiredLineFilter(pattern)

	// Positional fast path: when the prefilter reduces to trigram-length literals
	// (the case-insensitive "h" tail, and short literal alternations), the lines
	// a required trigram can occur on come straight from the positional postings
	// — exactly like Literal's begin/end-gram jump. RE2 then runs only on those
	// few lines, so we never scan the content of every candidate blob. Rare
	// tokens have tiny posting lists, so this collapses the multi-second
	// content-scan tail to milliseconds.
	if matches, stats, ok := regexPositional(ix, re, filter); ok {
		return dedupe(matches), stats, nil
	}

	ids := q.Eval(ix)
	matches, stats := scanRegexLines(ix, ids, re, filter)
	stats.QueryAll = q.String() == "ALL"
	stats.CandidateBlobs = len(ids)
	stats.LineFilter = filterString(filter)
	return dedupe(matches), stats, nil
}

const (
	// positionalSelectiveEnough stops AND driver-probing once a position's
	// postings are this few: it's already selective, more probes won't pay.
	positionalSelectiveEnough = 2048
	// maxPositionalPostings bounds the positional path. Above this the driver
	// trigram is common enough that decoding its postings and RE2-ing every line
	// is no better than the bounded content scan, so we fall back.
	maxPositionalPostings = 1 << 18 // 262144
)

// regexPositional verifies a regex from the positional postings instead of a
// content scan, when the prefilter reduces to trigram-length literals. It
// returns ok=false (caller falls back to scanRegexLines) for any filter with a
// non-trigram literal leaf, a rune class, or no filter, and when the driver
// posting set is too large to beat the scan path.
//
// Soundness: positionalCandidates returns posting lists whose union of
// (blob, offset) positions is a necessary condition — every line the pattern can
// match contains a driver trigram at some offset, so that offset's posting maps
// to the line. RE2 then confirms, so the match set is identical to the scan.
func regexPositional(ix *index.Index, re *regexp.Regexp, filter lineFilter) ([]Match, Stats, bool) {
	tris, ok := positionalTrigrams(ix, filter)
	if !ok {
		return nil, Stats{}, false
	}
	// Cheap cap check via PostingCount before decoding anything: a driver this
	// common is no cheaper than the bounded content scan, so fall back.
	total := 0
	for _, t := range tris {
		total += ix.PostingCount(t)
		if total > maxPositionalPostings {
			return nil, Stats{}, false
		}
	}

	// 1. Map the driver postings to a de-duplicated set of candidate lines. This
	// is cheap (a binary search + map insert per posting, no byte scanning), so
	// it stays serial; the byte-heavy verification below is what we parallelize.
	type blobLine struct {
		blob uint64
		line int
	}
	seen := map[blobLine]struct{}{}
	blobs := map[uint64]struct{}{}
	var cands []candidateLine
	for _, t := range tris {
		for _, p := range ix.Postings(t) {
			b := ix.Blob(p.Blob)
			if b == nil {
				continue
			}
			lineNo, line := b.LineAt(p.Offset)
			k := blobLine{p.Blob, lineNo}
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			blobs[p.Blob] = struct{}{}
			cands = append(cands, candidateLine{b: b, lineNo: lineNo, line: line})
		}
	}

	// 2. Verify candidate lines in parallel (filter.maybe re-applies the full
	// AND/OR the single driver position skipped, then RE2 confirms). Both are
	// byte-heavy on minified lines, so this is where the worker split pays.
	matches, vstats := verifyCandidateLines(cands, re, filter)
	stats := Stats{
		LineFilter:       "positional",
		CandidateBlobs:   len(blobs),
		CandidateLines:   int64(len(cands)),
		CandidateBytes:   vstats.CandidateBytes,
		LinesAfterFilter: vstats.LinesAfterFilter,
		LinesRE2:         vstats.LinesRE2,
		ParallelWorkers:  vstats.ParallelWorkers,
	}
	return matches, stats, true
}

// candidateLine is one de-duplicated line a driver trigram occurs on, pending
// full-filter + RE2 verification.
type candidateLine struct {
	b      *index.Blob
	lineNo int
	line   []byte
}

// verifyCandidateLines runs the prefilter and RE2 over candidate lines, split
// across the shared verify-worker pool. Each worker collects its own matches, so
// there is no shared mutable state during the byte-heavy work; results merge at
// the end.
func verifyCandidateLines(cands []candidateLine, re *regexp.Regexp, filter lineFilter) ([]Match, Stats) {
	verify := func(sub []candidateLine) scanResult {
		var res scanResult
		for _, c := range sub {
			if filter != nil && !filter.maybe(c.line) {
				continue
			}
			res.stats.LinesAfterFilter++
			res.stats.LinesRE2++
			res.stats.CandidateBytes += int64(len(c.line))
			if re.Match(c.line) {
				res.matches = appendRefs(res.matches, c.b, c.lineNo)
			}
		}
		return res
	}

	workers, release := claimVerifyWorkers(len(cands))
	defer release()
	if workers <= 1 {
		res := verify(cands)
		res.stats.ParallelWorkers = 1
		return res.matches, res.stats
	}
	// Dynamic work-stealing rather than fixed contiguous ranges: a few minified
	// lines (tens of KB each) can carry almost all the RE2 cost, and they cluster
	// in posting order, so an even split-by-count leaves one worker doing
	// everything. Workers pull chunks from a shared cursor instead, so heavy lines
	// spread across cores regardless of where they sit.
	const chunk = 16
	var next int64
	out := make([]scanResult, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			var res scanResult
			for {
				start := int(atomic.AddInt64(&next, chunk)) - chunk
				if start >= len(cands) {
					break
				}
				end := start + chunk
				if end > len(cands) {
					end = len(cands)
				}
				r := verify(cands[start:end])
				res.matches = append(res.matches, r.matches...)
				res.stats.add(r.stats)
			}
			out[w] = res
		}(w)
	}
	wg.Wait()
	return combineScanResults(out, workers)
}

// positionalTrigrams reduces a prefilter to the trigrams whose postings, unioned,
// are a sound superset of every line the filter could pass — or ok=false when
// the filter is not trigram-reducible. A litSet contributes its members (each
// must be exactly a trigram). An AND needs every child, so any one
// trigram-reducible child is sound; we pick the most selective via the cheap
// PostingCount and stop probing once one is clearly rare. An OR may match via any
// branch, so all branches must be reducible and are unioned. Only the returned
// trigrams are decoded by the caller, so a common position is never materialized
// just to be measured.
func positionalTrigrams(ix *index.Index, f lineFilter) ([]trigram.Trigram, bool) {
	switch v := f.(type) {
	case litSet:
		tris := make([]trigram.Trigram, 0, len(v))
		for _, lit := range v {
			if len(lit) != trigram.N {
				return nil, false
			}
			tris = append(tris, trigram.Trigram{lit[0], lit[1], lit[2]})
		}
		return tris, true
	case allFilter:
		var best []trigram.Trigram
		bestN := -1
		for _, sub := range v {
			tris, ok := positionalTrigrams(ix, sub)
			if !ok {
				continue // a non-trigram child still constrains via RE2
			}
			n := 0
			for _, t := range tris {
				n += ix.PostingCount(t)
			}
			if bestN < 0 || n < bestN {
				best, bestN = tris, n
			}
			if bestN <= positionalSelectiveEnough {
				break // already selective enough; skip the remaining positions
			}
		}
		if bestN < 0 {
			return nil, false
		}
		return best, true
	case orFilter:
		var tris []trigram.Trigram
		for _, sub := range v {
			st, ok := positionalTrigrams(ix, sub)
			if !ok {
				return nil, false // an unbounded branch makes the union unbounded
			}
			tris = append(tris, st...)
		}
		return tris, true
	default:
		return nil, false // runeClassFilter, nil, etc.
	}
}

type scanResult struct {
	matches []Match
	stats   Stats
}

func scanLiteralLines(ix *index.Index, ids []uint64, needle []byte) ([]Match, Stats) {
	workers, release := claimVerifyWorkers(len(ids))
	defer release()
	if workers <= 1 {
		res := scanLiteralRange(ix, ids, needle)
		res.stats.ParallelWorkers = 1
		return res.matches, res.stats
	}

	out := make([]scanResult, workers)
	var wg sync.WaitGroup
	for w, r := range splitRanges(len(ids), workers) {
		wg.Add(1)
		go func(w, start, end int) {
			defer wg.Done()
			out[w] = scanLiteralRange(ix, ids[start:end], needle)
		}(w, r.start, r.end)
	}
	wg.Wait()
	return combineScanResults(out, workers)
}

func scanLiteralRange(ix *index.Index, ids []uint64, needle []byte) scanResult {
	var res scanResult
	for _, id := range ids {
		b := ix.Blob(id)
		res.stats.CandidateBytes += int64(len(b.Content))
		forEachLine(b.Content, func(li int, line []byte) {
			res.stats.CandidateLines++
			if bytes.Contains(line, needle) {
				res.stats.LinesAfterFilter++
				res.matches = appendRefs(res.matches, b, li+1)
			}
		})
	}
	return res
}

func scanRegexLines(ix *index.Index, ids []uint64, re *regexp.Regexp, filter lineFilter) ([]Match, Stats) {
	workers, release := claimVerifyWorkers(len(ids))
	defer release()
	if workers <= 1 {
		res := scanRegexRange(ix, ids, re, filter)
		res.stats.ParallelWorkers = 1
		return res.matches, res.stats
	}

	out := make([]scanResult, workers)
	var wg sync.WaitGroup
	for w, r := range splitRanges(len(ids), workers) {
		wg.Add(1)
		go func(w, start, end int) {
			defer wg.Done()
			out[w] = scanRegexRange(ix, ids[start:end], re, filter)
		}(w, r.start, r.end)
	}
	wg.Wait()
	return combineScanResults(out, workers)
}

func scanRegexRange(ix *index.Index, ids []uint64, re *regexp.Regexp, filter lineFilter) scanResult {
	var res scanResult
	for _, id := range ids {
		b := ix.Blob(id)
		res.stats.CandidateBytes += int64(len(b.Content))
		forEachLine(b.Content, func(li int, line []byte) {
			res.stats.CandidateLines++
			if filter != nil && !filter.maybe(line) {
				return // cannot match: no required literal/filter expression present
			}
			res.stats.LinesAfterFilter++
			res.stats.LinesRE2++
			if re.Match(line) {
				res.matches = appendRefs(res.matches, b, li+1)
			}
		})
	}
	return res
}

func combineScanResults(results []scanResult, workers int) ([]Match, Stats) {
	var matches []Match
	stats := Stats{ParallelWorkers: workers}
	for _, res := range results {
		matches = append(matches, res.matches...)
		stats.add(res.stats)
	}
	stats.ParallelWorkers = workers
	return matches, stats
}

type indexRange struct{ start, end int }

func splitRanges(n, workers int) []indexRange {
	if workers < 1 {
		workers = 1
	}
	if workers > n {
		workers = n
	}
	if n == 0 {
		return nil
	}
	ranges := make([]indexRange, 0, workers)
	for w := 0; w < workers; w++ {
		start := w * n / workers
		end := (w + 1) * n / workers
		if start < end {
			ranges = append(ranges, indexRange{start: start, end: end})
		}
	}
	return ranges
}

func claimVerifyWorkers(items int) (int, func()) {
	if items < verifyParallelThreshold {
		return 1, func() {}
	}
	limit := runtime.GOMAXPROCS(0)
	if limit < 1 {
		limit = 1
	}
	if limit > items {
		limit = items
	}
	acquired := 0
	for acquired < limit-1 {
		select {
		case verifyPermits <- struct{}{}:
			acquired++
		default:
			return acquired + 1, releaseVerifyWorkers(acquired)
		}
	}
	return acquired + 1, releaseVerifyWorkers(acquired)
}

func releaseVerifyWorkers(n int) func() {
	return func() {
		for i := 0; i < n; i++ {
			<-verifyPermits
		}
	}
}

// lineFilter is a sound boolean prefilter over one candidate line. A nil filter
// means "no prefilter" (always maybe). Implementations may over-admit lines but
// must never reject a line that the full regexp could match.
type lineFilter interface {
	maybe([]byte) bool
	String() string
	lits() litSet
}

// litSet is a disjunctive byte-literal filter: a line maybe matches if it
// contains at least one member.
type litSet [][]byte

func (s litSet) maybe(line []byte) bool {
	if len(s) == 0 {
		return false
	}
	for _, lit := range s {
		if bytes.Contains(line, lit) {
			return true
		}
	}
	return false
}

func (s litSet) String() string {
	if len(s) == 0 {
		return "none"
	}
	min, max := litLenRange(s)
	return fmt.Sprintf("lit-any(n=%d,len=%d-%d)", len(s), min, max)
}

func (s litSet) lits() litSet { return s }

type allFilter []lineFilter

func (f allFilter) maybe(line []byte) bool {
	for _, sub := range f {
		if !sub.maybe(line) {
			return false
		}
	}
	return true
}

func (f allFilter) String() string {
	parts := make([]string, len(f))
	for i, sub := range f {
		parts[i] = sub.String()
	}
	return "all(" + strings.Join(parts, ",") + ")"
}

func (f allFilter) lits() litSet {
	var out litSet
	for _, sub := range f {
		out = append(out, sub.lits()...)
	}
	return out
}

type orFilter []lineFilter

func (f orFilter) maybe(line []byte) bool {
	for _, sub := range f {
		if sub.maybe(line) {
			return true
		}
	}
	return false
}

func (f orFilter) String() string {
	parts := make([]string, len(f))
	for i, sub := range f {
		parts[i] = sub.String()
	}
	return "or(" + strings.Join(parts, ",") + ")"
}

func (f orFilter) lits() litSet {
	var out litSet
	for _, sub := range f {
		out = append(out, sub.lits()...)
	}
	return out
}

type runeClassFilter struct {
	ranges []rune
	count  int
}

func (f runeClassFilter) maybe(line []byte) bool {
	for len(line) > 0 {
		r, size := utf8.DecodeRune(line)
		if runeInRanges(r, f.ranges) {
			return true
		}
		line = line[size:]
	}
	return false
}

func (f runeClassFilter) String() string {
	return fmt.Sprintf("rune-class(n=%d)", f.count)
}

func (f runeClassFilter) lits() litSet { return nil }

func runeInRanges(r rune, ranges []rune) bool {
	for i := 0; i+1 < len(ranges); i += 2 {
		if r >= ranges[i] && r <= ranges[i+1] {
			return true
		}
	}
	return false
}

func litLenRange(s litSet) (int, int) {
	min, max := -1, 0
	for _, b := range s {
		if min < 0 || len(b) < min {
			min = len(b)
		}
		if len(b) > max {
			max = len(b)
		}
	}
	if min < 0 {
		min = 0
	}
	return min, max
}

func filterString(f lineFilter) string {
	if f == nil {
		return "none"
	}
	return f.String()
}

func makeAll(filters ...lineFilter) lineFilter {
	var out []lineFilter
	for _, f := range filters {
		switch v := f.(type) {
		case nil:
			continue
		case allFilter:
			out = append(out, v...)
		default:
			out = append(out, f)
		}
	}
	switch len(out) {
	case 0:
		return nil
	case 1:
		return out[0]
	default:
		return allFilter(out)
	}
}

func makeOr(filters ...lineFilter) lineFilter {
	var out []lineFilter
	for _, f := range filters {
		if f == nil {
			return nil // one unfilterable branch makes the alternation unfilterable
		}
		switch v := f.(type) {
		case orFilter:
			out = append(out, v...)
		default:
			out = append(out, f)
		}
	}
	switch len(out) {
	case 0:
		return nil
	case 1:
		return out[0]
	default:
		return orFilter(out)
	}
}

// requiredLineFilter returns a sound prefilter expression for pattern, or nil
// if no line-level necessary condition can be proven.
//
// Soundness is the whole game: returning a literal that is NOT actually required
// would drop true matches and break ripgrep parity. We therefore only handle
// shapes we can prove:
//
//  1. A concatenation with a required literal run somewhere in it (e.g.
//     "func_[0-9]+" -> "func_", "public\s+class" -> "public"/"class"): any match
//     must contain that run verbatim.
//  2. A top-level alternation where EVERY branch has its own required literal
//     run (e.g. "handler|response|payload"): any match goes through one branch,
//     so it must contain that branch's required literal — hence at least one of
//     the set. If any branch lacks a required literal, the whole alternation is
//     un-prefilterable and we return empty.
//  3. A small, fully enumerable UTF-8 character class: any match must contain
//     one of the class's rune encodings. Huge or pure ASCII classes stay
//     unfiltered; that keeps the prefilter cheap and conservative.
//
// Anything else (optional/star-only content, anchors, etc.) yields an empty set:
// we do not prefilter.
func requiredLineFilter(pattern string) lineFilter {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil
	}
	re = re.Simplify()
	return requiredFilter(re)
}

// requiredLiterals is retained for white-box tests: it flattens the filter's
// literal leaves. Regex execution uses requiredLineFilter.
func requiredLiterals(pattern string) litSet {
	if f := requiredLineFilter(pattern); f != nil {
		return f.lits()
	}
	return nil
}

func requiredFilter(re *syntax.Regexp) lineFilter {
	switch re.Op {
	case syntax.OpLiteral:
		// A case-folded literal (e.g. from (?i)) matches several concrete byte
		// strings; a single case-sensitive run would under-approximate. Instead we
		// require bounded case-variant trigram positions (sound; see
		// foldedLiteralPrefilter).
		if re.Flags&syntax.FoldCase != 0 {
			return foldedLiteralPrefilter(re.Rune)
		}
		// Runes -> UTF-8 bytes (the index and content are byte-oriented).
		s := []byte(string(re.Rune))
		if len(s) == 0 {
			return nil
		}
		return litSet{s}
	case syntax.OpCharClass:
		return charClassPrefilter(re)
	case syntax.OpCapture:
		return requiredFilter(re.Sub[0])
	case syntax.OpConcat:
		var filters []lineFilter
		for _, sub := range re.Sub {
			if f := requiredFilter(sub); f != nil {
				filters = append(filters, f)
			}
		}
		return makeAll(filters...)
	case syntax.OpAlternate:
		filters := make([]lineFilter, 0, len(re.Sub))
		for _, sub := range re.Sub {
			f := requiredFilter(sub)
			if f == nil {
				return nil
			}
			filters = append(filters, f)
		}
		return makeOr(filters...)
	case syntax.OpPlus:
		// x+ requires at least one x, so x's required literals are required.
		return requiredFilter(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min >= 1 {
			return requiredFilter(re.Sub[0])
		}
		return nil
	default:
		// OpAnyChar, OpStar, OpQuest, OpEmpty, anchors, etc.: no literal/filter
		// condition is guaranteed.
		return nil
	}
}

const (
	maxFoldedPrefilterVariants  = 8
	maxFoldedPrefilterPositions = 3
	maxCharClassPrefilterLits   = 64
)

// foldedLiteralPrefilter returns bounded case-variant filters for clean ASCII
// positions in a case-insensitive literal. A clean trigram contributes an OR of
// its case variants; multiple clean positions are ANDed so they must co-occur
// on the same line before RE2 runs. Only when no clean trigram exists do we fall
// back to the longest clean 1-2 byte span. Clean means every rune's full
// SimpleFold orbit stays ASCII, so bytes.Contains cannot reject a Unicode-fold
// match. If no clean span exists, nil disables the prefilter, which is safe.
func foldedLiteralPrefilter(runes []rune) lineFilter {
	for _, r := range runes {
		if r >= 0x80 {
			return nil // multi-byte fold orbits can't be a single aligned byte run
		}
	}
	variants := make([][]byte, len(runes))
	clean := make([]bool, len(runes))
	for i, r := range runes {
		variants[i], clean[i] = asciiFoldVariants(r)
	}

	if len(runes) >= trigram.N {
		var filters []lineFilter
		for i := 0; i+trigram.N <= len(runes); i++ {
			if clean[i] && clean[i+1] && clean[i+2] {
				if s := foldedVariantSet(variants[i : i+trigram.N]); s != nil {
					filters = append(filters, s)
					if len(filters) == maxFoldedPrefilterPositions {
						break
					}
				}
			}
		}
		if len(filters) > 0 {
			return makeAll(filters...)
		}
	}

	bestStart, bestLen := -1, 0
	for i := 0; i < len(runes); i++ {
		if !clean[i] {
			continue
		}
		j := i + 1
		for j < len(runes) && clean[j] && j-i < trigram.N-1 {
			j++
		}
		spanLen := j - i
		if spanLen > bestLen {
			bestStart, bestLen = i, spanLen
		}
	}
	if bestLen == 0 {
		return nil
	}
	return foldedVariantSet(variants[bestStart : bestStart+bestLen])
}

func charClassPrefilter(re *syntax.Regexp) lineFilter {
	if re.Flags&syntax.FoldCase != 0 {
		return nil
	}
	ranges := re.Rune
	count := 0
	hasNonASCII := false
	for i := 0; i+1 < len(ranges); i += 2 {
		lo, hi := ranges[i], ranges[i+1]
		for r := lo; ; r++ {
			if count >= maxCharClassPrefilterLits {
				return nil
			}
			if r >= 0x80 {
				hasNonASCII = true
			}
			count++
			if r == hi {
				break
			}
		}
	}
	if !hasNonASCII || count == 0 {
		return nil
	}
	return runeClassFilter{ranges: append([]rune(nil), ranges...), count: count}
}

func foldedVariantSet(variants [][]byte) litSet {
	set := litSet{nil}
	for _, vars := range variants {
		var next litSet
		for _, prefix := range set {
			for _, b := range vars {
				candidate := append(append([]byte(nil), prefix...), b)
				next = append(next, candidate)
				if len(next) > maxFoldedPrefilterVariants {
					return nil
				}
			}
		}
		set = next
	}
	return set
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

func countLines(content []byte) int64 {
	var n int64
	forEachLine(content, func(int, []byte) { n++ })
	return n
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
