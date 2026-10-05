package eval

import (
	"context"
	"testing"
)

func TestCorpusGoldGate(t *testing.T) {
	ix, n, perRepo, ok := BuildGoldCorpusIndex()
	if !ok {
		t.Skip("gold corpus absent (set MOEDEX_CORPUS_ROOT/MOEDEX_CORPUS or populate ~/.moedex-managed)")
	}
	gold := CorpusGold()
	t.Logf("pooled index: %d files; per-repo kept = %v; %d gold queries", n, perRepo, len(gold))

	const k, topK = 5, 20
	ctx := context.Background()

	run := NewRunner(ix)

	repPath, err := run.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "GATE / lexical+path", repPath)

	run.DisablePathArm()
	repLexOnly, err := run.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "GATE / lexical only", repLexOnly)

	symCount := run.EnableSymbols()
	repSymNoPath, err := run.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "GATE / lexical+symbol", repSymNoPath)

	run.SetPathCoverage(0.6)
	repFull, err := run.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "GATE / lexical+path+symbol (PRODUCTION)", repFull)
	t.Logf("symbol arm: %d blobs carry symbols", symCount)

	t.Logf("GATE MeanNDCG   lexOnly=%.4f  +path=%.4f  +symbol=%.4f  PRODUCTION=%.4f",
		repLexOnly.MeanNDCG, repPath.MeanNDCG, repSymNoPath.MeanNDCG, repFull.MeanNDCG)
	t.Logf("GATE MeanMRR    lexOnly=%.4f  PRODUCTION=%.4f", repLexOnly.MeanMRR, repFull.MeanMRR)
	t.Logf("GATE MeanRecall lexOnly=%.4f  PRODUCTION=%.4f", repLexOnly.MeanRecall, repFull.MeanRecall)
	t.Logf("GATE MeanUDCG   lexOnly=%.4f  +path=%.4f  +symbol=%.4f  PRODUCTION=%.4f",
		repLexOnly.MeanUDCG, repPath.MeanUDCG, repSymNoPath.MeanUDCG, repFull.MeanUDCG)

	if symCount == 0 {
		t.Error("symbol arm attached to 0 blobs; the C#/TS/CF/SQL symbol lane is dead")
	}

	var (
		minLexNDCG    = corpusThreshold("minLexNDCG")
		minLexRecall  = corpusThreshold("minLexRecall")
		minLexMRR     = corpusThreshold("minLexMRR")
		minFullNDCG   = corpusThreshold("minFullNDCG")
		minFullRecall = corpusThreshold("minFullRecall")
		minFullMRR    = corpusThreshold("minFullMRR")
		watchFullUDCG = corpusThreshold("watchFullUDCG")
	)
	if repLexOnly.MeanNDCG < minLexNDCG {
		t.Errorf("lexical-only MeanNDCG = %.4f, below floor %.4f (regression)", repLexOnly.MeanNDCG, minLexNDCG)
	}
	if repLexOnly.MeanRecall < minLexRecall {
		t.Errorf("lexical-only MeanRecall@%d = %.4f, below floor %.4f (regression)", k, repLexOnly.MeanRecall, minLexRecall)
	}
	if repLexOnly.MeanMRR < minLexMRR {
		t.Errorf("lexical-only MeanMRR = %.4f, below floor %.4f (regression)", repLexOnly.MeanMRR, minLexMRR)
	}
	if repFull.MeanNDCG < minFullNDCG {
		t.Errorf("PRODUCTION MeanNDCG = %.4f, below floor %.4f (regression)", repFull.MeanNDCG, minFullNDCG)
	}
	if repFull.MeanRecall < minFullRecall {
		t.Errorf("PRODUCTION MeanRecall@%d = %.4f, below floor %.4f (regression)", k, repFull.MeanRecall, minFullRecall)
	}
	if repFull.MeanMRR < minFullMRR {
		t.Errorf("PRODUCTION MeanMRR = %.4f, below floor %.4f (regression)", repFull.MeanMRR, minFullMRR)
	}

	if repFull.MeanUDCG < watchFullUDCG {
		t.Logf("WATCH: PRODUCTION MeanUDCG = %.4f below soft floor %.4f — distractor leakage into top-k is up (not a hard failure)", repFull.MeanUDCG, watchFullUDCG)
	}

	if repPath.MeanNDCG < repLexOnly.MeanNDCG-1e-9 {
		t.Errorf("path arm regressed NDCG below lexical: lexOnly=%.4f +path=%.4f", repLexOnly.MeanNDCG, repPath.MeanNDCG)
	}
	if repSymNoPath.MeanNDCG < repLexOnly.MeanNDCG-1e-9 {
		t.Errorf("symbol arm regressed NDCG below lexical: lexOnly=%.4f +symbol=%.4f", repLexOnly.MeanNDCG, repSymNoPath.MeanNDCG)
	}
	if repFull.MeanNDCG < repLexOnly.MeanNDCG-1e-9 {
		t.Errorf("production stack regressed NDCG below plain lexical: lexOnly=%.4f full=%.4f", repLexOnly.MeanNDCG, repFull.MeanNDCG)
	}
}
