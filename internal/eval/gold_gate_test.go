package eval

import (
	"context"
	"testing"
)

// TestCorpusGoldGate is the HARD regression gate over the two-annotator,
// reconciled pooled gold set (CorpusGold; see gold_corpus.go). Unlike the
// observational measurement tests, this one ASSERTS: the lexical and
// lexical+symbol arms must clear thresholds set below the measured baseline, so a
// ranking regression reds this test rather than silently drifting a logged number.
//
// It uses NO embedder and NO build tag — only BM25 (lexical) and the symbol arm,
// both of which run in the default build. The dense arm is measured separately
// (TestCorpusONNXMeasurement, -tags onnx) because it needs the ONNX runtime; this
// gate must be runnable in plain `go test ./internal/eval/`.
//
// Skips cleanly when the corpus root is absent (set MOEDEX_CORPUS_ROOT or place
// repos under ~/TCGitlab), so it never reds CI on a machine without the corpus.
//
// THRESHOLDS are deliberately set a margin below the measured baseline (recorded
// in the comments next to each assertion). They are a floor that catches real
// regressions, not a tight pin — the set is only 24 single-domain queries, so a
// few-hundredths wobble is noise, a tenth is a regression.
func TestCorpusGoldGate(t *testing.T) {
	ix, n, perRepo, ok := BuildGoldCorpusIndex()
	if !ok {
		t.Skip("gold corpus absent (set MOEDEX_CORPUS_ROOT or place repos under ~/TCGitlab)")
	}
	gold := CorpusGold()
	t.Logf("pooled index: %d files; per-repo kept = %v; %d gold queries", n, perRepo, len(gold))

	const k, topK = 5, 20
	ctx := context.Background()

	// Lexical baseline.
	lexical := NewRunner(ix)
	repLex, err := lexical.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "GATE / lexical", repLex)

	// Lexical + symbol arm (fresh index so the corpora are independent).
	ixSym, _, _, _ := BuildGoldCorpusIndex()
	withSym := NewRunner(ixSym)
	symCount := withSym.EnableSymbols()
	repSym, err := withSym.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "GATE / lexical + symbol", repSym)
	t.Logf("symbol arm: %d blobs carry symbols", symCount)

	t.Logf("GATE MeanNDCG   lexical=%.4f  +symbol=%.4f", repLex.MeanNDCG, repSym.MeanNDCG)
	t.Logf("GATE MeanMRR    lexical=%.4f  +symbol=%.4f", repLex.MeanMRR, repSym.MeanMRR)
	t.Logf("GATE MeanRecall lexical=%.4f  +symbol=%.4f", repLex.MeanRecall, repSym.MeanRecall)

	// All four gold languages (C#/TS/ColdFusion/SQL) now carry symbols, so the
	// symbol arm must actually attach to some blobs.
	if symCount == 0 {
		t.Error("symbol arm attached to 0 blobs; the C#/TS/CF/SQL symbol lane is dead")
	}

	// --- Hard floors (calibrated ~0.06 below the measured baseline) ---------
	// MEASURED BASELINE (2026-06-24, reconciled two-annotator gold + ColdFusion
	// and SQL symbol lanes, 30 queries, 349 files, k=5): lexical NDCG=0.640
	// MRR=0.746 Recall=0.702; +symbol NDCG=0.758 MRR=0.803 Recall=0.784 (180 blobs
	// carry symbols across C#/TS/CF/SQL). The CF symbol arm is the headline lift:
	// e.g. "void transaction" NDCG 0.689->0.964. The SQL arm is neutral here (its
	// 6 queries are already lexical-perfect filename lookups), so +symbol is
	// unchanged from the CF-only baseline. Floors sit a margin below so noise on a
	// small set passes but a real (tenth-scale) regression reds the gate.
	const (
		minLexNDCG   = 0.58
		minLexRecall = 0.64
		minLexMRR    = 0.68
		minSymNDCG   = 0.69
		minSymRecall = 0.72
	)
	if repLex.MeanNDCG < minLexNDCG {
		t.Errorf("lexical MeanNDCG = %.4f, below floor %.4f (regression)", repLex.MeanNDCG, minLexNDCG)
	}
	if repLex.MeanRecall < minLexRecall {
		t.Errorf("lexical MeanRecall@%d = %.4f, below floor %.4f (regression)", k, repLex.MeanRecall, minLexRecall)
	}
	if repLex.MeanMRR < minLexMRR {
		t.Errorf("lexical MeanMRR = %.4f, below floor %.4f (regression)", repLex.MeanMRR, minLexMRR)
	}
	if repSym.MeanNDCG < minSymNDCG {
		t.Errorf("lexical+symbol MeanNDCG = %.4f, below floor %.4f (regression)", repSym.MeanNDCG, minSymNDCG)
	}
	if repSym.MeanRecall < minSymRecall {
		t.Errorf("lexical+symbol MeanRecall@%d = %.4f, below floor %.4f (regression)", k, repSym.MeanRecall, minSymRecall)
	}

	// The symbol arm must never regress aggregate NDCG below pure lexical: it is
	// additive (RRF) and gated, so at worst it is neutral.
	if repSym.MeanNDCG < repLex.MeanNDCG-1e-9 {
		t.Errorf("symbol arm regressed MeanNDCG: lexical=%.4f +symbol=%.4f", repLex.MeanNDCG, repSym.MeanNDCG)
	}
}
