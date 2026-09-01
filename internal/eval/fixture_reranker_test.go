package eval

import (
	"context"
	"testing"

	"moedex/internal/rank"
)

// TestFixtureLearnedRerankerPlumbing is the hermetic end-to-end A/B for the learned
// reranker: it proves the whole pipe works without the corpus —
//  1. extract labeled feature rows from the fixture gold through the Runner,
//  2. train a LinearReranker (assert the SGD loss actually decreases: real learning),
//  3. evaluate RRF vs the learned fusion OUT-OF-FOLD through Runner.SetFusion,
//  4. assert the learned mode is plumbed (produces a Report; recall preserved).
//
// This fixture is too small to make a meaningful quality CLAIM (the corpus A/B does
// that); the assertions here are about CORRECTNESS of the machinery, not a win.
func TestFixtureLearnedRerankerPlumbing(t *testing.T) {
	const k, topK = 5, 10
	ctx := context.Background()
	files := FixtureFiles()
	gold := FixtureGold()

	// Production-shaped runner: path arm on (default), symbol arm enabled. (No dense
	// here — that needs an embedder; the corpus A/B covers default-build arms.)
	run := NewRunner(BuildIndexFromFiles(files))
	run.EnableSymbols()

	// --- Baseline: RRF ---
	repRRF, err := run.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "fixture / RRF baseline", repRRF)

	// --- Extract labeled rows + train (assert real learning) ---
	rows, err := extractGoldRows(ctx, run, gold, topK)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no training rows extracted; feature extraction is broken")
	}
	var pos int
	for _, r := range rows {
		if r.label == 1 {
			pos++
		}
	}
	t.Logf("extracted %d candidate rows (%d positive) across %d queries", len(rows), pos, len(gold))
	if pos == 0 {
		t.Fatal("no positive rows; labeling never matched a gold RelPath (the arms surface gold files)")
	}

	ex := toTrainExamples(rows, run.RRFk())
	_, loss := rank.Train(ex, rank.TrainConfig{Epochs: 120})
	if len(loss) == 0 {
		t.Fatal("trainer recorded no loss")
	}
	if !(loss[len(loss)-1] < loss[0]) {
		t.Errorf("SGD did not reduce loss on the fixture: first=%.4f last=%.4f", loss[0], loss[len(loss)-1])
	}
	t.Logf("train loss: first=%.4f last=%.4f (delta %+.4f)", loss[0], loss[len(loss)-1], loss[len(loss)-1]-loss[0])

	// --- Out-of-fold learned A/B through the Runner ---
	// Small fixture => few queries => use 4-fold (GroupKFold clamps to #queries).
	models := crossValModels(rows, run.RRFk(), 4, rank.TrainConfig{Epochs: 120})
	if len(models) == 0 {
		t.Fatal("cross-validation produced no models")
	}
	run.SetFusion(rank.FusionLinear)
	repLearned, err := evaluateCrossVal(ctx, run, gold, models, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "fixture / LEARNED (out-of-fold)", repLearned)

	// Plumbing assertions (NOT a win claim):
	if len(repLearned.Queries) != len(gold) {
		t.Errorf("learned A/B evaluated %d queries, want %d", len(repLearned.Queries), len(gold))
	}
	// The learned mode must produce SANE scores end-to-end (not collapse the order):
	// on this fixture, where the arms already surface the gold, the learned NDCG must
	// stay in a healthy range. (recall@k is a cutoff metric, so reordering can move it
	// legitimately — the class-level no-drop guarantee is pinned in
	// internal/rank.TestRecallPreserved, not here.)
	if repLearned.MeanNDCG < 0.5 {
		t.Errorf("learned NDCG collapsed to %.4f; the learned fusion is mis-wired", repLearned.MeanNDCG)
	}
	t.Logf("MeanNDCG  RRF=%.4f  learned=%.4f (delta %+.4f)",
		repRRF.MeanNDCG, repLearned.MeanNDCG, repLearned.MeanNDCG-repRRF.MeanNDCG)
	t.Logf("MeanRecall RRF=%.4f  learned=%.4f", repRRF.MeanRecall, repLearned.MeanRecall)
	t.Logf("MeanUDCG  RRF=%.4f  learned=%.4f", repRRF.MeanUDCG, repLearned.MeanUDCG)

	// Switching back to RRF must restore the baseline exactly (mode is a clean toggle).
	run.SetFusion(rank.FusionRRF)
	repBack, err := run.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	if repBack.MeanNDCG != repRRF.MeanNDCG {
		t.Errorf("toggling fusion back to RRF did not restore the baseline NDCG: %.6f vs %.6f",
			repBack.MeanNDCG, repRRF.MeanNDCG)
	}
}
