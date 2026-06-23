package contextwin

import (
	"strings"
	"testing"

	"moedex/internal/index"
	"moedex/internal/rank"
)

// This source has a brace INSIDE a string literal, the documented failure mode
// of the brace heuristic. A salient hit deep inside withString should snap to the
// whole function when EnclosingBytes is supplied, but the brace counter is fooled
// by the "{" in the string and mis-captures otherwise.
const wiringSrc = `package main

func withString() {
	s := "an unbalanced { brace in a string"
	x := 1
	y := 2
	return s
}

func after() {
	println("after")
}
`

// Line map (1-based):
//  1 package main
//  2 (blank)
//  3 func withString() {
//  4   s := "an unbalanced { brace in a string"
//  5   x := 1
//  6   y := 2
//  7   return s
//  8 }
//  9 (blank)
// 10 func after() {
// 11   println("after")
// 12 }

// stubEnclosing returns an EnclosingBytes func that snaps any offset inside the
// withString body [byte range computed from the source] to that function's full
// range. It ignores all other offsets (returns ok=false) so the fallback path is
// also exercised.
func stubEnclosing(src string) func(uint64, int) (int, int, bool) {
	start := strings.Index(src, "func withString()")
	// End is the byte just past withString's closing brace on line 8.
	closeBrace := strings.Index(src, "\n}\n") // first standalone "}" — closes withString
	end := closeBrace + len("\n}") + 1        // include the '}' and its newline (exclusive)
	return func(blob uint64, off int) (int, int, bool) {
		if off >= start && off < end {
			return start, end, true
		}
		return 0, 0, false
	}
}

func TestAssemble_SymbolScopingSnapsToWholeFunction(t *testing.T) {
	ix, id := indexOne(t, "r", "main.go", "/abs/main.go", wiringSrc)
	res := []rank.RankedResult{{
		Blob:      id,
		Files:     []index.FileRef{ref("r", "main.go", "/abs/main.go")},
		Score:     1.0,
		LineSpans: []rank.LineSpan{span(5, 5)}, // hit on `x := 1` inside withString
	}}

	// With symbol scoping: block snaps to the WHOLE function, lines 3..8.
	win := Assemble(ix, res, Options{
		TokenBudget:    100000,
		ContextLines:   0,
		EnclosingBytes: stubEnclosing(wiringSrc),
	})
	if len(win.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d: %+v", len(win.Blocks), win.Blocks)
	}
	b := win.Blocks[0]
	if b.StartLine != 3 || b.EndLine != 8 {
		t.Fatalf("symbol scoping: want lines 3..8 (whole func), got %d..%d\n%s", b.StartLine, b.EndLine, b.Text)
	}
	if !strings.Contains(b.Text, "func withString()") || !strings.Contains(b.Text, "return s") {
		t.Errorf("block missing function head or tail:\n%s", b.Text)
	}
	if strings.Contains(b.Text, "func after()") {
		t.Errorf("block over-captured into after():\n%s", b.Text)
	}
}

func TestAssemble_NilEnclosingUnchanged(t *testing.T) {
	ix, id := indexOne(t, "r", "main.go", "/abs/main.go", wiringSrc)
	res := []rank.RankedResult{{
		Blob:      id,
		Files:     []index.FileRef{ref("r", "main.go", "/abs/main.go")},
		Score:     1.0,
		LineSpans: []rank.LineSpan{span(5, 5)},
	}}

	// Identical call with EnclosingBytes nil must produce exactly the legacy
	// heuristic result (proving the wiring is purely additive on the nil path).
	withNil := Assemble(ix, res, Options{TokenBudget: 100000, ContextLines: 0})
	if len(withNil.Blocks) != 1 {
		t.Fatalf("nil path: want 1 block, got %d", len(withNil.Blocks))
	}

	// Compare against expandSpan directly: the nil path must equal the raw
	// heuristic, and (crucially) it must NOT equal the symbol-scoped 3..8 here,
	// because the brace-in-string fools the heuristic — that divergence is the
	// whole point of the symbol layer.
	lines := splitLines([]byte(wiringSrc))
	hs, he := expandSpan(lines, span(5, 5), 3)
	b := withNil.Blocks[0]
	if b.StartLine != hs || b.EndLine != he {
		t.Errorf("nil path diverged from raw heuristic: got %d..%d want %d..%d", b.StartLine, b.EndLine, hs, he)
	}
}

// When EnclosingBytes is set but returns ok=false for the span, expansion must
// fall back to the heuristic exactly as if it were nil.
func TestAssemble_EnclosingMissFallsBack(t *testing.T) {
	ix, id := indexOne(t, "r", "main.go", "/abs/main.go", wiringSrc)
	res := []rank.RankedResult{{
		Blob:      id,
		Files:     []index.FileRef{ref("r", "main.go", "/abs/main.go")},
		Score:     1.0,
		LineSpans: []rank.LineSpan{span(5, 5)},
	}}

	miss := func(uint64, int) (int, int, bool) { return 0, 0, false }
	winMiss := Assemble(ix, res, Options{TokenBudget: 100000, ContextLines: 0, EnclosingBytes: miss})
	winNil := Assemble(ix, res, Options{TokenBudget: 100000, ContextLines: 0})

	if len(winMiss.Blocks) != 1 || len(winNil.Blocks) != 1 {
		t.Fatalf("want 1 block each, got miss=%d nil=%d", len(winMiss.Blocks), len(winNil.Blocks))
	}
	if winMiss.Blocks[0].StartLine != winNil.Blocks[0].StartLine ||
		winMiss.Blocks[0].EndLine != winNil.Blocks[0].EndLine {
		t.Errorf("ok=false path differs from nil path: miss=%d..%d nil=%d..%d",
			winMiss.Blocks[0].StartLine, winMiss.Blocks[0].EndLine,
			winNil.Blocks[0].StartLine, winNil.Blocks[0].EndLine)
	}
}
