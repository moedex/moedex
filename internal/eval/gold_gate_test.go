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

	// The TS gold exercises the symbol arm (C#/TS files carry symbols; SQL does
	// not). The arm must actually attach to some blobs.
	if symCount == 0 {
		t.Error("symbol arm attached to 0 blobs; the TS/C# symbol lane is dead")
	}

	// --- Hard floors (calibrated ~0.06 below the measured baseline) ---------
	// MEASURED BASELINE (2026-06-23, reconciled two-annotator gold, 24 queries,
	// 171 files, k=5): lexical NDCG=0.645 MRR=0.762 Recall=0.713; +symbol
	// NDCG=0.726 MRR=0.762 Recall=0.807. Floors sit a margin below so noise on a
	// small set passes but a real (tenth-scale) regression reds the gate.
	const (
		minLexNDCG   = 0.58
		minLexRecall = 0.65
		minLexMRR    = 0.70
		minSymNDCG   = 0.66
		minSymRecall = 0.74
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
