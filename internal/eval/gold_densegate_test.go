//go:build onnx

package eval

import (
	"context"
	"os"
	"testing"

	"moedex/internal/embed"
)

// TestCorpusDenseGateSweep sweeps the dense confidence gate
// (rank.Config.DenseMinScore) to find the value that is PURELY ADDITIVE — the same
// criterion that set the path gate at 0.6: keep the dense arm's win on the
// synonym-gap stratum (corpusGoldAgentNL) while removing its regression on the
// answerable gold (CorpusGold). It logs a table of (threshold -> answerable vs
// agent-NL NDCG/Recall) against the no-dense baseline; the chosen default is baked
// into rank.Config.withDefaults. The dense store is embedded ONCE and reused across
// thresholds (SetDenseMinScore only rebuilds the ranker), so the sweep is cheap after
// the initial embed. Needs the ONNX runtime; skips cleanly otherwise.
func TestCorpusDenseGateSweep(t *testing.T) {
	ix, _, _, ok := BuildGoldCorpusIndex()
	if !ok {
		t.Skip("gold corpus absent (set MOEDEX_CORPUS_ROOT/MOEDEX_CORPUS or populate ~/.moedex-managed)")
	}
	emb, err := embed.NewONNXEmbedder(os.Getenv("ONNXRUNTIME_LIB_PATH"))
	if err != nil {
		t.Skipf("onnx runtime unavailable (set ONNXRUNTIME_LIB_PATH): %v", err)
	}
	defer emb.Close()

	const k, topK = 5, 20
	ctx := context.Background()
	ans := CorpusGold()
	nl := corpusGoldAgentNL()

	// No-dense baseline (lexical + path + symbol) on both golds.
	base := NewRunner(ix)
	base.EnableSymbols()
	ansBase, err := base.Evaluate(ctx, ans, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	nlBase, err := base.Evaluate(ctx, nl, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("NO-DENSE baseline: answerable NDCG=%.4f Recall=%.4f | agent-NL NDCG=%.4f Recall=%.4f",
		ansBase.MeanNDCG, ansBase.MeanRecall, nlBase.MeanNDCG, nlBase.MeanRecall)

	// Dense runner: embed once, reuse across thresholds.
	dr := NewRunner(ix)
	if _, err := dr.EnableDense(ctx, emb, 40, 8); err != nil {
		t.Skipf("dense build failed (%v)", err)
	}
	dr.EnableSymbols()

	// --- (1) COSINE-SCORE gate sweep (DenseMinScore). -1 disables it. ---
	dr.SetDenseMinQueryTerms(-1) // length gate off for the score sweep
	thresholds := []float64{-1, 0.20, 0.25, 0.30, 0.35, 0.40, 0.45, 0.50, 0.55, 0.60}
	t.Logf("SCORE-GATE sweep (length gate off):")
	t.Logf("%-7s | %-22s | %-22s | additive", "thresh", "answerable NDCG/Rec", "agent-NL NDCG/Rec")
	for _, th := range thresholds {
		dr.SetDenseMinScore(th)
		a, err := dr.Evaluate(ctx, ans, k, topK)
		if err != nil {
			t.Fatal(err)
		}
		n, err := dr.Evaluate(ctx, nl, k, topK)
		if err != nil {
			t.Fatal(err)
		}
		additive := a.MeanNDCG >= ansBase.MeanNDCG-1e-9 &&
			a.MeanRecall >= ansBase.MeanRecall-1e-9 &&
			n.MeanNDCG > nlBase.MeanNDCG+1e-9
		t.Logf("%-7.2f | %.4f / %.4f       | %.4f / %.4f       | %v",
			th, a.MeanNDCG, a.MeanRecall, n.MeanNDCG, n.MeanRecall, additive)
	}

	// --- (2) QUERY-LENGTH gate sweep (DenseMinQueryTerms). -1 disables it. This is
	// the additive mechanism: dense fires only on queries with >= N distinct terms. ---
	dr.SetDenseMinScore(-1) // score gate off for the length sweep
	t.Logf("LENGTH-GATE sweep (score gate off):")
	t.Logf("%-9s | %-22s | %-22s | additive", "minTerms", "answerable NDCG/Rec", "agent-NL NDCG/Rec")
	for _, minTerms := range []int{-1, 3, 4, 5, 6, 7} {
		dr.SetDenseMinQueryTerms(minTerms)
		a, err := dr.Evaluate(ctx, ans, k, topK)
		if err != nil {
			t.Fatal(err)
		}
		n, err := dr.Evaluate(ctx, nl, k, topK)
		if err != nil {
			t.Fatal(err)
		}
		additive := a.MeanNDCG >= ansBase.MeanNDCG-1e-9 &&
			a.MeanRecall >= ansBase.MeanRecall-1e-9 &&
			n.MeanNDCG > nlBase.MeanNDCG+1e-9
		t.Logf("%-9d | %.4f / %.4f       | %.4f / %.4f       | %v",
			minTerms, a.MeanNDCG, a.MeanRecall, n.MeanNDCG, n.MeanRecall, additive)
	}

	// --- GUARD the SHIPPED default (rank.Config.withDefaults DenseMinQueryTerms). 0
	// triggers the default; score gate off. It must be PURELY ADDITIVE: the answerable
	// gold untouched (dense never fires on its short queries) and the synonym-gap
	// stratum lifted. This is the regression gate for the dense gate itself.
	dr.SetDenseMinScore(0)      // off (Rank requires > 0)
	dr.SetDenseMinQueryTerms(0) // 0 -> withDefaults default
	aDef, err := dr.Evaluate(ctx, ans, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	nDef, err := dr.Evaluate(ctx, nl, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("DEFAULT gate: answerable %.4f/%.4f (baseline %.4f/%.4f) | agent-NL %.4f/%.4f (baseline %.4f/%.4f)",
		aDef.MeanNDCG, aDef.MeanRecall, ansBase.MeanNDCG, ansBase.MeanRecall,
		nDef.MeanNDCG, nDef.MeanRecall, nlBase.MeanNDCG, nlBase.MeanRecall)
	if aDef.MeanNDCG < ansBase.MeanNDCG-1e-9 || aDef.MeanRecall < ansBase.MeanRecall-1e-9 {
		t.Errorf("default dense gate REGRESSED the answerable gold (not additive): NDCG %.4f<%.4f or Recall %.4f<%.4f",
			aDef.MeanNDCG, ansBase.MeanNDCG, aDef.MeanRecall, ansBase.MeanRecall)
	}
	if nDef.MeanNDCG <= nlBase.MeanNDCG {
		t.Errorf("default dense gate gave NO agent-NL lift: %.4f <= baseline %.4f", nDef.MeanNDCG, nlBase.MeanNDCG)
	}
}
