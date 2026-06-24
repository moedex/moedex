package eval

import (
	"context"
	"testing"
)

// TestCorpusGoldGate is the HARD regression gate over the pooled gold set
// (CorpusGold; see gold_corpus.go). Unlike the observational measurement tests it
// ASSERTS: the production ranking stack must clear thresholds set below the
// measured baseline, so a ranking regression reds this test rather than silently
// drifting a logged number.
//
// It uses NO embedder and NO build tag — only the arms that run in the default
// build: BM25 (lexical), the filename/path arm (on by default), and the symbol
// arm. The dense arm is measured separately (TestCorpusONNXMeasurement, -tags
// onnx) because it needs the ONNX runtime; this gate must be runnable in plain
// `go test ./internal/eval/`.
//
// FOUR-ARM REALITY (2026-06-24). The path arm changed the picture. Measured on
// one index by toggling arms:
//
//	lexical only (no path/sym) NDCG=0.640 MRR=0.746 Recall=0.702   <- historical baseline
//	+symbol (no path)          NDCG=0.758 MRR=0.803 Recall=0.784   <- symbol's standalone value
//	lexical+path (no symbol)   NDCG=0.893 MRR=0.933 Recall=0.921   <- the path arm's big lift
//	+path+symbol (PRODUCTION)  NDCG=0.855 MRR=0.853 Recall=0.913   <- the default stack
//
// KEY FINDING: on this gold the path arm SUBSUMES the symbol arm. The TC corpus
// names files for their concept (RefundOrderValidator.cs, administration.service.ts,
// mysql_create_federated_server.sql), so the filename signal captures — and exceeds
// — what symbol-name matching provided, lifting NDCG +0.25 over plain lexical and
// fixing standing failures ("federated server"/"administration service" 0.0->1.0).
// Stacking symbol on top of path is then slightly NDCG-negative (it adds recall:
// 0.921->0.913 NDCG but the symbol votes broaden coverage). So the old invariant
// "symbol >= lexical" is no longer the right one — with path on, lexical already
// contains most of symbol's signal. The gate now asserts, instead, that EACH arm
// beats the no-path lexical baseline and the full production stack clears floors.
// (Symbol retains independent value for non-filename-aligned queries and for
// context scoping, a distribution this 30q filename-heavy gold under-represents.)
//
// Skips cleanly when the corpus root is absent (set MOEDEX_CORPUS_ROOT or place
// repos under ~/TCGitlab), so it never reds CI on a machine without the corpus.
//
// THRESHOLDS sit a margin below the measured baseline. They are a floor that
// catches real regressions, not a tight pin — the set is only 30 queries, so a
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

	// One runner, toggled across the four arm configurations. EnableSymbols and
	// DisablePathArm/SetPathCoverage only rebuild the ranker from the same (ix, ti)
	// — they never mutate the index — so reusing it keeps the four runs comparable
	// and avoids re-ingesting the corpus three extra times.
	run := NewRunner(ix)

	// lexical + path (path is on by default).
	repPath, err := run.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "GATE / lexical+path", repPath)

	// lexical only (no path, no symbol) — the historical baseline every arm must beat.
	run.DisablePathArm()
	repLexOnly, err := run.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "GATE / lexical only", repLexOnly)

	// lexical + symbol, path still off — the symbol arm's standalone contribution.
	symCount := run.EnableSymbols()
	repSymNoPath, err := run.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "GATE / lexical+symbol", repSymNoPath)

	// Full production stack: lexical + path + symbol (path back on at its default).
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

	// All four gold languages (C#/TS/ColdFusion/SQL) carry symbols, so the symbol
	// arm must actually attach to some blobs.
	if symCount == 0 {
		t.Error("symbol arm attached to 0 blobs; the C#/TS/CF/SQL symbol lane is dead")
	}

	// --- Hard floors (a margin below the 2026-06-24 measured baseline) ----------
	const (
		// Historical lexical-only baseline (0.640 / 0.702 / 0.746).
		minLexNDCG   = 0.58
		minLexRecall = 0.64
		minLexMRR    = 0.68
		// Full production stack: lexical+path+symbol (0.855 / 0.913 / 0.853).
		minFullNDCG   = 0.78
		minFullRecall = 0.84
		minFullMRR    = 0.78
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

	// Each arm must beat the no-path lexical baseline (its reason to exist), and the
	// full stack must beat plain lexical. We do NOT assert symbol >= path: the path
	// arm subsumes symbol on this filename-aligned gold (see the four-arm note
	// above), so that comparison is expected to be ~flat/slightly-negative on NDCG.
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
