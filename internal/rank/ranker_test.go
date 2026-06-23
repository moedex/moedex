package rank

import (
	"context"
	"hash/fnv"
	"strings"
	"testing"

	"moedex/internal/embed"
	"moedex/internal/index"
	"moedex/internal/symbol"
	"moedex/internal/tokenindex"
)

// buildIndex makes a tiny index from labeled contents (one blob each).
func buildIndex(contents ...string) *index.Index {
	ix := index.New()
	for i, c := range contents {
		sha := string(rune('a'+i)) + c
		ix.AddFile("repo", "f", "/abs/f", sha, []byte(c))
	}
	return ix
}

// fakeEmbedder is a deterministic hashed bag-of-words embedder: texts sharing
// words get higher cosine. No network, no model.
type fakeEmbedder struct{ dim int }

func (f fakeEmbedder) Dim() int { return f.dim }
func (f fakeEmbedder) Embed(_ context.Context, texts []string) ([]embed.Vector, error) {
	out := make([]embed.Vector, len(texts))
	for i, t := range texts {
		v := make(embed.Vector, f.dim)
		for _, w := range strings.Fields(strings.ToLower(t)) {
			h := fnv.New32a()
			h.Write([]byte(w))
			v[h.Sum32()%uint32(f.dim)]++
		}
		out[i] = v
	}
	return out, nil
}

// TestLexicalOrderingByBM25 checks that, with no dense arm, a blob with higher
// term frequency for the query term ranks above one with lower tf, given equal
// document length.
func TestLexicalOrderingByBM25(t *testing.T) {
	ix := buildIndex(
		"database database database connection", // tf(database)=3
		"database connection pool here",         // tf(database)=1
		"unrelated content with words here",     // tf(database)=0
	)
	ti := tokenindex.Build(ix)
	r := New(ix, ti, nil, nil, Config{})

	res, err := r.Rank(context.Background(), "database", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 candidates (blobs containing 'database'), got %d: %+v", len(res), res)
	}
	if res[0].Blob != 0 {
		t.Errorf("expected blob 0 (tf=3) ranked first, got blob %d", res[0].Blob)
	}
	if res[0].Lexical <= res[1].Lexical {
		t.Errorf("expected blob0 lexical (%v) > blob1 (%v)", res[0].Lexical, res[1].Lexical)
	}
	if res[0].Dense != 0 {
		t.Errorf("dense arm disabled: expected Dense=0, got %v", res[0].Dense)
	}
}

// TestLineSpansLocateTerm checks the salient spans point at the lines that
// actually contain the query term.
func TestLineSpansLocateTerm(t *testing.T) {
	content := "package main\n\nfunc target() {\n\tdatabase.Connect()\n}\n"
	ix := buildIndex(content)
	ti := tokenindex.Build(ix)
	r := New(ix, ti, nil, nil, Config{})

	res, err := r.Rank(context.Background(), "database", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 result, got %d", len(res))
	}
	// "database" appears only on line 4.
	found := false
	for _, s := range res[0].LineSpans {
		if s.StartLine <= 4 && 4 <= s.EndLine {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a span covering line 4 (the database line), got %+v", res[0].LineSpans)
	}
}

// TestShortTokenFallback checks that a query whose tokens are all too short for
// a trigram still returns results (candidate set falls back to all blobs) rather
// than dropping everything.
func TestShortTokenFallback(t *testing.T) {
	ix := buildIndex("a b c here", "no match line")
	ti := tokenindex.Build(ix)
	r := New(ix, ti, nil, nil, Config{})

	res, err := r.Rank(context.Background(), "a b", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("short-token query dropped all results; expected the fallback candidate set")
	}
	// blob 0 contains both 'a' and 'b'; it must be present.
	if res[0].Blob != 0 {
		t.Errorf("expected blob 0 (contains a and b) first, got %d", res[0].Blob)
	}
}

// TestDenseArmContributes checks that enabling the dense arm populates Dense and
// that fusion still returns the lexically-relevant blob, and that a blob the
// dense arm surfaces gets a nonzero Dense score.
func TestDenseArmContributes(t *testing.T) {
	ix := buildIndex(
		"alpha alpha database here",
		"beta gamma delta epsilon",
	)
	ti := tokenindex.Build(ix)
	emb := fakeEmbedder{dim: 16}
	store, err := embed.BuildStore(context.Background(), ix, emb, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	r := New(ix, ti, store, emb, Config{})

	res, err := r.Rank(context.Background(), "alpha database", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("no results")
	}
	// blob 0 shares 'alpha' and 'database' with the query both lexically and
	// densely, so it should rank first with both arms nonzero.
	top := res[0]
	if top.Blob != 0 {
		t.Errorf("expected blob 0 first, got %d", top.Blob)
	}
	if top.Lexical <= 0 {
		t.Errorf("expected nonzero lexical for top result, got %v", top.Lexical)
	}
	if top.Dense <= 0 {
		t.Errorf("expected nonzero dense for top result, got %v", top.Dense)
	}
	if top.Score <= 0 {
		t.Errorf("expected nonzero fused score, got %v", top.Score)
	}
}

// TestSymbolArmRaisesDefiner checks the third RRF arm: a blob that DEFINES a
// func named like the query term ranks at or above a blob that merely mentions
// the word in prose, the symbol arm raised the definer's fused Score, and it
// contributed a LineSpan at the definition line. With symbols nil the ranking is
// unchanged.
func TestSymbolArmRaisesDefiner(t *testing.T) {
	// Blob 0 DEFINES Refund; blob 1 only talks about refunds in prose. Pad blob 1
	// so its lexical term frequency for "refund" is high enough that, lexically
	// alone, it would not sit clearly below the definer.
	defSrc := "package billing\n\n// Refund reverses a charge.\nfunc Refund() error {\n\treturn nil\n}\n"
	proseSrc := "we should refund the customer when a refund is requested; refund handling matters\n"
	ix := buildIndex(defSrc, proseSrc)
	ti := tokenindex.Build(ix)

	// Build a symbol index over the corpus and wire it in.
	syms := symbol.Build(ix, symbol.GoExtractor{})

	// Baseline WITHOUT the symbol arm.
	rBase := New(ix, ti, nil, nil, Config{})
	base, err := rBase.Rank(context.Background(), "refund", 10)
	if err != nil {
		t.Fatal(err)
	}
	var baseDefiner RankedResult
	for _, res := range base {
		if res.Blob == 0 {
			baseDefiner = res
		}
	}

	// WITH the symbol arm.
	r := New(ix, ti, nil, nil, Config{})
	r.SetSymbols(syms)
	res, err := r.Rank(context.Background(), "refund", 10)
	if err != nil {
		t.Fatal(err)
	}

	var definer, prose RankedResult
	var haveDef, haveProse bool
	for _, x := range res {
		switch x.Blob {
		case 0:
			definer, haveDef = x, true
		case 1:
			prose, haveProse = x, true
		}
	}
	if !haveDef {
		t.Fatal("definer blob 0 missing from results")
	}
	if !haveProse {
		t.Fatal("prose blob 1 missing from results")
	}

	// Definer ranks at or above prose.
	if res[0].Blob != 0 {
		t.Errorf("expected definer (blob 0) ranked first, got blob %d (%+v)", res[0].Blob, res)
	}
	if definer.Score < prose.Score {
		t.Errorf("definer score %v should be >= prose score %v", definer.Score, prose.Score)
	}

	// The symbol arm raised the definer's fused Score above its symbol-less score.
	if definer.Score <= baseDefiner.Score {
		t.Errorf("symbol arm should raise definer Score: with=%v without=%v", definer.Score, baseDefiner.Score)
	}

	// It contributed a LineSpan at the definition line. "func Refund" is on line 4
	// of defSrc (package, blank, doc, func).
	const defLine = 4
	hit := false
	for _, s := range definer.LineSpans {
		if s.StartLine <= defLine && defLine <= s.EndLine {
			hit = true
		}
	}
	if !hit {
		t.Errorf("expected a LineSpan covering the def line %d, got %+v", defLine, definer.LineSpans)
	}
}

// TestSymbolArmNilUnchanged asserts SetSymbols(nil) leaves results identical to
// never calling it (the arm is strictly opt-in).
func TestSymbolArmNilUnchanged(t *testing.T) {
	defSrc := "package billing\n\nfunc Refund() error {\n\treturn nil\n}\n"
	proseSrc := "we should refund the customer here\n"
	ix := buildIndex(defSrc, proseSrc)
	ti := tokenindex.Build(ix)

	rA := New(ix, ti, nil, nil, Config{})
	a, err := rA.Rank(context.Background(), "refund", 10)
	if err != nil {
		t.Fatal(err)
	}

	rB := New(ix, ti, nil, nil, Config{})
	rB.SetSymbols(nil)
	b, err := rB.Rank(context.Background(), "refund", 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(a) != len(b) {
		t.Fatalf("nil symbol arm changed result count: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Blob != b[i].Blob || a[i].Score != b[i].Score {
			t.Errorf("nil symbol arm changed result %d: %+v vs %+v", i, a[i], b[i])
		}
	}
}

// TestSymbolArmCoverageGate checks Config.SymbolMinCoverage: a weak, coincidental
// match (one common term of a multi-term query) is suppressed, while a fully
// covered match still fires and the gate is a no-op for it.
func TestSymbolArmCoverageGate(t *testing.T) {
	// Blob 0 defines BuildIndex; the lowercase prose makes it a lexical candidate
	// for every query term regardless of identifier case, so the only thing that
	// varies between rankers is the symbol arm's marginal vote. For the query
	// "build deploy stage" the BuildIndex symbol matches only "build" (1 of 3
	// distinct terms = 0.33 coverage), below the 0.5 default gate.
	src := "package search\n\n// build deploy stage pipeline notes\nfunc BuildIndex() {}\n"
	ix := buildIndex(src)
	ti := tokenindex.Build(ix)
	syms := symbol.Build(ix, symbol.GoExtractor{})

	score := func(cfg Config, q string) float64 {
		r := New(ix, ti, nil, nil, cfg)
		r.SetSymbols(syms)
		res, err := r.Rank(context.Background(), q, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range res {
			if x.Blob == 0 {
				return x.Score
			}
		}
		t.Fatalf("blob 0 missing from results for %q", q)
		return 0
	}

	// Default gate (0.5) vs gate disabled (negative). The weak 1/3 match votes only
	// when the gate is off, so the fused score is strictly higher ungated.
	gated := score(Config{}, "build deploy stage")
	ungated := score(Config{SymbolMinCoverage: -1}, "build deploy stage")
	if !(gated < ungated) {
		t.Errorf("coverage gate should suppress the weak 1/3 symbol vote: gated=%v ungated=%v", gated, ungated)
	}

	// A single-term query "build" is fully covered (1/1 >= 0.5), so the gate is a
	// no-op: gated and ungated scores are identical (both include the symbol vote).
	if g, u := score(Config{}, "build"), score(Config{SymbolMinCoverage: -1}, "build"); g != u {
		t.Errorf("full-coverage query should be unaffected by the gate: gated=%v ungated=%v", g, u)
	}
}

// TestTopKRespected checks the topK cap.
func TestTopKRespected(t *testing.T) {
	ix := buildIndex("word one here", "word two here", "word three here", "word four here")
	ti := tokenindex.Build(ix)
	r := New(ix, ti, nil, nil, Config{})

	res, err := r.Rank(context.Background(), "word", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("topK=2 should cap to 2 results, got %d", len(res))
	}
}

// TestNoMatchEmpty checks a query with no lexical or dense hit returns empty.
func TestNoMatchEmpty(t *testing.T) {
	ix := buildIndex("nothing relevant here at all")
	ti := tokenindex.Build(ix)
	r := New(ix, ti, nil, nil, Config{})

	res, err := r.Rank(context.Background(), "zzzzqqqq", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Errorf("expected no results for a non-matching query, got %d: %+v", len(res), res)
	}
}
