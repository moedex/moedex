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
		t.Logf("  %-26s recall=%.3f prec=%.3f mrr=%.3f ndcg=%.3f",
			q.Query, q.RecallAtK, q.PrecAtK, q.MRR, q.NDCGAtK)
	}
	t.Logf("  MEAN  recall=%.3f prec=%.3f mrr=%.3f ndcg=%.3f",
		rep.MeanRecall, rep.MeanPrec, rep.MeanMRR, rep.MeanNDCG)
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

	// Polyglot symbol extraction (BuildMulti dispatches by file extension): the 3
	// Go files (refund.go, charge.go, index.go), the 2 C# files (RefundOrder.cs,
	// SslOrderService.cs) and the 2 TS files (refund.component.ts, order.service.ts)
	// all carry symbols; the 2 SQL and 1 YAML files have no extractor and
	// contribute none. 3 + 2 + 2 = 7.
	if symCount != 7 {
		t.Errorf("expected 7 blobs with symbols (3 Go + 2 C# + 2 TS), got %d", symCount)
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
