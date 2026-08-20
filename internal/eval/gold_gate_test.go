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
// FOUR-ARM REALITY (2026-06-24, 82-query gold after the P8 expansion). Measured on
// one index by toggling arms:
//
//	lexical only (no path/sym) NDCG=0.639 MRR=0.692 Recall=0.704   <- baseline
//	lexical+path (no symbol)   NDCG=0.744                          <- wins the filename-aligned half
//	lexical+symbol (no path)   NDCG=0.764                          <- wins the non-aligned half
//	+path+symbol (PRODUCTION)  NDCG=0.809 MRR=0.836 Recall=0.839   <- the default stack (UDCG=0.519)
//
// KEY FINDING: path and symbol are COMPLEMENTARY. Production (0.809) clearly beats
// either arm alone (0.744 / 0.764), because each covers a stratum the other misses:
// the path arm wins filename-aligned queries (the definer file is named for the
// concept — RefundOrderValidator.cs, mysql_create_federated_server.sql; it also
// fixed "federated server"/"administration service" 0.0->1.0), while the symbol arm
// wins non-filename-aligned queries (the concept lives in a method/UDF name inside a
// generically-named file — SslService.ParseVendorErrorMessages,
// act_functions3.cfm:parseUserAgent, the 15-query corpusGoldCFLibrary stratum).
//
// NOTE: the P8 expansion (36 -> 82 queries) added many non-aligned CF-library/C#
// queries, which (a) lowered the absolute scores from the filename-aligned-biased
// 36-query numbers (production 0.932 -> 0.809) and (b) tipped symbol slightly above
// path (0.764 > 0.744), since the non-aligned half — symbol's territory — grew. The
// floors below were recalibrated to the 82-query baseline.
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
		t.Skip("gold corpus absent (set MOEDEX_CORPUS_ROOT/MOEDEX_CORPUS or populate ~/.moedex-managed)")
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
	t.Logf("GATE MeanUDCG   lexOnly=%.4f  +path=%.4f  +symbol=%.4f  PRODUCTION=%.4f",
		repLexOnly.MeanUDCG, repPath.MeanUDCG, repSymNoPath.MeanUDCG, repFull.MeanUDCG)

	// All four gold languages (C#/TS/ColdFusion/SQL) carry symbols, so the symbol
	// arm must actually attach to some blobs.
	if symCount == 0 {
		t.Error("symbol arm attached to 0 blobs; the C#/TS/CF/SQL symbol lane is dead")
	}

	// --- Hard floors (~0.07 below the 2026-06-24 measured baseline, 82 queries) --
	// Recalibrated when the gold set was expanded 36 -> 82 (P8). The absolute
	// scores dropped (NDCG 0.932 -> 0.809) because the new queries deliberately
	// target the HARDER, non-filename-aligned half of the search space (CF-library
	// UDFs and C# methods whose concept lives in a symbol name inside a generically
	// named file), correcting the filename-aligned sampling bias the 36-query set
	// carried. Lower absolute, more representative — a regression watch with a
	// defensible floor, not a published claim.
	const (
		// Lexical-only baseline, 82 queries (NDCG 0.639 / Recall 0.704 / MRR 0.692).
		minLexNDCG   = 0.57
		minLexRecall = 0.63
		minLexMRR    = 0.62
		// Full production stack lexical+path+symbol (NDCG 0.809 / Recall 0.839 / MRR 0.836).
		minFullNDCG   = 0.74
		minFullRecall = 0.76
		minFullMRR    = 0.76
		// UDCG soft watch (production 0.519): LOGGED if breached, never a hard fail
		// (UDCG is only as good as the hand-distractor labels — see metrics.go).
		watchFullUDCG = 0.44
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

	// UDCG is a SOFT gate (regression WATCH, not a hard floor): a breach is logged,
	// not failed, because UDCG depends on the hand-labeled hard distractors, which
	// are sparser than the relevance labels. A drop here means distractor leakage
	// into the top-k context window is rising — worth a look, not a red build.
	if repFull.MeanUDCG < watchFullUDCG {
		t.Logf("WATCH: PRODUCTION MeanUDCG = %.4f below soft floor %.4f — distractor leakage into top-k is up (not a hard failure)", repFull.MeanUDCG, watchFullUDCG)
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
