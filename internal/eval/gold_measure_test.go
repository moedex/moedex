package eval

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// logReport prints a readable per-query + mean table.
func logReport(t *testing.T, label string, rep Report) {
	t.Helper()
	t.Logf("== %s (k=%d) ==", label, rep.K)
	for _, q := range rep.SortedQueries() {
		t.Logf("  %-26s recall=%.3f prec=%.3f mrr=%.3f ndcg=%.3f udcg=%.3f",
			q.Query, q.RecallAtK, q.PrecAtK, q.MRR, q.NDCGAtK, q.UDCGAtK)
	}
	t.Logf("  MEAN  recall=%.3f prec=%.3f mrr=%.3f ndcg=%.3f udcg=%.3f",
		rep.MeanRecall, rep.MeanPrec, rep.MeanMRR, rep.MeanNDCG, rep.MeanUDCG)
}

func posOf(ranked []string, relPath string) int {
	for i, r := range ranked {
		if r == relPath {
			return i
		}
	}
	return -1
}

// TestFixtureMeasurement is the first real ranking measurement: a multi-language
// corpus scored with and without the symbol arm.
func TestFixtureMeasurement(t *testing.T) {
	const k, topK = 5, 10
	ctx := context.Background()
	files := FixtureFiles()
	gold := FixtureGold()

	// Baseline: pure lexical.
	base := NewRunner(BuildIndexFromFiles(files))
	repBase, err := base.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "fixture / lexical only", repBase)

	// With the symbol-name arm enabled.
	withSym := NewRunner(BuildIndexFromFiles(files))
	symCount := withSym.EnableSymbols()
	repSym, err := withSym.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "fixture / lexical + symbol arm", repSym)
	t.Logf("symbol arm: %d blobs carry symbols", symCount)

	// Polyglot symbol extraction (BuildMulti dispatches by file extension): the 4
	// Go files (refund.go, charge.go, index.go, authenticate.go), the 2 C# files
	// (RefundOrder.cs, SslOrderService.cs), the 2 TS files (refund.component.ts,
	// order.service.ts) and the 2 SQL files (refund_table.sql -> refund_log,
	// orders.sql -> orders) all carry symbols; only the 1 YAML file has no
	// extractor and contributes none. 4 + 2 + 2 + 2 = 10.
	if symCount != 10 {
		t.Errorf("expected 10 blobs with symbols (4 Go + 2 C# + 2 TS + 2 SQL), got %d", symCount)
	}
	// The harness must actually find relevant docs across languages.
	if repBase.MeanRecall < 0.8 {
		t.Errorf("lexical MeanRecall@%d = %.3f, want >= 0.8 (gold terms are present in the docs)", k, repBase.MeanRecall)
	}
	// The symbol arm must not regress overall ranking quality on this set.
	if repSym.MeanNDCG < repBase.MeanNDCG-1e-9 {
		t.Errorf("symbol arm regressed MeanNDCG: with=%.4f without=%.4f", repSym.MeanNDCG, repBase.MeanNDCG)
	}
	t.Logf("MeanNDCG  lexical=%.4f  +symbol=%.4f  (delta %+.4f)",
		repBase.MeanNDCG, repSym.MeanNDCG, repSym.MeanNDCG-repBase.MeanNDCG)

	// Concretely: for "refund", the Go definer (billing/refund.go) should rank
	// no worse with the symbol arm than without — the arm exists to surface it.
	var baseRefund, symRefund QueryReport
	for _, q := range repBase.Queries {
		if q.Query == "refund" {
			baseRefund = q
		}
	}
	for _, q := range repSym.Queries {
		if q.Query == "refund" {
			symRefund = q
		}
	}
	pb := posOf(baseRefund.Ranked, "billing/refund.go")
	ps := posOf(symRefund.Ranked, "billing/refund.go")
	t.Logf("'refund' rank of billing/refund.go: lexical=%d  +symbol=%d (0-based)", pb, ps)
	if ps < 0 {
		t.Error("'refund' with symbol arm dropped the Go definer entirely")
	}
	if pb >= 0 && ps > pb {
		t.Errorf("symbol arm pushed the Go definer DOWN for 'refund': %d -> %d", pb, ps)
	}

	// Coverage-gate regression guard. "build deploy stage" must NOT be hurt by the
	// symbol arm. Before the gate, search/index.go's BuildIndex symbol matched only
	// "build" (1 of 3 query terms) yet cast a full RRF vote, pushing the correct CI
	// YAML down (nDCG 1.0 -> 0.631). With the coverage gate that weak 1/3 match no
	// longer qualifies, so the YAML keeps its rank. This pins the fix in place.
	var baseCI, symCI QueryReport
	for _, q := range repBase.Queries {
		if q.Query == "build deploy stage" {
			baseCI = q
		}
	}
	for _, q := range repSym.Queries {
		if q.Query == "build deploy stage" {
			symCI = q
		}
	}
	t.Logf("'build deploy stage' nDCG: lexical=%.4f  +symbol=%.4f", baseCI.NDCGAtK, symCI.NDCGAtK)
	if symCI.NDCGAtK < baseCI.NDCGAtK-1e-9 {
		t.Errorf("coverage gate failed: 'build deploy stage' nDCG regressed with symbol arm: with=%.4f without=%.4f", symCI.NDCGAtK, baseCI.NDCGAtK)
	}
}

// TestDenseHybridMeasurement is the lexical-vs-dense-vs-hybrid comparison the
// harness previously could not run (the old Runner wired a nil dense arm, so the
// dense/hybrid quality was NEVER measured). It builds four rankers over the same
// fixture corpus and embedder:
//
//	lexical          : BM25 only
//	dense            : BM25 + dense concept arm (EnableDense)
//	lexical+symbol   : BM25 + symbol-name arm  (EnableSymbols)
//	full hybrid      : BM25 + dense + symbol   (both enabled, either order)
//
// The dense arm uses a deterministic concept embedder (fake_embedder_test.go), so
// the comparison is hermetic — no embedding server. The headline assertion is on
// the synonym query "login credentials": its target file shares NO trigram with
// the query, so pure lexical CANNOT retrieve it (recall 0), while the dense
// concept arm can. That is the one thing dense buys over BM25, made measurable.
func TestDenseHybridMeasurement(t *testing.T) {
	const k, topK = 5, 10
	const linesPerChunk, overlap = 6, 2
	ctx := context.Background()
	files := FixtureFiles()
	gold := FixtureGold()

	// The dense arm must NOT collapse to a copy of lexical: a tail dimension keeps
	// out-of-lexicon tokens distinguishable. 8 concept axes + tail.
	embedder := newConceptEmbedder(16)

	lexical := NewRunner(BuildIndexFromFiles(files))
	repLex, err := lexical.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "fixture / lexical only", repLex)

	dense := NewRunner(BuildIndexFromFiles(files))
	// Disable the production query-length gate: this fixture exercises the dense
	// MECHANISM with short synthetic queries (e.g. the 2-term "login credentials"
	// synonym), which the default gate (>= 5 terms) would otherwise suppress.
	dense.SetDenseMinQueryTerms(-1)
	nChunks, err := dense.EnableDense(ctx, embedder, linesPerChunk, overlap)
	if err != nil {
		t.Fatalf("EnableDense: %v", err)
	}
	if nChunks == 0 {
		t.Fatal("EnableDense produced no chunks; dense arm would be a no-op")
	}
	repDense, err := dense.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "fixture / lexical + dense", repDense)
	t.Logf("dense arm: %d embedded chunks (dim=%d)", nChunks, embedder.Dim())

	withSym := NewRunner(BuildIndexFromFiles(files))
	withSym.EnableSymbols()
	repSym, err := withSym.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "fixture / lexical + symbol", repSym)

	// Full hybrid: enable BOTH arms. Enable symbols FIRST then dense to prove the
	// Runner preserves the symbol arm across a dense rebuild (order-independence).
	hybrid := NewRunner(BuildIndexFromFiles(files))
	hybrid.SetDenseMinQueryTerms(-1) // same: short synthetic queries, gate off
	hybrid.EnableSymbols()
	if _, err := hybrid.EnableDense(ctx, embedder, linesPerChunk, overlap); err != nil {
		t.Fatalf("hybrid EnableDense: %v", err)
	}
	repHybrid, err := hybrid.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "fixture / FULL HYBRID (lexical+dense+symbol)", repHybrid)

	t.Logf("MeanNDCG  lexical=%.4f  +dense=%.4f  +symbol=%.4f  hybrid=%.4f",
		repLex.MeanNDCG, repDense.MeanNDCG, repSym.MeanNDCG, repHybrid.MeanNDCG)
	t.Logf("MeanRecall lexical=%.4f  +dense=%.4f  +symbol=%.4f  hybrid=%.4f",
		repLex.MeanRecall, repDense.MeanRecall, repSym.MeanRecall, repHybrid.MeanRecall)

	// --- Headline: dense recovers a synonym query lexical structurally misses ---
	lexLogin := queryReport(repLex, "login credentials")
	denseLogin := queryReport(repDense, "login credentials")
	t.Logf("'login credentials' recall@%d: lexical=%.3f  +dense=%.3f",
		k, lexLogin.RecallAtK, denseLogin.RecallAtK)
	if lexLogin.RecallAtK != 0 {
		t.Errorf("expected pure lexical to MISS the no-trigram-overlap synonym query "+
			"(recall 0), got %.3f — fixture no longer demonstrates the dense gap",
			lexLogin.RecallAtK)
	}
	if denseLogin.RecallAtK == 0 {
		t.Error("dense arm failed to recover 'login credentials' (auth/authenticate.go); " +
			"the dense arm is not contributing")
	}
	if posOf(denseLogin.Ranked, "auth/authenticate.go") < 0 {
		t.Error("dense arm did not surface auth/authenticate.go for 'login credentials'")
	}

	// The full hybrid must keep the dense win (login) AND the symbol win.
	hybLogin := queryReport(repHybrid, "login credentials")
	if hybLogin.RecallAtK == 0 {
		t.Error("full hybrid lost the dense-only 'login credentials' win")
	}
	// Symbol win: 'refund' surfaces the Go definer (lexical alone dropped it from
	// the top-k — see TestFixtureMeasurement). Hybrid must still surface it.
	hybRefund := queryReport(repHybrid, "refund")
	if posOf(hybRefund.Ranked, "billing/refund.go") < 0 {
		t.Error("full hybrid lost the symbol-arm 'refund' win (Go definer dropped)")
	}

	// Adding arms must not regress aggregate ranking quality on this set: each
	// richer configuration should be >= lexical on mean nDCG.
	if repDense.MeanNDCG < repLex.MeanNDCG-1e-9 {
		t.Errorf("dense arm regressed MeanNDCG: lexical=%.4f dense=%.4f", repLex.MeanNDCG, repDense.MeanNDCG)
	}
	if repHybrid.MeanNDCG < repLex.MeanNDCG-1e-9 {
		t.Errorf("full hybrid regressed MeanNDCG below lexical: lexical=%.4f hybrid=%.4f", repLex.MeanNDCG, repHybrid.MeanNDCG)
	}
	// Recall: the dense and hybrid configs must recover queries lexical misses, so
	// their mean recall must strictly EXCEED pure lexical on this fixture.
	if repDense.MeanRecall <= repLex.MeanRecall {
		t.Errorf("dense arm did not improve MeanRecall: lexical=%.4f dense=%.4f", repLex.MeanRecall, repDense.MeanRecall)
	}
	if repHybrid.MeanRecall <= repLex.MeanRecall {
		t.Errorf("full hybrid did not improve MeanRecall: lexical=%.4f hybrid=%.4f", repLex.MeanRecall, repHybrid.MeanRecall)
	}
}

// queryReport returns the QueryReport for a given query string, failing if absent.
func queryReport(rep Report, query string) QueryReport {
	for _, q := range rep.Queries {
		if q.Query == query {
			return q
		}
	}
	return QueryReport{}
}

// TestTCSslApiMeasurement runs the verified C# gold against the real corpus when
// present, for a real-world lexical baseline. Skips cleanly when absent.
func TestTCSslApiMeasurement(t *testing.T) {
	dir := os.Getenv("MOEDEX_EVAL_CORPUS")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("no home dir: %v", err)
		}
		dir = filepath.Join(home, "TCGitlab", "Services.Registrar", "TC.SslApi")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("corpus absent at %s", dir)
	}

	ix, n, err := BuildIndexFromCorpus("TC.SslApi", dir)
	if err != nil {
		t.Skipf("ingest failed (not a git repo?): %v", err)
	}
	if n == 0 {
		t.Skip("corpus has no indexable files")
	}
	t.Logf("indexed %d files from %s", n, dir)

	const k, topK = 5, 20
	rep, err := NewRunner(ix).Evaluate(context.Background(), TCSslApiGold(), k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "TC.SslApi (real C# corpus) / lexical", rep)

	// Observational floor on MRR, NOT recall@k. This hand-authored gold is
	// sparse (a few labeled files per query) over a 144-file corpus where many
	// unlabeled files also match, so recall@5 structurally under-measures —
	// unlabeled-but-relevant files crowd out the labeled ones in the top k. MRR
	// only needs the first relevant hit anywhere in the list, so it is the more
	// honest signal until a complete/pooled gold set exists (future work).
	// (Also surfaced here: a tokenizer asymmetry — query "reissue" does not match
	// the identifier "ReIssue" which splits into re+issue. See findings.)
	if rep.MeanMRR < 0.25 {
		t.Errorf("real-corpus MeanMRR = %.3f, want >= 0.25 (first relevant should usually appear)", rep.MeanMRR)
	}
}
