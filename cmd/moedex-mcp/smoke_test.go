package main

import (
	"context"
	"strings"
	"testing"

	"moedex/internal/index"
	"moedex/internal/mcp"
	"moedex/internal/rank"
	"moedex/internal/symbol"
	"moedex/internal/tokenindex"
)

// smokeSrc is a Go file whose body carries a '{' INSIDE a string literal — the
// documented failure mode of contextwin's brace/indent heuristic. A salient hit
// deep inside collectKeys must, with the real symbol layer wired, snap to the
// whole function's true boundaries (lines 3..9). The brace heuristic, fooled by
// the stray '{' in the string, captures a different (wrong) range — so a divergence
// between the symbol-scoped and heuristic windows is positive proof the symbol
// layer's EnclosingBytes fired, not the fallback.
const smokeSrc = `package widgets

func collectKeys(m map[string]int) []string {
	prefix := "open brace { then text"
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, prefix+k)
	}
	return keys
}

func unrelated() {
	println("noise")
}
`

// buildSmokeSearcher reproduces cmd/moedex-mcp/main.go's wiring (index -> token
// index -> ranker -> symbol layer -> IndexSearcher) over an in-memory blob, so
// the test drives the exact SearchContext path the binary serves.
func buildSmokeSearcher(t *testing.T) (*mcp.IndexSearcher, *symbol.Index) {
	t.Helper()
	ix := index.New()
	ix.AddFile("widgets", "widgets.go", "/abs/widgets.go", "sha-smoke", []byte(smokeSrc))
	ti := tokenindex.Build(ix)
	ranker := rank.New(ix, ti, nil, nil, rank.Config{})
	symIdx := symbol.BuildMulti(ix)
	if symIdx.NumBlobs() < 1 {
		t.Fatalf("symbol layer empty: BuildMulti found no symbols in a Go file")
	}
	ranker.SetSymbols(symIdx)
	searcher := mcp.NewIndexSearcher(ix, ranker, 20)
	return searcher, symIdx
}

func TestSmoke_SymbolScopingFires(t *testing.T) {
	ctx := context.Background()
	searcher, symIdx := buildSmokeSearcher(t)

	// Baseline: heuristic-only (EnclosingBytes NOT wired).
	winHeur, err := searcher.SearchContext(ctx, "prefix keys", 8000, 20)
	if err != nil {
		t.Fatalf("heuristic SearchContext: %v", err)
	}
	if len(winHeur.Blocks) == 0 {
		t.Fatalf("heuristic search returned no blocks")
	}

	// Now wire the real symbol layer, exactly as main.go does.
	searcher.SetEnclosingBytes(symIdx.EnclosingBytesFunc())
	winSym, err := searcher.SearchContext(ctx, "prefix keys", 8000, 20)
	if err != nil {
		t.Fatalf("symbol-scoped SearchContext: %v", err)
	}
	if len(winSym.Blocks) == 0 {
		t.Fatalf("symbol-scoped search returned no blocks")
	}

	bSym := winSym.Blocks[0]
	bHeur := winHeur.Blocks[0]
	t.Logf("heuristic block:  %s:%d-%d", bHeur.RelPath, bHeur.StartLine, bHeur.EndLine)
	t.Logf("symbol   block:   %s:%d-%d", bSym.RelPath, bSym.StartLine, bSym.EndLine)

	// Evidence #1: the symbol-scoped block is the WHOLE collectKeys function,
	// lines 3..10 (header through closing brace). The symbol layer reports the
	// function's real range; the heuristic cannot, because of the brace-in-string.
	if bSym.StartLine != 3 || bSym.EndLine != 10 {
		t.Errorf("symbol scoping should snap to collectKeys (lines 3-10), got %d-%d:\n%s",
			bSym.StartLine, bSym.EndLine, bSym.Text)
	}
	if !strings.Contains(bSym.Text, "func collectKeys(") || !strings.Contains(bSym.Text, "return keys") {
		t.Errorf("symbol block missing function head/tail:\n%s", bSym.Text)
	}
	if strings.Contains(bSym.Text, "func unrelated()") {
		t.Errorf("symbol block over-captured into the next function:\n%s", bSym.Text)
	}

	// Evidence #2: it DIVERGES from the heuristic result. If both produced the
	// same range, we could not claim the symbol path fired — this guards against a
	// silent fallback.
	if bSym.StartLine == bHeur.StartLine && bSym.EndLine == bHeur.EndLine {
		t.Errorf("symbol and heuristic windows are identical (%d-%d) — cannot prove EnclosingBytes fired over the fallback",
			bSym.StartLine, bSym.EndLine)
	}

	// Evidence #3: token-budgeted answer is well-formed.
	if winSym.TokenEstimate <= 0 {
		t.Errorf("expected a positive token estimate, got %d", winSym.TokenEstimate)
	}
	t.Logf("symbol-scoped, token-budgeted: 1 block, ~%d tokens, symbol-scoped boundaries confirmed", winSym.TokenEstimate)
}
