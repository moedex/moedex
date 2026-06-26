// Package contextwin assembles ranked results into the agent-facing context
// window the northstar calls for: "ranked, deduplicated, symbol-aware,
// token-budgeted context blocks, not raw grep hits."
//
// Given ranked results (which carry salient LineSpans) and the index that holds
// blob content, Assemble:
//
//  1. expands each salient span to its enclosing code block — using real symbol
//     boundaries when Options.EnclosingBytes is wired (cmd/mcp supplies the
//     symbol layer), falling back to a brace/indent heuristic otherwise,
//  2. merges overlapping/adjacent blocks within the same file,
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
	"sort"

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
}

// ContextWindow is the assembled, token-budgeted answer.
type ContextWindow struct {
	Blocks        []ContextBlock
	TokenEstimate int
	Truncated     bool // true if the budget cut off lower-ranked blocks
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
// stay stable (best-first by Score, ties broken by original result order).
type candidate struct {
	repo, relPath, absPath string
	startLine, endLine     int    // 1-based inclusive
	content                []byte // blob content this block is sliced from
	score                  float64
	lexical, dense         float64 // per-arm scores of the contributing result (track score)
	order                  int     // index of the originating result (lower = earlier)
	blob                   uint64  // originating blob ID (for symbol scoping)
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
// they overlap OR touch (adjacent, i.e. next.start <= cur.end+1). The merged
// block spans the union of line ranges and keeps the maximum Score of its parts.
// Blocks in different files never merge.
//
// Budget / truncation rule: blocks are emitted best-first (Score desc; ties
// broken by originating result order, then file path, then start line — fully
// deterministic). A block is emitted while the running TokenEstimate plus the
// block's own token cost stays within TokenBudget. The FIRST emitted block is
// always allowed even if it alone exceeds the budget (so the window is never
// empty when results exist); subsequent over-budget blocks are skipped and any
// skip sets Truncated = true.
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
		lines := splitLines(blob.Content)
		if len(lines) == 0 {
			continue
		}
		ref := res.Files[0]
		for _, span := range res.LineSpans {
			start, end := expandSpanScoped(lines, blob.Content, res.Blob, span, ctxLines, opts.EnclosingBytes)
			c := candidate{
				repo:      ref.Repo,
				relPath:   ref.RelPath,
				absPath:   ref.AbsPath,
				startLine: start,
				endLine:   end,
				content:   blob.Content,
				score:     res.Score,
				lexical:   res.Lexical,
				dense:     res.Dense,
				order:     order,
				blob:      res.Blob,
			}
			if _, seen := byFile[c.absPath]; !seen {
				fileOrder = append(fileOrder, c.absPath)
			}
			byFile[c.absPath] = append(byFile[c.absPath], c)
		}
	}

	// 3. Merge overlapping/adjacent candidates within each file.
	var merged []candidate
	for _, absPath := range fileOrder {
		merged = append(merged, mergeFile(byFile[absPath])...)
	}

	// 4. Walk best-first under the token budget.
	sort.SliceStable(merged, func(i, j int) bool {
		a, b := merged[i], merged[j]
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
		text := sliceLines(c.content, c.startLine, c.endLine)
		cost := estimateTokens(text)
		if len(win.Blocks) > 0 && win.TokenEstimate+cost > budget {
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
		})
		win.TokenEstimate += cost
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
	lines := splitLines(content)
	if len(lines) == 0 {
		return ""
	}
	start = clamp(start, 1, len(lines))
	end = clamp(end, 1, len(lines))
	if end < start {
		end = start
	}
	var b bytes.Buffer
	for i := start; i <= end; i++ {
		b.Write(lines[i-1])
		b.WriteByte('\n')
	}
	return b.String()
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
	if enc != nil {
		n := len(lines)
		startLine := clamp(span.StartLine, 1, n)
		byteOff := lineStartByte(lines, startLine)
		if bs, be, ok := enc(blob, byteOff); ok && be > bs {
			// be is exclusive; subtract 1 to land on the last contained byte so
			// LineOf maps to the line the body actually ends on (not the line
			// after a trailing newline).
			sLine := lineOfByte(content, bs)
			eLine := lineOfByte(content, be-1)
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

// mergeFile merges overlapping/adjacent candidates (all from one file). Input
// order need not be sorted; output is sorted by start line.
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
	for _, c := range cands[1:] {
		last := &out[len(out)-1]
		if c.startLine <= last.endLine+1 { // overlap or touch
			if c.endLine > last.endLine {
				last.endLine = c.endLine
			}
			if c.score > last.score {
				last.score = c.score
				last.lexical = c.lexical
				last.dense = c.dense
			}
			if c.order < last.order {
				last.order = c.order
			}
			continue
		}
		out = append(out, c)
	}
	return out
}
