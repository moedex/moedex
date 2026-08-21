// Package contextwin assembles ranked results into the agent-facing context
// window described by the architecture: ranked, deduplicated, symbol-aware,
// token-budgeted context blocks, not raw grep hits."
//
// Given ranked results (which carry salient LineSpans) and the index that holds
// blob content, Assemble:
//
//  1. expands each salient span to its enclosing code block — using real symbol
//     boundaries when Options.EnclosingBytes is wired (cmd/mcp supplies the
//     symbol layer), falling back to a brace/indent heuristic otherwise,
//  2. merges overlapping blocks and blocks separated by at most one blank line
//     within the same file,
//  3. walks results best-first, emitting blocks until the token budget is spent,
//  4. estimates tokens so the whole window fits an agent's budget.
//
// CONTRACT FREEZE (Lane C implements behind these signatures):
//
//	type ContextBlock struct { Blob uint64; Repo, RelPath, AbsPath string; StartLine, EndLine int; Text string; Score, Lexical, Dense float64 }
//	type ContextWindow struct { Blocks []ContextBlock; TokenEstimate int; Truncated bool }
//	type Options struct { TokenBudget, ContextLines int }
//	func Assemble(ix *index.Index, results []rank.RankedResult, opts Options) ContextWindow
package contextwin

import (
	"bytes"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"moedex/internal/index"
	"moedex/internal/rank"
)

// Defaults applied when Options carries non-positive values.
const (
	// DefaultTokenBudget caps the whole window when opts.TokenBudget <= 0.
	DefaultTokenBudget = 8000
	// DefaultContextLines pads each salient span when opts.ContextLines <= 0.
	DefaultContextLines = 3
	// tokensPerChar drives TokenEstimate: tokens ~= ceil(chars / charsPerToken).
	charsPerToken = 4
	// importSelectionMultiplier softly demotes import-dominated blocks while
	// keeping a strongly relevant dependency/header result reachable.
	importSelectionMultiplier = 0.4
	// testSelectionMultiplier softly demotes requested test-path conventions
	// while keeping test-focused results reachable.
	testSelectionMultiplier = 0.5
)

// ContextBlock is one contiguous, deduplicated slice of a file selected for the
// agent. Lines are 1-based inclusive.
type ContextBlock struct {
	Blob      uint64 // content identity of the contributing blob (cross-call/source dedup key)
	Repo      string
	RelPath   string
	AbsPath   string
	StartLine int
	EndLine   int
	Text      string
	Score     float64 // fused score, inherited from the result that contributed this block
	Lexical   float64 // BM25 component of that result (raw, pre-fusion)
	Dense     float64 // dense cosine component of that result (0 when no dense arm ran)
	Clipped   bool    // true when Text was narrowed to honor the token budget
}

// ContextWindow is the assembled, token-budgeted answer.
type ContextWindow struct {
	Blocks        []ContextBlock
	TokenEstimate int
	Truncated     bool // true if the budget cut off lower-ranked blocks
	Clipped       bool // true if at least one returned block's source was narrowed
}

// Options tunes assembly. TokenBudget caps the whole window; ContextLines is the
// padding added around a salient span before block expansion. Zero values get
// sensible defaults: TokenBudget -> DefaultTokenBudget, ContextLines ->
// DefaultContextLines.
type Options struct {
	TokenBudget  int
	ContextLines int

	// EnclosingBytes, when non-nil, supplies real symbol boundaries to scope a
	// block instead of the brace/indent heuristic. Given a blob ID and a byte
	// offset within that blob, it returns the innermost enclosing symbol's
	// [startByte, endByte) and ok. When it is nil, or returns ok=false for a
	// span, span expansion falls back to the brace/indent heuristic unchanged.
	//
	// It is a func (not a *symbol.Index) so contextwin imports no symbol package
	// and stays unit-testable with a stub; cmd/mcp wires in
	// (*symbol.Index).EnclosingBytesFunc().
	EnclosingBytes func(blob uint64, byteOff int) (startByte, endByte int, ok bool)
}

// candidate is an in-flight block keyed to a file, carried through expansion,
// merge, and the budget walk. It tracks the source result order so the walk can
// stay stable after internal selection penalties are applied.
type candidate struct {
	repo, relPath, absPath string
	startLine, endLine     int    // 1-based inclusive
	content                []byte // blob content this block is sliced from
	view                   *blobView
	score                  float64
	selectionScore         float64 // internal ordering score after composable penalties
	lexical, dense         float64 // per-arm scores of the contributing result (track score)
	order                  int     // index of the originating result (lower = earlier)
	blob                   uint64  // originating blob ID (for symbol scoping)
	salientLine            int     // highest-ranked salient line retained through merging
	salientOrder           int     // originating result order for salient-line tie breaking
	importDominated        bool    // true when >70% of non-blank lines are import/header syntax
	testDominated          bool    // true when relPath matches a requested test convention
}

// blobView is the per-assembly, per-blob line index. It makes all candidate
// classification queries O(1) after one linear scan and supplies the same line
// representation to expansion, slicing, clipping, and byte/line conversion.
type blobView struct {
	content        []byte
	lines          [][]byte
	lineStarts     []int
	nonBlankPrefix []int
	importPrefix   []int
}

func newBlobView(content []byte) *blobView {
	v := &blobView{content: content, lines: splitLines(content)}
	n := len(v.lines)
	v.lineStarts = make([]int, n)
	v.nonBlankPrefix = make([]int, n+1)
	v.importPrefix = make([]int, n+1)

	off := 0
	inGoImportBlock := false
	for i, raw := range v.lines {
		v.lineStarts[i] = off
		off += len(raw) + 1

		trimmed := strings.TrimSpace(string(raw))
		if strings.HasPrefix(trimmed, "import (") || trimmed == "import(" {
			inGoImportBlock = true
		}
		v.nonBlankPrefix[i+1] = v.nonBlankPrefix[i]
		v.importPrefix[i+1] = v.importPrefix[i]
		if trimmed != "" {
			v.nonBlankPrefix[i+1]++
			if inGoImportBlock || isImportLine(trimmed) {
				v.importPrefix[i+1]++
			}
		}
		if inGoImportBlock && trimmed == ")" {
			inGoImportBlock = false
		}
	}
	return v
}

func (v *blobView) importDominated(startLine, endLine int) bool {
	if v == nil || len(v.lines) == 0 {
		return false
	}
	start := clamp(startLine, 1, len(v.lines))
	end := clamp(endLine, start, len(v.lines))
	nonBlank := v.nonBlankPrefix[end] - v.nonBlankPrefix[start-1]
	imports := v.importPrefix[end] - v.importPrefix[start-1]
	return nonBlank > 0 && imports*10 > nonBlank*7
}

func (v *blobView) sliceLines(startLine, endLine int) string {
	if v == nil || len(v.lines) == 0 {
		return ""
	}
	start := clamp(startLine, 1, len(v.lines))
	end := clamp(endLine, start, len(v.lines))
	startByte := v.lineStarts[start-1]
	endByte := len(v.content)
	if end < len(v.lines) {
		endByte = v.lineStarts[end]
	}
	text := v.content[startByte:endByte]
	if len(text) > 0 && text[len(text)-1] == '\n' {
		return string(text)
	}
	return string(text) + "\n"
}

func (v *blobView) lineStart(line int) int {
	if v == nil || len(v.lineStarts) == 0 {
		return 0
	}
	return v.lineStarts[clamp(line, 1, len(v.lineStarts))-1]
}

func (v *blobView) lineOfByte(off int) int {
	if v == nil || len(v.lineStarts) == 0 {
		return 1
	}
	off = clamp(off, 0, len(v.content))
	// lineStarts contains every line's first byte. The number of starts <= off
	// is therefore the containing 1-based line number.
	line := sort.Search(len(v.lineStarts), func(i int) bool { return v.lineStarts[i] > off })
	if line == 0 {
		return 1
	}
	return line
}

// Assemble turns ranked results into a token-budgeted, deduplicated,
// block-scoped context window. Results are consumed best-first; ix supplies the
// blob content the blocks are sliced from.
//
// Block expansion. When Options.EnclosingBytes is wired, each span is scoped to
// its innermost enclosing symbol's real boundaries (see expandSpanScoped). When
// it is nil or reports no enclosing symbol, expansion falls back to the
// approximate brace/indent heuristic below. For each span, after padding by
// ContextLines and clamping to file bounds:
//
//  1. Brace balance. Scan the padded region's lines. If they contain a net
//     positive '{' surplus (more opens than closes), walk the end downward until
//     braces balance or EOF. If a net negative surplus (more closes), walk the
//     start upward until balanced or BOF. Braces inside the region are matched
//     greedily by net count; this ignores braces in strings/comments — a known
//     failure mode.
//  2. Indent fallback. When the region is already brace-balanced (e.g. Python,
//     YAML, or a brace-free snippet), take the minimum indentation among the
//     region's non-blank lines, then extend the start upward and the end
//     downward across the contiguous run of lines whose indentation is >= that
//     minimum (blank lines are permitted inside the run). Finally, pull in one
//     header line above the run if it is non-blank and less-indented (the
//     enclosing def/declaration). This is heuristic: it can over- or
//     under-capture when indentation is irregular or tabs/spaces are mixed.
//
// All line numbers are clamped to [1, lineCount]; a span past EOF is clamped,
// never a panic.
//
// Merge rule: within a single file (keyed by AbsPath) blocks are merged when
// they overlap, touch, or have one blank line between them (that is,
// next.start <= cur.end+2). The merged block spans the union of line ranges and
// keeps the maximum Score of its parts. Blocks in different files never merge.
//
// Budget / truncation rule: blocks are emitted best-first by the internal
// selection score (raw Score with soft import/test penalties); ties use raw
// Score, originating result order, file path, and start line. If the best block
// alone exceeds TokenBudget it is clipped around its highest-ranked salient
// line. Subsequent over-budget blocks are skipped and any skip sets Truncated =
// true. Clipping is reported separately because it narrows returned source
// while truncation omits candidates.
//
// Token formula: tokens(text) = ceil(len(text) / 4). TokenEstimate is the sum
// over emitted blocks' Text.
//
// FileRef reported: a result's Files may hold several refs (content dedup). The
// block reports Files[0] deterministically. Results with no Files or whose Blob
// is absent from the index are skipped.
func Assemble(ix *index.Index, results []rank.RankedResult, opts Options) ContextWindow {
	budget := opts.TokenBudget
	if budget <= 0 {
		budget = DefaultTokenBudget
	}
	ctxLines := opts.ContextLines
	if ctxLines <= 0 {
		ctxLines = DefaultContextLines
	}

	// 1+2. Expand every span into a candidate block, grouped per file.
	byFile := map[string][]candidate{}
	views := map[uint64]*blobView{}
	var fileOrder []string // preserve first-seen file order for determinism
	for order, res := range results {
		if len(res.Files) == 0 {
			continue
		}
		// index.Blob panics on an out-of-range ID, so bounds-check first.
		if ix == nil || res.Blob >= uint64(ix.NumBlobs()) {
			continue
		}
		blob := ix.Blob(res.Blob)
		if blob == nil {
			continue
		}
		view := views[res.Blob]
		if view == nil {
			view = newBlobView(blob.Content)
			views[res.Blob] = view
		}
		if len(view.lines) == 0 {
			continue
		}
		ref := res.Files[0]
		for _, span := range res.LineSpans {
			start, end := expandSpanScopedView(view, res.Blob, span, ctxLines, opts.EnclosingBytes)
			c := candidate{
				repo:         ref.Repo,
				relPath:      ref.RelPath,
				absPath:      ref.AbsPath,
				startLine:    start,
				endLine:      end,
				content:      blob.Content,
				view:         view,
				score:        res.Score,
				lexical:      res.Lexical,
				dense:        res.Dense,
				order:        order,
				blob:         res.Blob,
				salientLine:  clamp(span.StartLine, 1, len(view.lines)),
				salientOrder: order,
			}
			if _, seen := byFile[c.absPath]; !seen {
				fileOrder = append(fileOrder, c.absPath)
			}
			byFile[c.absPath] = append(byFile[c.absPath], c)
		}
	}

	// 3. Merge candidates separated by at most one blank line within each file.
	var merged []candidate
	for _, absPath := range fileOrder {
		merged = append(merged, mergeFile(byFile[absPath])...)
	}
	for i := range merged {
		merged[i].importDominated = isImportDominated(merged[i])
		merged[i].testDominated = isTestDominatedPath(merged[i].relPath)
		merged[i].selectionScore = merged[i].score
		if merged[i].importDominated {
			merged[i].selectionScore *= importSelectionMultiplier
		}
		if merged[i].testDominated {
			merged[i].selectionScore *= testSelectionMultiplier
		}
	}

	// 4. Walk best-first under the token budget.
	sort.SliceStable(merged, func(i, j int) bool {
		a, b := merged[i], merged[j]
		if a.selectionScore != b.selectionScore {
			return a.selectionScore > b.selectionScore
		}
		if a.score != b.score {
			return a.score > b.score // higher score first
		}
		if a.order != b.order {
			return a.order < b.order // earlier result first
		}
		if a.absPath != b.absPath {
			return a.absPath < b.absPath
		}
		return a.startLine < b.startLine
	})

	win := ContextWindow{}
	for _, c := range merged {
		text := c.view.sliceLines(c.startLine, c.endLine)
		cost := estimateTokens(text)
		clipped := false
		if len(win.Blocks) == 0 && cost > budget {
			text, c.startLine, c.endLine = clipAroundSalient(c, budget*charsPerToken)
			cost = estimateTokens(text)
			clipped = true
		}
		if len(win.Blocks) > 0 && win.TokenEstimate+cost > budget {
			// Intentionally continue, not break: a later, lower-scored, smaller
			// candidate may still fit in the remainder of the budget even though
			// this larger one didn't. This packs the window for maximum budget
			// use rather than emitting a strict score-prefix; Truncated records
			// that at least one candidate was skipped for size.
			win.Truncated = true
			continue
		}
		win.Blocks = append(win.Blocks, ContextBlock{
			Blob:      c.blob,
			Repo:      c.repo,
			RelPath:   c.relPath,
			AbsPath:   c.absPath,
			StartLine: c.startLine,
			EndLine:   c.endLine,
			Text:      text,
			Score:     c.score,
			Lexical:   c.lexical,
			Dense:     c.dense,
			Clipped:   clipped,
		})
		win.TokenEstimate += cost
		win.Clipped = win.Clipped || clipped
	}
	return win
}

// splitLines returns the content split on '\n', with the trailing newline
// dropped from each line. A trailing empty segment after a final '\n' is
// removed so line counts match index.Blob.LineOf semantics.
func splitLines(content []byte) [][]byte {
	if len(content) == 0 {
		return nil
	}
	lines := bytes.Split(content, []byte{'\n'})
	// bytes.Split on "a\n" yields ["a", ""]; drop the trailing empty.
	if n := len(lines); n > 0 && len(lines[n-1]) == 0 {
		lines = lines[:n-1]
	}
	return lines
}

// sliceLines returns the text of lines [start,end] (1-based inclusive),
// rejoined with '\n' and terminated with a trailing '\n'.
func sliceLines(content []byte, start, end int) string {
	return newBlobView(content).sliceLines(start, end)
}

// estimateTokens implements ceil(len/4).
func estimateTokens(s string) int {
	return (len(s) + charsPerToken - 1) / charsPerToken
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// expandSpanScoped chooses the block for a span. When enc is non-nil it converts
// the salient span's first line to a byte offset within content and asks for the
// innermost enclosing symbol; on a hit it maps [startByte,endByte) back to
// 1-based inclusive lines and uses that block. When enc is nil or reports no
// enclosing symbol, it falls back to expandSpan's brace/indent heuristic.
//
// The byte offset queried is the start of the span's first line (clamped),
// because the symbol layer keys on definition ranges and the span's opening line
// is the most reliable anchor inside the enclosing definition.
func expandSpanScoped(lines [][]byte, content []byte, blob uint64, span rank.LineSpan, ctxLines int, enc func(uint64, int) (int, int, bool)) (int, int) {
	return expandSpanScopedView(newBlobView(content), blob, span, ctxLines, enc)
}

func expandSpanScopedView(view *blobView, blob uint64, span rank.LineSpan, ctxLines int, enc func(uint64, int) (int, int, bool)) (int, int) {
	lines := view.lines
	if enc != nil {
		n := len(lines)
		startLine := clamp(span.StartLine, 1, n)
		byteOff := view.lineStart(startLine)
		if bs, be, ok := enc(blob, byteOff); ok && be > bs {
			// be is exclusive; subtract 1 to land on the last contained byte so
			// LineOf maps to the line the body actually ends on (not the line
			// after a trailing newline).
			sLine := view.lineOfByte(bs)
			eLine := view.lineOfByte(be - 1)
			sLine = clamp(sLine, 1, n)
			eLine = clamp(eLine, 1, n)
			if eLine < sLine {
				eLine = sLine
			}
			return sLine, eLine
		}
	}
	return expandSpan(lines, span, ctxLines)
}

// clipAroundSalient returns a contiguous source slice no larger than maxBytes,
// anchored on c.salientLine. It admits only complete neighboring lines. When
// the salient line itself is too large, it returns a UTF-8-safe prefix of that
// line. maxBytes is positive for every normalized token budget.
func clipAroundSalient(c candidate, maxBytes int) (text string, startLine, endLine int) {
	view := c.view
	if view == nil {
		view = newBlobView(c.content)
	}
	lines := view.lines
	if len(lines) == 0 || maxBytes <= 0 {
		return "", c.salientLine, c.salientLine
	}
	salient := clamp(c.salientLine, c.startLine, c.endLine)
	line := lines[salient-1]
	if len(line)+1 > maxBytes {
		prefix := utf8Prefix(line, maxBytes)
		return string(prefix), salient, salient
	}

	start, end := salient, salient
	used := len(line) + 1 // sliceLines terminates every complete line with '\n'.
	leftOpen, rightOpen := start > c.startLine, end < c.endLine
	for leftOpen || rightOpen {
		progress := false
		if leftOpen {
			cost := len(lines[start-2]) + 1
			if used+cost <= maxBytes {
				start--
				used += cost
				progress = true
			} else {
				leftOpen = false
			}
			if start <= c.startLine {
				leftOpen = false
			}
		}
		if rightOpen {
			cost := len(lines[end]) + 1
			if used+cost <= maxBytes {
				end++
				used += cost
				progress = true
			} else {
				rightOpen = false
			}
			if end >= c.endLine {
				rightOpen = false
			}
		}
		if !progress && !leftOpen && !rightOpen {
			break
		}
	}
	return view.sliceLines(start, end), start, end
}

// utf8Prefix returns the longest valid UTF-8 prefix no longer than max bytes.
func utf8Prefix(src []byte, max int) []byte {
	if max >= len(src) {
		return src
	}
	if max <= 0 {
		return nil
	}
	end := max
	for end > 0 && !utf8.Valid(src[:end]) {
		end--
	}
	return src[:end]
}

// lineStartByte returns the byte offset of the first byte of the given 1-based
// line, mirroring how splitLines drops the trailing '\n' from each line: each
// line contributes len(line)+1 bytes (the +1 is its terminating newline).
func lineStartByte(lines [][]byte, line int) int {
	off := 0
	for i := 1; i < line && i <= len(lines); i++ {
		off += len(lines[i-1]) + 1
	}
	return off
}

// lineOfByte returns the 1-based line number containing byte offset off, by
// counting newlines before it. Matches index.Blob.LineOf semantics.
func lineOfByte(content []byte, off int) int {
	if off < 0 {
		off = 0
	}
	if off > len(content) {
		off = len(content)
	}
	return 1 + bytes.Count(content[:off], []byte{'\n'})
}

// expandSpan applies the documented brace/indent heuristic. lines is 0-indexed;
// returned start/end are 1-based inclusive and clamped to [1,len(lines)].
func expandSpan(lines [][]byte, span rank.LineSpan, ctxLines int) (int, int) {
	n := len(lines)
	// Anchor the heuristic on the salient span itself (clamped), so padding never
	// pollutes the brace/indent decision.
	hitStart := clamp(span.StartLine, 1, n)
	hitEnd := clamp(span.EndLine, 1, n)
	if hitEnd < hitStart {
		hitEnd = hitStart
	}
	start := clamp(span.StartLine-ctxLines, 1, n)
	end := clamp(span.EndLine+ctxLines, 1, n)
	if end < start {
		end = start
	}

	net := braceNet(lines, hitStart, hitEnd)
	switch {
	case net > 0:
		// More opens than closes in the hit: extend downward to find the closer.
		if d := balanceDown(lines, hitStart, hitEnd, n); d > end {
			end = d
		}
	case net < 0:
		// More closes than opens in the hit: extend upward to find the opener.
		if u := balanceUp(lines, hitStart, hitEnd); u < start {
			start = u
		}
	default:
		// Balanced (or brace-free): fall back to the indentation heuristic,
		// anchored on the hit but never shrinking below the padded region.
		is, ie := expandByIndent(lines, hitStart, hitEnd, n)
		if is < start {
			start = is
		}
		if ie > end {
			end = ie
		}
	}
	return clamp(start, 1, n), clamp(end, 1, n)
}

// braceNet returns (# '{') - (# '}') across lines [start,end] (1-based).
func braceNet(lines [][]byte, start, end int) int {
	net := 0
	for i := start; i <= end; i++ {
		net += bytes.Count(lines[i-1], []byte{'{'}) - bytes.Count(lines[i-1], []byte{'}'})
	}
	return net
}

// balanceDown walks end downward until the running brace balance from start
// reaches zero or EOF is hit.
func balanceDown(lines [][]byte, start, end, n int) int {
	bal := 0
	for i := start; i <= n; i++ {
		bal += bytes.Count(lines[i-1], []byte{'{'}) - bytes.Count(lines[i-1], []byte{'}'})
		if i >= end && bal <= 0 {
			return i
		}
	}
	return n
}

// balanceUp walks start upward until the running brace balance back to end
// reaches zero or BOF is hit.
func balanceUp(lines [][]byte, start, end int) int {
	bal := 0
	for i := end; i >= 1; i-- {
		bal += bytes.Count(lines[i-1], []byte{'}'}) - bytes.Count(lines[i-1], []byte{'{'})
		if i <= start && bal <= 0 {
			return i
		}
	}
	return 1
}

// expandByIndent implements the indent fallback. It computes the minimum
// indentation of non-blank lines in [start,end], extends across the contiguous
// run of lines at >= that indent (blank lines allowed), then pulls in one
// less-indented header line above.
func expandByIndent(lines [][]byte, start, end, n int) (int, int) {
	minIndent := -1
	for i := start; i <= end; i++ {
		if isBlank(lines[i-1]) {
			continue
		}
		ind := indentOf(lines[i-1])
		if minIndent < 0 || ind < minIndent {
			minIndent = ind
		}
	}
	if minIndent <= 0 {
		// All blank, or the hit sits at column 0 (top-level / unindented). There
		// is no deeper enclosing scope to walk out to, and extending across the
		// indent-0 run would swallow the whole file, so leave the span as-is.
		return start, end
	}

	// Extend upward across the contiguous >= minIndent run.
	for start > 1 {
		prev := lines[start-2]
		if isBlank(prev) || indentOf(prev) >= minIndent {
			start--
			continue
		}
		break
	}
	// Extend downward across the contiguous >= minIndent run.
	for end < n {
		next := lines[end]
		if isBlank(next) || indentOf(next) >= minIndent {
			end++
			continue
		}
		break
	}
	// Trim blank lines from both ends of the run so the block hugs real code.
	for start < end && isBlank(lines[start-1]) {
		start++
	}
	for end > start && isBlank(lines[end-1]) {
		end--
	}
	// Pull in one header line above the run, if it is less-indented and non-blank.
	if start > 1 {
		header := lines[start-2]
		if !isBlank(header) && indentOf(header) < minIndent {
			start--
		}
	}
	return start, end
}

func isBlank(line []byte) bool { return len(bytes.TrimSpace(line)) == 0 }

// indentOf counts leading whitespace columns, treating a tab as one column.
func indentOf(line []byte) int {
	n := 0
	for _, c := range line {
		if c == ' ' || c == '\t' {
			n++
			continue
		}
		break
	}
	return n
}

// mergeFile merges candidates that overlap, touch, or have one blank line
// between them. Input order need not be sorted; output is sorted by start line.
// A merged candidate keeps the max Score (and that result's
// Lexical/Dense/order) of its parts.
func mergeFile(cands []candidate) []candidate {
	if len(cands) == 0 {
		return nil
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].startLine != cands[j].startLine {
			return cands[i].startLine < cands[j].startLine
		}
		return cands[i].endLine < cands[j].endLine
	})
	out := []candidate{cands[0]}
	for _, next := range cands[1:] {
		cur := &out[len(out)-1]
		// Inclusive ranges separated by one blank line have start=end+2.
		if next.startLine > cur.endLine+2 {
			out = append(out, next)
			continue
		}
		if next.endLine > cur.endLine {
			cur.endLine = next.endLine
		}
		if next.score > cur.score || (next.score == cur.score && next.salientOrder < cur.salientOrder) {
			cur.score = next.score
			cur.lexical = next.lexical
			cur.dense = next.dense
			cur.salientLine = next.salientLine
			cur.salientOrder = next.salientOrder
		}
		if next.order < cur.order {
			cur.order = next.order
		}
	}
	return out
}

func isImportDominated(c candidate) bool {
	view := c.view
	if view == nil {
		view = newBlobView(c.content)
	}
	return view.importDominated(c.startLine, c.endLine)
}

func isTestDominatedPath(relPath string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(relPath, "\\", "/"))
	base := path.Base(normalized)
	if strings.HasSuffix(base, "tests.cs") || strings.HasSuffix(base, ".spec.ts") || strings.HasSuffix(base, "_test.go") {
		return true
	}
	normalized = "/" + strings.Trim(normalized, "/") + "/"
	return strings.Contains(normalized, "/cypress/")
}

func isImportLine(line string) bool {
	return strings.HasPrefix(line, "using ") || strings.HasPrefix(line, "global using ") ||
		strings.HasPrefix(line, "import ") || strings.HasPrefix(line, "import\t") ||
		strings.HasPrefix(line, "from ") && strings.Contains(line, " import ") ||
		strings.HasPrefix(line, "use ") || strings.HasPrefix(line, "#include") ||
		strings.HasPrefix(line, "require(") || strings.HasPrefix(line, "require ")
}
