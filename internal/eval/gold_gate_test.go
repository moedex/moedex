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
// FOUR-ARM REALITY (2026-06-24, balanced 36-query gold). Measured on one index by
// toggling arms:
//
//	lexical only (no path/sym) NDCG=0.656 MRR=0.762 Recall=0.710   <- historical baseline
//	lexical+path (no symbol)   NDCG=0.838                          <- wins the filename-aligned half
//	lexical+symbol (no path)   NDCG=0.839                          <- wins the non-aligned half
//	+path+symbol (PRODUCTION)  NDCG=0.932 MRR=0.958 Recall=0.962   <- the default stack
//
// KEY FINDING: path and symbol are COMPLEMENTARY. Production (0.932) clearly beats
// either arm alone (~0.838), because each covers a stratum the other misses: the
// path arm wins filename-aligned queries (the definer file is named for the concept
// — RefundOrderValidator.cs, mysql_create_federated_server.sql; it also fixed
// "federated server"/"administration service" 0.0->1.0), while the symbol arm wins
// non-filename-aligned queries (the concept lives in a method/UDF name inside a
// generically-named file — SslService.ParseVendorErrorMessages,
// act_functions3.cfm:parseUserAgent; see corpusGoldNonAligned, where symbol lifts
// the stratum 0.52->0.94 and path adds nothing).
//
// HISTORY: an earlier filename-aligned-ONLY 30q gold showed path SUBSUMING symbol
// (production ~= path, ~0.89). That was a sampling artifact; expanding the gold with
// the non-aligned stratum revealed the true complementarity. The symbol gate was
// raised 0.5->0.67 when the path arm landed (see rank.Config.SymbolMinCoverage). The
// gate asserts each arm beats the no-path lexical baseline and the full stack clears
// floors; it does NOT assert symbol >= path (path alone wins the aligned half, and
// vice-versa) — the meaningful invariant is that the FULL stack beats plain lexical.
//
// Skips cleanly when the corpus root is absent (set MOEDEX_CORPUS_ROOT or place
// repos under ~/TCGitlab), so it never reds CI on a machine without the corpus.
//
// THRESHOLDS sit a margin below the measured baseline. They are a floor that
// catches real regressions, not a tight pin — the set is only 36 queries, so a
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

	// --- Hard floors (~0.08 below the 2026-06-24 measured baseline, 36 queries) --
	const (
		// Historical lexical-only baseline (NDCG 0.656 / Recall 0.710 / MRR 0.762).
		minLexNDCG   = 0.58
		minLexRecall = 0.64
		minLexMRR    = 0.68
		// Full production stack lexical+path+symbol (NDCG 0.932 / Recall 0.962 / MRR 0.958).
		minFullNDCG   = 0.85
		minFullRecall = 0.88
		minFullMRR    = 0.88
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
