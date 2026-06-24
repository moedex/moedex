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

// buildIndexWithPaths makes a tiny index from {relPath, content} pairs (one blob
// each), so tests can exercise the filename/path arm with meaningful paths.
func buildIndexWithPaths(pairs ...[2]string) *index.Index {
	ix := index.New()
	for i, p := range pairs {
		rel, content := p[0], p[1]
		sha := string(rune('a'+i)) + rel + content
		ix.AddFile("repo", rel, "/abs/"+rel, sha, []byte(content))
	}
	return ix
}

// hasPath reports whether any result is backed by a file with the given RelPath.
func hasPath(res []RankedResult, rel string) bool {
	for _, r := range res {
		for _, f := range r.Files {
			if f.RelPath == rel {
				return true
			}
		}
	}
	return false
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

// TestPathArmSurfacesFilenameOnlyMatch is the path arm's reason to exist: a blob
// whose query terms appear ONLY in its file path (never in the content the lexical
// arm scores) is still surfaced. This is the real "federated server" case, where
// "federated" lives only in mysql_create_federated_server.sql's name.
func TestPathArmSurfacesFilenameOnlyMatch(t *testing.T) {
	// Content deliberately omits "federated" and "server"; both live only in the path.
	ix := buildIndexWithPaths(
		[2]string{"db/federated_server_setup.sql", "select concat host database user password"},
		[2]string{"db/other.sql", "select count star from table"},
	)
	ti := tokenindex.Build(ix)

	// Path arm ON (default): the filename-only match is found.
	on := New(ix, ti, nil, nil, Config{})
	res, err := on.Rank(context.Background(), "federated server", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !hasPath(res, "db/federated_server_setup.sql") {
		t.Errorf("path arm should surface a filename-only match; got %+v", res)
	}

	// Path arm OFF: the content has neither term, so the blob is not retrievable.
	off := New(ix, ti, nil, nil, Config{PathMinCoverage: -1})
	res, err = off.Rank(context.Background(), "federated server", 10)
	if err != nil {
		t.Fatal(err)
	}
	if hasPath(res, "db/federated_server_setup.sql") {
		t.Errorf("with the path arm disabled, a filename-only match must NOT be surfaced; got %+v", res)
	}
}

// TestPathArmCoverageGate checks the path arm's coverage gate: a path that matches
// only one term of a two-term query is gated out at the 0.6 default (which needs
// both) but votes when the gate is loosened to 0.5. The true two-term match always
// fires. Content carries none of the query terms, so the path arm is the only
// signal in play.
func TestPathArmCoverageGate(t *testing.T) {
	ix := buildIndexWithPaths(
		[2]string{"app/order.service.ts", "zzz yyy www"}, // path has order+service (2/2)
		[2]string{"app/admin.service.ts", "qqq rrr sss"}, // path has service only (1/2)
	)
	ti := tokenindex.Build(ix)

	// Default gate 0.6 -> minHits=2 for a 2-term query: the 1/2 path is suppressed.
	def := New(ix, ti, nil, nil, Config{})
	res, err := def.Rank(context.Background(), "order service", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !hasPath(res, "app/order.service.ts") {
		t.Errorf("the full 2/2 path match should always fire; got %+v", res)
	}
	if hasPath(res, "app/admin.service.ts") {
		t.Errorf("default gate (0.6) should suppress the weak 1/2 path match; got %+v", res)
	}

	// Loosened gate 0.5 -> minHits=1: the 1/2 path now votes and is surfaced.
	loose := New(ix, ti, nil, nil, Config{PathMinCoverage: 0.5})
	res, err = loose.Rank(context.Background(), "order service", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !hasPath(res, "app/admin.service.ts") {
		t.Errorf("loosened gate (0.5) should let the 1/2 path match vote; got %+v", res)
	}
}

// TestPathArmDisabledUnchanged asserts a negative PathMinCoverage leaves ranking
// identical to a build where no path token could ever match — the arm is strictly
// gated off and never perturbs scores.
func TestPathArmDisabledUnchanged(t *testing.T) {
	// Query terms appear in content (so the lexical arm drives ranking) but NOT in
	// any path, so even an enabled path arm contributes nothing here — disabling it
	// must therefore produce byte-identical results.
	ix := buildIndexWithPaths(
		[2]string{"a.txt", "database connection here database"},
		[2]string{"b.txt", "database pool"},
	)
	ti := tokenindex.Build(ix)

	onRes, err := New(ix, ti, nil, nil, Config{}).Rank(context.Background(), "database", 10)
	if err != nil {
		t.Fatal(err)
	}
	offRes, err := New(ix, ti, nil, nil, Config{PathMinCoverage: -1}).Rank(context.Background(), "database", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(onRes) != len(offRes) {
		t.Fatalf("path arm changed result count on a no-path-match query: %d vs %d", len(onRes), len(offRes))
	}
	for i := range onRes {
		if onRes[i].Blob != offRes[i].Blob || onRes[i].Score != offRes[i].Score {
			t.Errorf("path arm perturbed a no-path-match query at %d: %+v vs %+v", i, onRes[i], offRes[i])
		}
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
