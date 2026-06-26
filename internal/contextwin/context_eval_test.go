package contextwin

import (
	"strings"
	"testing"

	"moedex/internal/index"
	"moedex/internal/rank"
)

// This file is a small, reported context-window EVAL (not just a unit test): it
// builds a fixed gold corpus, assembles the window, and scores two properties an
// agent consumer actually depends on:
//
//   - recall-within-budget: the salient gold content of the highest-ranked
//     results is fully present in the emitted window, and when the budget is
//     tight the survivors are exactly the top-scored results (no relevant block
//     silently lost below an irrelevant one).
//   - dedup correctness: content shared across paths (SHA-deduped to one blob)
//     and overlapping/adjacent spans in one file never produce duplicated or
//     line-overlapping blocks.
//
// The eval prints its recall numbers via t.Logf so the score is visible in the
// test output, mirroring how only ranking is gated today (§B of the daily-use
// checklist: "if an agent is the primary consumer this is the relevant signal").

// goldItem is one piece of content the window is expected to recall, tagged with
// the result score that should drive its survival under budget pressure.
type goldItem struct {
	repo, rel, abs string
	content        string // full file content
	marker         string // unique substring that proves recall
	score          float64
	span           rank.LineSpan
}

// buildGoldCorpus indexes every gold item (SHA-deduping identical content) and
// returns the index plus the ranked results, blob IDs assigned in insertion
// order with dedup honored.
func buildGoldCorpus(t *testing.T, items []goldItem) (*index.Index, []rank.RankedResult) {
	t.Helper()
	ix := index.New()
	shaToBlob := map[string]uint64{}
	var next uint64
	results := make([]rank.RankedResult, 0, len(items))
	for _, it := range items {
		key := sha(it.content)
		blob, seen := shaToBlob[key]
		if !seen {
			blob = next
			shaToBlob[key] = blob
			next++
		}
		ix.AddFile(it.repo, it.rel, it.abs, key, []byte(it.content))
		results = append(results, rank.RankedResult{
			Blob:      blob,
			Files:     []index.FileRef{ref(it.repo, it.rel, it.abs)},
			Score:     it.score,
			LineSpans: []rank.LineSpan{it.span},
		})
	}
	return ix, results
}

// recall returns the fraction of the given markers that appear in the window's
// concatenated block text.
func recall(win ContextWindow, markers []string) float64 {
	if len(markers) == 0 {
		return 1.0
	}
	var all strings.Builder
	for _, b := range win.Blocks {
		all.WriteString(b.Text)
		all.WriteByte('\n')
	}
	text := all.String()
	hit := 0
	for _, m := range markers {
		if strings.Contains(text, m) {
			hit++
		}
	}
	return float64(hit) / float64(len(markers))
}

// assertNoLineOverlap fails if any two emitted blocks for the same file share a
// line — the dedup/merge invariant.
func assertNoLineOverlap(t *testing.T, win ContextWindow) {
	t.Helper()
	byFile := map[string][][2]int{}
	for _, b := range win.Blocks {
		byFile[b.AbsPath] = append(byFile[b.AbsPath], [2]int{b.StartLine, b.EndLine})
	}
	for abs, ranges := range byFile {
		for i := range ranges {
			for j := i + 1; j < len(ranges); j++ {
				a, b := ranges[i], ranges[j]
				if a[0] <= b[1] && b[0] <= a[1] {
					t.Errorf("dedup violation in %s: blocks %v and %v overlap", abs, a, b)
				}
			}
		}
	}
}

// goldFunc renders a tiny Go file whose body carries a unique marker on line 4.
func goldFunc(name, marker string) string {
	return "package p\n\nfunc " + name + "() {\n\t_ = \"" + marker + "\"\n\treturn\n}\n"
}

// ---- recall-within-budget ----

func TestEval_RecallWithinBudget(t *testing.T) {
	items := []goldItem{
		{repo: "r", rel: "a.go", abs: "/abs/a.go", content: goldFunc("alpha", "MARK_ALPHA"), marker: "MARK_ALPHA", score: 0.9, span: span(4, 4)},
		{repo: "r", rel: "b.go", abs: "/abs/b.go", content: goldFunc("bravo", "MARK_BRAVO"), marker: "MARK_BRAVO", score: 0.6, span: span(4, 4)},
		{repo: "r", rel: "c.go", abs: "/abs/c.go", content: goldFunc("charlie", "MARK_CHARLIE"), marker: "MARK_CHARLIE", score: 0.3, span: span(4, 4)},
	}
	ix, res := buildGoldCorpus(t, items)
	all := []string{"MARK_ALPHA", "MARK_BRAVO", "MARK_CHARLIE"}

	// Generous budget: every gold marker must be recalled (recall == 1.0).
	winFull := Assemble(ix, res, Options{TokenBudget: 100000, ContextLines: 1})
	if r := recall(winFull, all); r < 1.0 {
		t.Errorf("full-budget recall = %.2f, want 1.0; blocks=%d", r, len(winFull.Blocks))
	}
	t.Logf("recall@full-budget = %.2f (%d blocks, %d tok)", recall(winFull, all), len(winFull.Blocks), winFull.TokenEstimate)

	// Each gold function block is ~ goldFunc bytes -> a few dozen tokens. A budget
	// that fits exactly the top-2 scored blocks must recall ALPHA+BRAVO (the two
	// highest scores) and drop CHARLIE — recall-within-budget = top-K by score.
	oneBlock := estimateTokens(sliceLines([]byte(goldFunc("alpha", "MARK_ALPHA")), 3, 6))
	tight := oneBlock*2 + 1
	winTight := Assemble(ix, res, Options{TokenBudget: tight, ContextLines: 1})
	if !winTight.Truncated {
		t.Errorf("tight budget should truncate; got Truncated=false with %d blocks", len(winTight.Blocks))
	}
	if r := recall(winTight, []string{"MARK_ALPHA", "MARK_BRAVO"}); r < 1.0 {
		t.Errorf("tight-budget recall of top-2 = %.2f, want 1.0", r)
	}
	if recall(winTight, []string{"MARK_CHARLIE"}) != 0.0 {
		t.Errorf("lowest-scored CHARLIE should be dropped under tight budget")
	}
	if winTight.TokenEstimate > tight {
		t.Errorf("TokenEstimate %d exceeds budget %d", winTight.TokenEstimate, tight)
	}
	t.Logf("recall@tight-budget(top2) = 1.00, charlie dropped, %d tok <= %d", winTight.TokenEstimate, tight)
}

// ---- dedup correctness ----

func TestEval_DedupSharedContentOneBlock(t *testing.T) {
	// Identical content under two repos/paths is SHA-deduped to ONE blob. The
	// ranker emits one RankedResult per blob carrying every path in Files (see
	// rank.RankedResult: "Files are every path the blob's content appears at").
	// contextwin must surface that blob once (reporting Files[0]), not once per
	// path — otherwise byte-identical content burns the budget twice.
	shared := goldFunc("shared", "MARK_SHARED")
	ix := index.New()
	ix.AddFile("r1", "x.go", "/abs/x.go", sha(shared), []byte(shared))
	ix.AddFile("r2", "y.go", "/abs/y.go", sha(shared), []byte(shared)) // deduped to blob 0
	if got := ix.NumBlobs(); got != 1 {
		t.Fatalf("identical content should dedup to 1 blob, got %d", got)
	}
	res := []rank.RankedResult{{
		Blob: 0,
		Files: []index.FileRef{
			ref("r1", "x.go", "/abs/x.go"),
			ref("r2", "y.go", "/abs/y.go"),
		},
		Score:     0.8,
		LineSpans: []rank.LineSpan{span(4, 4)},
	}}
	win := Assemble(ix, res, Options{TokenBudget: 100000, ContextLines: 1})

	if len(win.Blocks) != 1 {
		t.Fatalf("deduped blob should yield 1 block, got %d: %+v", len(win.Blocks), win.Blocks)
	}
	count := 0
	for _, b := range win.Blocks {
		count += strings.Count(b.Text, "MARK_SHARED")
	}
	if count != 1 {
		t.Errorf("shared content should appear once after dedup, appeared %d times", count)
	}
	if win.Blocks[0].Repo != "r1" || win.Blocks[0].RelPath != "x.go" {
		t.Errorf("deduped block should report Files[0] (r1/x.go), got %s/%s", win.Blocks[0].Repo, win.Blocks[0].RelPath)
	}
	assertNoLineOverlap(t, win)
	t.Logf("dedup: blob shared across 2 paths emitted once (as %s/%s)", win.Blocks[0].Repo, win.Blocks[0].RelPath)
}

func TestEval_DedupOverlappingSpansMerge(t *testing.T) {
	// Three overlapping/adjacent salient spans in one file must merge to a single
	// block with no line counted twice.
	src := "L1\nL2\nL3\nL4\nL5\nL6\nL7\nL8\nL9\nL10\n"
	ix := index.New()
	ix.AddFile("r", "f.txt", "/abs/f.txt", sha(src), []byte(src))
	res := []rank.RankedResult{
		{Blob: 0, Files: []index.FileRef{ref("r", "f.txt", "/abs/f.txt")}, Score: 0.5, LineSpans: []rank.LineSpan{span(2, 2)}},
		{Blob: 0, Files: []index.FileRef{ref("r", "f.txt", "/abs/f.txt")}, Score: 0.5, LineSpans: []rank.LineSpan{span(3, 3)}},
		{Blob: 0, Files: []index.FileRef{ref("r", "f.txt", "/abs/f.txt")}, Score: 0.5, LineSpans: []rank.LineSpan{span(4, 4)}},
	}
	win := Assemble(ix, res, Options{TokenBudget: 100000, ContextLines: 1})
	if len(win.Blocks) != 1 {
		t.Fatalf("overlapping spans must merge to 1 block, got %d: %+v", len(win.Blocks), win.Blocks)
	}
	assertNoLineOverlap(t, win)
	// No line content should repeat inside the merged block.
	for _, line := range []string{"L2", "L3", "L4", "L5"} {
		if c := strings.Count(win.Blocks[0].Text, line+"\n"); c != 1 {
			t.Errorf("line %s appears %d times in merged block, want 1:\n%s", line, c, win.Blocks[0].Text)
		}
	}
	t.Logf("dedup: 3 overlapping spans merged into 1 block lines %d-%d", win.Blocks[0].StartLine, win.Blocks[0].EndLine)
}
