package contextwin

import (
	"strings"
	"testing"
	"unicode/utf8"

	"moedex/internal/index"
	"moedex/internal/rank"
)

// helper: build an index with one file and return (index, blobID).
func indexOne(t *testing.T, repo, rel, abs, content string) (*index.Index, uint64) {
	t.Helper()
	ix := index.New()
	ix.AddFile(repo, rel, abs, sha(content), []byte(content))
	b := ix.Blob(0)
	if b == nil {
		t.Fatalf("blob 0 missing after AddFile")
	}
	return ix, 0
}

// sha is a trivial unique-ish key for tests (content-addressed dedup only needs
// distinct keys for distinct content here).
func sha(s string) string { return "sha-" + s }

func span(start, end int) rank.LineSpan { return rank.LineSpan{StartLine: start, EndLine: end} }

func ref(repo, rel, abs string) index.FileRef {
	return index.FileRef{Repo: repo, RelPath: rel, AbsPath: abs}
}

// ---- Provenance propagation (ADR 0015) ----

// A block must inherit the contributing result's Blob and per-arm scores
// (Lexical/Dense), not just the fused Score.
func TestAssembleCarriesProvenance(t *testing.T) {
	src := "package main\n\nfunc Connect() {\n\tdatabase.Open()\n}\n"
	ix, id := indexOne(t, "r", "db.go", "/abs/db.go", src)
	res := []rank.RankedResult{{
		Blob:      id,
		Files:     []index.FileRef{ref("r", "db.go", "/abs/db.go")},
		Score:     0.031,
		Lexical:   7.41,
		Dense:     0.83,
		LineSpans: []rank.LineSpan{span(4, 4)},
	}}
	win := Assemble(ix, res, Options{TokenBudget: 100000})
	if len(win.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(win.Blocks))
	}
	b := win.Blocks[0]
	if b.Blob != id {
		t.Errorf("Blob = %d, want %d", b.Blob, id)
	}
	if b.Score != 0.031 || b.Lexical != 7.41 || b.Dense != 0.83 {
		t.Errorf("provenance = (score %v, lexical %v, dense %v), want (0.031, 7.41, 0.83)", b.Score, b.Lexical, b.Dense)
	}
}

// When two spans of the same file merge, the merged block keeps the per-arm
// scores of the higher-scoring contributor (consistent with the kept Score).
func TestAssembleMergeKeepsWinningProvenance(t *testing.T) {
	// Two results on the same blob with adjacent spans so they merge; the second
	// has the higher fused Score and must win the provenance.
	src := "package main\n\nfunc A() {\n\tx := 1\n\ty := 2\n\tz := 3\n}\n"
	ix, id := indexOne(t, "r", "a.go", "/abs/a.go", src)
	files := []index.FileRef{ref("r", "a.go", "/abs/a.go")}
	res := []rank.RankedResult{
		{Blob: id, Files: files, Score: 0.10, Lexical: 1.0, Dense: 0.10, LineSpans: []rank.LineSpan{span(4, 4)}},
		{Blob: id, Files: files, Score: 0.90, Lexical: 9.0, Dense: 0.90, LineSpans: []rank.LineSpan{span(5, 5)}},
	}
	win := Assemble(ix, res, Options{TokenBudget: 100000, ContextLines: 0})
	if len(win.Blocks) != 1 {
		t.Fatalf("want 1 merged block, got %d", len(win.Blocks))
	}
	b := win.Blocks[0]
	if b.Score != 0.90 || b.Lexical != 9.0 || b.Dense != 0.90 {
		t.Errorf("merged provenance = (score %v, lexical %v, dense %v), want the higher-scoring contributor (0.90, 9.0, 0.90)", b.Score, b.Lexical, b.Dense)
	}
}

// ---- Block expansion: brace-delimited function ----

func TestExpandBraceFunction(t *testing.T) {
	src := `package main

func foo() {
	x := 1
	y := 2
	return x + y
}

func bar() {}
`
	// Lines:
	// 1 package main
	// 2 (blank)
	// 3 func foo() {
	// 4   x := 1
	// 5   y := 2
	// 6   return x + y
	// 7 }
	// 8 (blank)
	// 9 func bar() {}
	ix, id := indexOne(t, "r", "main.go", "/abs/main.go", src)
	res := []rank.RankedResult{{
		Blob:      id,
		Files:     []index.FileRef{ref("r", "main.go", "/abs/main.go")},
		Score:     1.0,
		LineSpans: []rank.LineSpan{span(4, 4)}, // hit on `x := 1`, deep inside foo
	}}
	// ContextLines 0 so padding does not mask the brace logic; but defaults kick
	// in at <=0, so use 1 explicitly and rely on brace balance to capture body.
	win := Assemble(ix, res, Options{TokenBudget: 100000, ContextLines: 0})
	if len(win.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d: %+v", len(win.Blocks), win.Blocks)
	}
	b := win.Blocks[0]
	// Padding default (3) -> start 1, end 7; net braces over [1,7]: '{' on 3, '}'
	// on 7 => balanced, indent fallback. To make the brace path deterministic we
	// instead assert the function body is fully captured: must include the
	// closing brace line 7 and the opening line 3.
	if !strings.Contains(b.Text, "func foo()") {
		t.Errorf("block should include function header:\n%s", b.Text)
	}
	if !strings.Contains(b.Text, "return x + y") {
		t.Errorf("block should include function body:\n%s", b.Text)
	}
	if b.StartLine > 3 {
		t.Errorf("StartLine %d should reach func header (line 3)", b.StartLine)
	}
	if b.EndLine < 7 {
		t.Errorf("EndLine %d should reach closing brace (line 7)", b.EndLine)
	}
	// Should NOT bleed into bar().
	if strings.Contains(b.Text, "func bar()") {
		t.Errorf("block should not include the next function:\n%s", b.Text)
	}
}

// A span on the opening brace line with no context padding exercises the net>0
// brace-down walk specifically.
func TestExpandBraceDownFromHeader(t *testing.T) {
	src := "func f() {\n\ta()\n\tb()\n}\nafter()\n"
	// 1 func f() {
	// 2   a()
	// 3   b()
	// 4 }
	// 5 after()
	ix, id := indexOne(t, "r", "f.go", "/abs/f.go", src)
	res := []rank.RankedResult{{
		Blob:      id,
		Files:     []index.FileRef{ref("r", "f.go", "/abs/f.go")},
		Score:     1.0,
		LineSpans: []rank.LineSpan{span(1, 1)},
	}}
	// Anchor is the hit line 1 (net brace +1) -> walk down to closing brace 4.
	win := Assemble(ix, res, Options{TokenBudget: 100000, ContextLines: 1})
	if len(win.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(win.Blocks))
	}
	b := win.Blocks[0]
	if b.StartLine != 1 || b.EndLine != 4 {
		t.Errorf("want lines 1-4 (balanced function), got %d-%d:\n%s", b.StartLine, b.EndLine, b.Text)
	}
	if strings.Contains(b.Text, "after()") {
		t.Errorf("should stop at closing brace, not include after():\n%s", b.Text)
	}
}

// ---- Block expansion: indent-delimited (Python-like) ----

func TestExpandIndent(t *testing.T) {
	src := "def f():\n    a = 1\n    b = 2\n    return a + b\n\ndef g():\n    pass\n"
	// 1 def f():
	// 2     a = 1
	// 3     b = 2
	// 4     return a + b
	// 5 (blank)
	// 6 def g():
	// 7     pass
	ix, id := indexOne(t, "r", "x.py", "/abs/x.py", src)
	res := []rank.RankedResult{{
		Blob:      id,
		Files:     []index.FileRef{ref("r", "x.py", "/abs/x.py")},
		Score:     1.0,
		LineSpans: []rank.LineSpan{span(3, 3)}, // `b = 2`
	}}
	win := Assemble(ix, res, Options{TokenBudget: 100000, ContextLines: 1})
	if len(win.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(win.Blocks))
	}
	b := win.Blocks[0]
	// indent fallback: min indent of region (line 3) is 4; extend across the
	// >=4-indent run (lines 2-4), then pull header `def f():` (line 1, indent 0).
	if b.StartLine != 1 {
		t.Errorf("want header line 1, got StartLine %d:\n%s", b.StartLine, b.Text)
	}
	if b.EndLine != 4 {
		t.Errorf("want end of indented run line 4, got EndLine %d:\n%s", b.EndLine, b.Text)
	}
	if !strings.Contains(b.Text, "def f():") || !strings.Contains(b.Text, "return a + b") {
		t.Errorf("block should span the whole indented body:\n%s", b.Text)
	}
	if strings.Contains(b.Text, "def g()") {
		t.Errorf("block should not bleed into next def:\n%s", b.Text)
	}
}

// ---- Clamping at file start/end ----

func TestExpandClampsToFileBounds(t *testing.T) {
	src := "a\nb\nc\n" // 3 lines
	ix, id := indexOne(t, "r", "t.txt", "/abs/t.txt", src)
	res := []rank.RankedResult{{
		Blob:      id,
		Files:     []index.FileRef{ref("r", "t.txt", "/abs/t.txt")},
		Score:     1.0,
		LineSpans: []rank.LineSpan{span(1, 3)},
	}}
	// Large padding would go negative / past EOF; must clamp to [1,3].
	win := Assemble(ix, res, Options{TokenBudget: 100000, ContextLines: 50})
	if len(win.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(win.Blocks))
	}
	b := win.Blocks[0]
	if b.StartLine != 1 || b.EndLine != 3 {
		t.Errorf("want clamp to 1-3, got %d-%d", b.StartLine, b.EndLine)
	}
	if b.Text != "a\nb\nc\n" {
		t.Errorf("unexpected text %q", b.Text)
	}
}

// A span starting/ending past EOF must clamp, not panic.
func TestSpanBeyondEOFClamped(t *testing.T) {
	src := "one\ntwo\n" // 2 lines
	ix, id := indexOne(t, "r", "t.txt", "/abs/t.txt", src)
	res := []rank.RankedResult{{
		Blob:      id,
		Files:     []index.FileRef{ref("r", "t.txt", "/abs/t.txt")},
		Score:     1.0,
		LineSpans: []rank.LineSpan{span(100, 200)},
	}}
	win := Assemble(ix, res, Options{TokenBudget: 100000, ContextLines: 1})
	if len(win.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(win.Blocks))
	}
	b := win.Blocks[0]
	if b.EndLine > 2 || b.StartLine < 1 {
		t.Errorf("span past EOF not clamped: %d-%d", b.StartLine, b.EndLine)
	}
}

// ---- Overlap / adjacency merge within a file ----

func TestMergeOverlappingSpansSameFile(t *testing.T) {
	// 10 plain lines, brace-free, indent-free -> indent fallback is a no-op
	// (minIndent 0, no extension), so block lines equal padded spans.
	src := "L1\nL2\nL3\nL4\nL5\nL6\nL7\nL8\nL9\nL10\n"
	ix, id := indexOne(t, "r", "f.txt", "/abs/f.txt", src)
	res := []rank.RankedResult{{
		Blob:  id,
		Files: []index.FileRef{ref("r", "f.txt", "/abs/f.txt")},
		Score: 0.5,
		LineSpans: []rank.LineSpan{
			span(3, 3), // pad1 -> 2..4
			span(5, 5), // pad1 -> 4..6  (touches/overlaps 2..4)
		},
	}}
	win := Assemble(ix, res, Options{TokenBudget: 100000, ContextLines: 1})
	if len(win.Blocks) != 1 {
		t.Fatalf("overlapping spans should merge to 1 block, got %d: %+v", len(win.Blocks), win.Blocks)
	}
	b := win.Blocks[0]
	if b.StartLine != 2 || b.EndLine != 6 {
		t.Errorf("merged range want 2-6, got %d-%d", b.StartLine, b.EndLine)
	}
}

func TestMergeKeepsMaxScore(t *testing.T) {
	src := "L1\nL2\nL3\nL4\nL5\nL6\nL7\n"
	ix := index.New()
	ix.AddFile("r", "f.txt", "/abs/f.txt", sha(src), []byte(src))
	res := []rank.RankedResult{
		{Blob: 0, Files: []index.FileRef{ref("r", "f.txt", "/abs/f.txt")}, Score: 0.2, LineSpans: []rank.LineSpan{span(2, 2)}},
		{Blob: 0, Files: []index.FileRef{ref("r", "f.txt", "/abs/f.txt")}, Score: 0.9, LineSpans: []rank.LineSpan{span(3, 3)}},
	}
	win := Assemble(ix, res, Options{TokenBudget: 100000, ContextLines: 1})
	if len(win.Blocks) != 1 {
		t.Fatalf("adjacent spans should merge to 1 block, got %d", len(win.Blocks))
	}
	if win.Blocks[0].Score != 0.9 {
		t.Errorf("merged block should keep max score 0.9, got %v", win.Blocks[0].Score)
	}
}

func TestDifferentFilesStaySeparate(t *testing.T) {
	srcA := "A1\nA2\nA3\n"
	srcB := "B1\nB2\nB3\n"
	ix := index.New()
	ix.AddFile("r", "a.txt", "/abs/a.txt", sha(srcA), []byte(srcA))
	ix.AddFile("r", "b.txt", "/abs/b.txt", sha(srcB), []byte(srcB))
	res := []rank.RankedResult{
		{Blob: 0, Files: []index.FileRef{ref("r", "a.txt", "/abs/a.txt")}, Score: 0.5, LineSpans: []rank.LineSpan{span(2, 2)}},
		{Blob: 1, Files: []index.FileRef{ref("r", "b.txt", "/abs/b.txt")}, Score: 0.5, LineSpans: []rank.LineSpan{span(2, 2)}},
	}
	win := Assemble(ix, res, Options{TokenBudget: 100000, ContextLines: 1})
	if len(win.Blocks) != 2 {
		t.Fatalf("blocks from different files must stay separate, got %d", len(win.Blocks))
	}
}

func TestMergeAcrossOneBlankLine(t *testing.T) {
	content := []byte("first\n\nthird\n")
	got := mergeFile([]candidate{
		{startLine: 1, endLine: 1, content: content, score: 1, salientLine: 1},
		{startLine: 3, endLine: 3, content: content, score: .9, salientLine: 3},
	})
	if len(got) != 1 || got[0].startLine != 1 || got[0].endLine != 3 {
		t.Fatalf("one-blank-line ranges did not coalesce: %+v", got)
	}
}

func TestImportPenaltyLetsSimilarImplementationWin(t *testing.T) {
	header := "using System;\nusing System.Linq;\nusing Product.Domain;\nusing Product.Cart;\n"
	implementation := "public Order Checkout(Cart cart) {\n    return domain.Register(cart.Domain);\n}\n"
	ix := index.New()
	ix.AddFile("r", "Controller.cs", "/abs/Controller.cs", sha(header), []byte(header))
	ix.AddFile("r", "Checkout.cs", "/abs/Checkout.cs", sha(implementation), []byte(implementation))
	win := Assemble(ix, []rank.RankedResult{
		{Blob: 0, Files: []index.FileRef{ref("r", "Controller.cs", "/abs/Controller.cs")}, Score: 1, LineSpans: []rank.LineSpan{span(1, 4)}},
		{Blob: 1, Files: []index.FileRef{ref("r", "Checkout.cs", "/abs/Checkout.cs")}, Score: .5, LineSpans: []rank.LineSpan{span(1, 3)}},
	}, Options{TokenBudget: 100, ContextLines: 1})
	if len(win.Blocks) < 1 || win.Blocks[0].RelPath != "Checkout.cs" {
		t.Fatalf("import-dominated header outranked implementation: %+v", win.Blocks)
	}
}

func TestImportPenaltyLetsStrongHeaderBeatWeakImplementation(t *testing.T) {
	header := "using System;\nusing System.Linq;\nusing Product.Domain;\nusing Product.Cart;\n"
	implementation := "public Order Noise() {\n    return default;\n}\n"
	ix := index.New()
	ix.AddFile("r", "Controller.cs", "/abs/Controller.cs", sha(header), []byte(header))
	ix.AddFile("r", "Noise.cs", "/abs/Noise.cs", sha(implementation), []byte(implementation))
	win := Assemble(ix, []rank.RankedResult{
		{Blob: 0, Files: []index.FileRef{ref("r", "Controller.cs", "/abs/Controller.cs")}, Score: 1, LineSpans: []rank.LineSpan{span(1, 4)}},
		{Blob: 1, Files: []index.FileRef{ref("r", "Noise.cs", "/abs/Noise.cs")}, Score: .3, LineSpans: []rank.LineSpan{span(1, 3)}},
	}, Options{TokenBudget: 100, ContextLines: 1})
	if len(win.Blocks) < 1 || win.Blocks[0].RelPath != "Controller.cs" {
		t.Fatalf("strong import result should beat weak implementation noise: %+v", win.Blocks)
	}
	if win.Blocks[0].Score != 1 {
		t.Fatalf("public Score changed by internal import penalty: got %v want 1", win.Blocks[0].Score)
	}
}

func TestTestPathPenaltyIsSoftAndComposable(t *testing.T) {
	const implementation = "public Order Checkout() { return order; }\n"
	const imports = "using System;\nusing Product.Domain;\nusing Product.Cart;\n"
	tests := []struct {
		name       string
		paths      []string
		scores     []float64
		contents   []string
		wantFirst  string
		wantScores []float64
	}{
		{
			name:       "production narrowly beats test",
			paths:      []string{"CheckoutTests.cs", "Checkout.cs"},
			scores:     []float64{.9, .5},
			contents:   []string{implementation, implementation + "// production\n"},
			wantFirst:  "Checkout.cs",
			wantScores: []float64{.5, .9},
		},
		{
			name:       "strong test beats weak production",
			paths:      []string{"CheckoutTests.cs", "Checkout.cs"},
			scores:     []float64{1, .4},
			contents:   []string{implementation, implementation + "// production\n"},
			wantFirst:  "CheckoutTests.cs",
			wantScores: []float64{1, .4},
		},
		{
			name:       "import and test penalties compose",
			paths:      []string{"CheckoutTests.cs", "Checkout.cs"},
			scores:     []float64{1, .25},
			contents:   []string{imports, implementation},
			wantFirst:  "Checkout.cs",
			wantScores: []float64{.25, 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ix := index.New()
			results := make([]rank.RankedResult, 0, len(tt.paths))
			for i := range tt.paths {
				abs := "/abs/" + tt.paths[i]
				ix.AddFile("r", tt.paths[i], abs, sha(tt.contents[i]), []byte(tt.contents[i]))
				results = append(results, rank.RankedResult{
					Blob: uint64(i), Files: []index.FileRef{ref("r", tt.paths[i], abs)}, Score: tt.scores[i],
					LineSpans: []rank.LineSpan{span(1, strings.Count(tt.contents[i], "\n"))},
				})
			}
			win := Assemble(ix, results, Options{TokenBudget: 1000, ContextLines: 1})
			if len(win.Blocks) != 2 || win.Blocks[0].RelPath != tt.wantFirst {
				t.Fatalf("blocks = %+v, want %s first", win.Blocks, tt.wantFirst)
			}
			for i, want := range tt.wantScores {
				if win.Blocks[i].Score != want {
					t.Fatalf("block %d public Score = %v, want raw score %v", i, win.Blocks[i].Score, want)
				}
			}
		})
	}
}

func TestSelectionScoreTieUsesRawScoreThenStableKeys(t *testing.T) {
	imports := "using System;\nusing Product.Domain;\n"
	implementation := "public void Checkout() {}\n"
	ix := index.New()
	ix.AddFile("r", "Imports.cs", "/abs/z.cs", sha(imports), []byte(imports))
	ix.AddFile("r", "Checkout.cs", "/abs/a.cs", sha(implementation), []byte(implementation))
	results := []rank.RankedResult{
		{Blob: 1, Files: []index.FileRef{ref("r", "Checkout.cs", "/abs/a.cs")}, Score: .4, LineSpans: []rank.LineSpan{span(1, 1)}},
		{Blob: 0, Files: []index.FileRef{ref("r", "Imports.cs", "/abs/z.cs")}, Score: 1, LineSpans: []rank.LineSpan{span(1, 2)}},
	}
	for i := 0; i < 10; i++ {
		win := Assemble(ix, results, Options{TokenBudget: 1000, ContextLines: 1})
		if len(win.Blocks) != 2 || win.Blocks[0].RelPath != "Imports.cs" {
			t.Fatalf("run %d selection-score tie did not use raw score deterministically: %+v", i, win.Blocks)
		}
	}
}

func TestTestDominatedPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"Orders/CheckoutTests.cs", true},
		{"orders/checkouttests.CS", true},
		{"ui/cart.spec.ts", true},
		{"pkg/cart_test.go", true},
		{`ui\\cypress\\support\\Cart.ts`, true},
		{"cypress/Cart.ts", true},
		{"Orders/CheckoutTest.cs", false},
		{"ui/cart.spec.tsx", false},
		{"pkg/cart_tests.go", false},
		{"ui/mycypress/support/Cart.ts", false},
		{"ui/cypressical/Cart.ts", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isTestDominatedPath(tt.path); got != tt.want {
				t.Fatalf("isTestDominatedPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestTestPenaltyFollowsDisplayedFirstFileRef(t *testing.T) {
	shared := "public void Checkout() {}\n"
	competitor := "public void Other() {}\n"
	ix := index.New()
	ix.AddFile("r", "CheckoutTests.cs", "/abs/CheckoutTests.cs", sha(shared), []byte(shared))
	ix.AddFile("r", "Checkout.cs", "/abs/Checkout.cs", sha(shared), []byte(shared))
	ix.AddFile("r", "Other.cs", "/abs/Other.cs", sha(competitor), []byte(competitor))

	assemble := func(files []index.FileRef) ContextWindow {
		return Assemble(ix, []rank.RankedResult{
			{Blob: 0, Files: files, Score: .9, LineSpans: []rank.LineSpan{span(1, 1)}},
			{Blob: 1, Files: []index.FileRef{ref("r", "Other.cs", "/abs/Other.cs")}, Score: .5, LineSpans: []rank.LineSpan{span(1, 1)}},
		}, Options{TokenBudget: 1000, ContextLines: 1})
	}

	testFirst := assemble([]index.FileRef{
		ref("r", "CheckoutTests.cs", "/abs/CheckoutTests.cs"),
		ref("r", "Checkout.cs", "/abs/Checkout.cs"),
	})
	if len(testFirst.Blocks) != 2 || testFirst.Blocks[0].RelPath != "Other.cs" {
		t.Fatalf("displayed test path should receive test penalty: %+v", testFirst.Blocks)
	}

	productionFirst := assemble([]index.FileRef{
		ref("r", "Checkout.cs", "/abs/Checkout.cs"),
		ref("r", "CheckoutTests.cs", "/abs/CheckoutTests.cs"),
	})
	if len(productionFirst.Blocks) != 2 || productionFirst.Blocks[0].RelPath != "Checkout.cs" {
		t.Fatalf("non-displayed test alias should not penalize production provenance: %+v", productionFirst.Blocks)
	}
}

func TestBlobViewImportClassificationAtDeepLine(t *testing.T) {
	var src strings.Builder
	for i := 1; i <= 5000; i++ {
		switch {
		case i == 3999:
			src.WriteString("import (\n")
		case i >= 4000 && i <= 4004:
			src.WriteString("\t\"example.com/pkg\"\n")
		case i == 4005:
			src.WriteString(")\n")
		default:
			src.WriteString("implementation()\n")
		}
	}
	view := newBlobView([]byte(src.String()))
	if !view.importDominated(3999, 4005) {
		t.Fatal("deep Go import block was not classified from precomputed membership")
	}
	if view.importDominated(4006, 4010) {
		t.Fatal("implementation below deep import block was misclassified")
	}
}

// ---- Token budget / truncation ----

func TestTokenBudgetTruncates(t *testing.T) {
	// Three files, each block ~ a known number of chars. With a tight budget only
	// the top-scoring blocks survive and Truncated is set.
	body := strings.Repeat("xxxxxxxxxx\n", 5) // 5 lines * 11 chars = 55 chars -> 14 tokens
	ix := index.New()
	ix.AddFile("r", "a.txt", "/abs/a.txt", sha("a"+body), []byte(body))
	ix.AddFile("r", "b.txt", "/abs/b.txt", sha("b"+body), []byte(body))
	ix.AddFile("r", "c.txt", "/abs/c.txt", sha("c"+body), []byte(body))
	res := []rank.RankedResult{
		{Blob: 0, Files: []index.FileRef{ref("r", "a.txt", "/abs/a.txt")}, Score: 0.9, LineSpans: []rank.LineSpan{span(1, 5)}},
		{Blob: 1, Files: []index.FileRef{ref("r", "b.txt", "/abs/b.txt")}, Score: 0.5, LineSpans: []rank.LineSpan{span(1, 5)}},
		{Blob: 2, Files: []index.FileRef{ref("r", "c.txt", "/abs/c.txt")}, Score: 0.1, LineSpans: []rank.LineSpan{span(1, 5)}},
	}
	// Each block is 55 chars -> 14 tokens. Budget 20 fits exactly one block.
	win := Assemble(ix, res, Options{TokenBudget: 20, ContextLines: 0})
	if len(win.Blocks) != 1 {
		t.Fatalf("budget should admit exactly 1 block, got %d (estimate %d)", len(win.Blocks), win.TokenEstimate)
	}
	if win.Blocks[0].AbsPath != "/abs/a.txt" {
		t.Errorf("highest-scoring block (a) should survive, got %s", win.Blocks[0].AbsPath)
	}
	if !win.Truncated {
		t.Errorf("Truncated should be true when blocks were dropped")
	}
	if win.TokenEstimate > 20 {
		t.Errorf("TokenEstimate %d exceeds budget 20", win.TokenEstimate)
	}
}

func TestFirstBlockClippedToBudget(t *testing.T) {
	body := strings.Repeat("y", 1000) + "\n" // ~250 tokens, far over budget
	ix, id := indexOne(t, "r", "big.txt", "/abs/big.txt", body)
	res := []rank.RankedResult{{
		Blob:      id,
		Files:     []index.FileRef{ref("r", "big.txt", "/abs/big.txt")},
		Score:     1.0,
		LineSpans: []rank.LineSpan{span(1, 1)},
	}}
	win := Assemble(ix, res, Options{TokenBudget: 5, ContextLines: 0})
	if len(win.Blocks) != 1 {
		t.Fatalf("first block should be clipped rather than omitted, got %d blocks", len(win.Blocks))
	}
	if win.Truncated {
		t.Errorf("only block emitted -> nothing dropped -> Truncated should be false")
	}
	if !win.Clipped || !win.Blocks[0].Clipped {
		t.Fatalf("oversized first block should report clipping: %+v", win)
	}
	if win.TokenEstimate > 5 {
		t.Fatalf("TokenEstimate = %d, exceeds budget 5", win.TokenEstimate)
	}
	if win.Blocks[0].Text == "" {
		t.Fatal("clipped first block must remain non-empty")
	}
}

func TestLargeEnclosingSymbolClipsAroundSalient(t *testing.T) {
	var src strings.Builder
	for i := 1; i <= 400; i++ {
		if i == 200 {
			src.WriteString("salient_call(\"🙂\")\n")
		} else {
			src.WriteString("short line\n")
		}
	}
	ix, id := indexOne(t, "r", "large.go", "/abs/large.go", src.String())
	res := []rank.RankedResult{{
		Blob: id, Files: []index.FileRef{ref("r", "large.go", "/abs/large.go")}, Score: 1,
		LineSpans: []rank.LineSpan{span(200, 200)},
	}}
	win := Assemble(ix, res, Options{
		TokenBudget: 20, ContextLines: 1,
		EnclosingBytes: func(uint64, int) (int, int, bool) { return 0, len(src.String()), true },
	})
	if len(win.Blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(win.Blocks))
	}
	if !win.Blocks[0].Clipped {
		t.Fatalf("400-line symbol should remain the selected scope and clip around its salient line: %+v", win.Blocks[0])
	}
	if !strings.Contains(win.Blocks[0].Text, "salient_call(\"🙂\")") {
		t.Fatalf("fallback lost salient line: %q", win.Blocks[0].Text)
	}
	if !utf8.ValidString(win.Blocks[0].Text) {
		t.Fatalf("large-scope clip split UTF-8: %q", win.Blocks[0].Text)
	}
	if win.TokenEstimate > 20 {
		t.Fatalf("TokenEstimate = %d, exceeds budget 20", win.TokenEstimate)
	}
	if win.TokenEstimate != estimateTokens(win.Blocks[0].Text) {
		t.Fatalf("TokenEstimate = %d, exact clipped estimate = %d", win.TokenEstimate, estimateTokens(win.Blocks[0].Text))
	}
}

func TestClippingCentersOnHighestRankedMergedSalientLine(t *testing.T) {
	src := "low\n" + strings.Repeat("middle line\n", 20) + "HIGH-SALIENT\n"
	ix, id := indexOne(t, "r", "merged.txt", "/abs/merged.txt", src)
	files := []index.FileRef{ref("r", "merged.txt", "/abs/merged.txt")}
	res := []rank.RankedResult{
		{Blob: id, Files: files, Score: 0.1, LineSpans: []rank.LineSpan{span(1, 1)}},
		{Blob: id, Files: files, Score: 1.0, LineSpans: []rank.LineSpan{span(22, 22)}},
	}
	win := Assemble(ix, res, Options{
		TokenBudget:    4,
		ContextLines:   50,
		EnclosingBytes: func(uint64, int) (int, int, bool) { return 0, len(src), true },
	})
	if len(win.Blocks) != 1 || !win.Blocks[0].Clipped {
		t.Fatalf("expected one clipped merged block: %+v", win)
	}
	if !strings.Contains(win.Blocks[0].Text, "HIGH-SALIENT") {
		t.Fatalf("clipping was not anchored on highest-ranked salient line: %q", win.Blocks[0].Text)
	}
}

func TestOneLineUTF8OverflowClipsAtRuneBoundary(t *testing.T) {
	body := strings.Repeat("🙂", 20) + "\n"
	ix, id := indexOne(t, "r", "utf8.txt", "/abs/utf8.txt", body)
	win := Assemble(ix, []rank.RankedResult{{
		Blob: id, Files: []index.FileRef{ref("r", "utf8.txt", "/abs/utf8.txt")}, Score: 1,
		LineSpans: []rank.LineSpan{span(1, 1)},
	}}, Options{TokenBudget: 3, ContextLines: 1})
	if len(win.Blocks) != 1 || win.Blocks[0].Text == "" {
		t.Fatalf("expected one non-empty block: %+v", win)
	}
	if strings.ToValidUTF8(win.Blocks[0].Text, "") != win.Blocks[0].Text {
		t.Fatalf("clipped text is not valid UTF-8: %q", win.Blocks[0].Text)
	}
	if win.TokenEstimate > 3 {
		t.Fatalf("TokenEstimate = %d, exceeds budget 3", win.TokenEstimate)
	}
}

func TestClippingAndTruncationAreIndependent(t *testing.T) {
	large := strings.Repeat("first block ", 100) + "\n"
	small := "second block\n"
	ix := index.New()
	ix.AddFile("r", "large.txt", "/abs/large.txt", sha(large), []byte(large))
	ix.AddFile("r", "small.txt", "/abs/small.txt", sha(small), []byte(small))
	win := Assemble(ix, []rank.RankedResult{
		{Blob: 0, Files: []index.FileRef{ref("r", "large.txt", "/abs/large.txt")}, Score: 1, LineSpans: []rank.LineSpan{span(1, 1)}},
		{Blob: 1, Files: []index.FileRef{ref("r", "small.txt", "/abs/small.txt")}, Score: .5, LineSpans: []rank.LineSpan{span(1, 1)}},
	}, Options{TokenBudget: 5, ContextLines: 1})
	if !win.Clipped || !win.Truncated {
		t.Fatalf("window should report both clipping and omitted candidates: %+v", win)
	}
	if win.TokenEstimate > 5 {
		t.Fatalf("TokenEstimate = %d, exceeds budget 5", win.TokenEstimate)
	}
}

func TestTokenEstimateMatchesFormula(t *testing.T) {
	src := "abcd\nefgh\n" // block text = "abcd\nefgh\n" = 10 chars -> ceil(10/4)=3
	ix, id := indexOne(t, "r", "t.txt", "/abs/t.txt", src)
	res := []rank.RankedResult{{
		Blob:      id,
		Files:     []index.FileRef{ref("r", "t.txt", "/abs/t.txt")},
		Score:     1.0,
		LineSpans: []rank.LineSpan{span(1, 2)},
	}}
	win := Assemble(ix, res, Options{TokenBudget: 100000, ContextLines: 0})
	if win.TokenEstimate != 3 {
		t.Errorf("ceil(10/4)=3 expected, got %d", win.TokenEstimate)
	}
}

// ---- Ordering ----

func TestOrderingByScoreDesc(t *testing.T) {
	srcA := "A1\nA2\nA3\n"
	srcB := "B1\nB2\nB3\n"
	srcC := "C1\nC2\nC3\n"
	ix := index.New()
	ix.AddFile("r", "a.txt", "/abs/a.txt", sha(srcA), []byte(srcA))
	ix.AddFile("r", "b.txt", "/abs/b.txt", sha(srcB), []byte(srcB))
	ix.AddFile("r", "c.txt", "/abs/c.txt", sha(srcC), []byte(srcC))
	// Provide in non-score order to prove the sort, not input order, decides.
	res := []rank.RankedResult{
		{Blob: 0, Files: []index.FileRef{ref("r", "a.txt", "/abs/a.txt")}, Score: 0.3, LineSpans: []rank.LineSpan{span(2, 2)}},
		{Blob: 1, Files: []index.FileRef{ref("r", "b.txt", "/abs/b.txt")}, Score: 0.9, LineSpans: []rank.LineSpan{span(2, 2)}},
		{Blob: 2, Files: []index.FileRef{ref("r", "c.txt", "/abs/c.txt")}, Score: 0.6, LineSpans: []rank.LineSpan{span(2, 2)}},
	}
	win := Assemble(ix, res, Options{TokenBudget: 100000, ContextLines: 0})
	if len(win.Blocks) != 3 {
		t.Fatalf("want 3 blocks, got %d", len(win.Blocks))
	}
	gotOrder := []string{win.Blocks[0].AbsPath, win.Blocks[1].AbsPath, win.Blocks[2].AbsPath}
	want := []string{"/abs/b.txt", "/abs/c.txt", "/abs/a.txt"}
	for i := range want {
		if gotOrder[i] != want[i] {
			t.Errorf("ordering wrong: got %v want %v", gotOrder, want)
			break
		}
	}
}

// ---- Edge cases ----

func TestEmptyResults(t *testing.T) {
	ix := index.New()
	win := Assemble(ix, nil, Options{})
	if len(win.Blocks) != 0 {
		t.Errorf("empty results -> no blocks, got %d", len(win.Blocks))
	}
	if win.Truncated {
		t.Errorf("empty results -> Truncated false")
	}
	if win.TokenEstimate != 0 {
		t.Errorf("empty results -> TokenEstimate 0, got %d", win.TokenEstimate)
	}
}

func TestDefaultsApplied(t *testing.T) {
	// ContextLines<=0 -> default 3. Hit on line 5 of a 20-line plain file should
	// pad to 2..8 (default 3, indent fallback no-op on brace/indent-free text).
	var sb strings.Builder
	for i := 1; i <= 20; i++ {
		sb.WriteString("line\n")
	}
	src := sb.String()
	ix, id := indexOne(t, "r", "t.txt", "/abs/t.txt", src)
	res := []rank.RankedResult{{
		Blob:      id,
		Files:     []index.FileRef{ref("r", "t.txt", "/abs/t.txt")},
		Score:     1.0,
		LineSpans: []rank.LineSpan{span(5, 5)},
	}}
	win := Assemble(ix, res, Options{}) // all defaults
	if len(win.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(win.Blocks))
	}
	b := win.Blocks[0]
	if b.StartLine != 2 || b.EndLine != 8 {
		t.Errorf("default ContextLines=3 should pad to 2-8, got %d-%d", b.StartLine, b.EndLine)
	}
}

func TestMissingBlobSkipped(t *testing.T) {
	ix := index.New()
	ix.AddFile("r", "a.txt", "/abs/a.txt", sha("a"), []byte("a\nb\n"))
	res := []rank.RankedResult{
		{Blob: 999, Files: []index.FileRef{ref("r", "x", "/abs/x")}, Score: 1.0, LineSpans: []rank.LineSpan{span(1, 1)}},
		{Blob: 0, Files: []index.FileRef{ref("r", "a.txt", "/abs/a.txt")}, Score: 0.5, LineSpans: []rank.LineSpan{span(1, 1)}},
	}
	// Blob 999 is absent; should be skipped without panic, blob 0 still emitted.
	win := Assemble(ix, res, Options{TokenBudget: 100000})
	if len(win.Blocks) != 1 || win.Blocks[0].AbsPath != "/abs/a.txt" {
		t.Fatalf("missing blob should be skipped, valid one kept: %+v", win.Blocks)
	}
}

func TestResultWithNoFilesSkipped(t *testing.T) {
	ix := index.New()
	ix.AddFile("r", "a.txt", "/abs/a.txt", sha("a"), []byte("a\nb\n"))
	res := []rank.RankedResult{
		{Blob: 0, Files: nil, Score: 1.0, LineSpans: []rank.LineSpan{span(1, 1)}},
	}
	win := Assemble(ix, res, Options{TokenBudget: 100000})
	if len(win.Blocks) != 0 {
		t.Fatalf("result with no FileRefs should be skipped, got %d blocks", len(win.Blocks))
	}
}

// Block reports Files[0] deterministically when a result has multiple refs.
func TestReportsFirstFileRef(t *testing.T) {
	ix := index.New()
	// Same content under two paths -> deduped to one blob with two FileRefs.
	ix.AddFile("r1", "a.txt", "/abs/a.txt", sha("dup"), []byte("dup\nx\n"))
	ix.AddFile("r2", "b.txt", "/abs/b.txt", sha("dup"), []byte("dup\nx\n"))
	res := []rank.RankedResult{{
		Blob: 0,
		Files: []index.FileRef{
			ref("r1", "a.txt", "/abs/a.txt"),
			ref("r2", "b.txt", "/abs/b.txt"),
		},
		Score:     1.0,
		LineSpans: []rank.LineSpan{span(1, 1)},
	}}
	win := Assemble(ix, res, Options{TokenBudget: 100000})
	if len(win.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(win.Blocks))
	}
	b := win.Blocks[0]
	if b.Repo != "r1" || b.RelPath != "a.txt" || b.AbsPath != "/abs/a.txt" {
		t.Errorf("should report Files[0], got %+v", b)
	}
}
