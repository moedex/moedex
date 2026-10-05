package eval

import (
	"context"
	"testing"

	"moedex/internal/rank"
)

func TestCorpusLearnedRerankerAB(t *testing.T) {
	ix, n, perRepo, ok := BuildGoldCorpusIndex()
	if !ok {
		t.Skip("gold corpus absent (set MOEDEX_CORPUS_ROOT/MOEDEX_CORPUS or populate ~/.moedex-managed)")
	}
	gold := CorpusGold()
	t.Logf("pooled index: %d files; per-repo kept = %v; %d gold queries", n, perRepo, len(gold))

	const k, topK = 5, 20
	ctx := context.Background()

	run := NewRunner(ix)
	symCount := run.EnableSymbols()
	t.Logf("symbol arm: %d blobs carry symbols", symCount)

	repRRF, err := run.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "AB / RRF (production lexical+path+symbol)", repRRF)

	rows, err := extractGoldRows(ctx, run, gold, topK)
	if err != nil {
		t.Fatal(err)
	}
	var pos int
	for _, r := range rows {
		if r.label == 1 {
			pos++
		}
	}
	t.Logf("extracted %d candidate rows (%d positive) over %d queries", len(rows), pos, len(gold))
	if pos == 0 {
		t.Fatal("no positive training rows; labeling never matched a gold RelPath")
	}

	if _, loss := rank.Train(toTrainExamples(rows, run.RRFk()), rank.TrainConfig{}); len(loss) > 0 {
		t.Logf("full-data train loss: first=%.4f last=%.4f", loss[0], loss[len(loss)-1])
	}

	const folds = 5
	models := crossValModels(rows, run.RRFk(), folds, rank.TrainConfig{})
	run.SetFusion(rank.FusionLinear)
	repLearned, err := evaluateCrossVal(ctx, run, gold, models, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "AB / LEARNED linear reranker (out-of-fold, 5-fold CV)", repLearned)

	dNDCG := repLearned.MeanNDCG - repRRF.MeanNDCG
	dUDCG := repLearned.MeanUDCG - repRRF.MeanUDCG
	dRecall := repLearned.MeanRecall - repRRF.MeanRecall
	dMRR := repLearned.MeanMRR - repRRF.MeanMRR
	t.Logf("A/B DELTAS (learned - RRF):  NDCG %+.4f  UDCG %+.4f  Recall %+.4f  MRR %+.4f", dNDCG, dUDCG, dRecall, dMRR)
	t.Logf("  RRF:     NDCG=%.4f UDCG=%.4f Recall=%.4f MRR=%.4f", repRRF.MeanNDCG, repRRF.MeanUDCG, repRRF.MeanRecall, repRRF.MeanMRR)
	t.Logf("  LEARNED: NDCG=%.4f UDCG=%.4f Recall=%.4f MRR=%.4f", repLearned.MeanNDCG, repLearned.MeanUDCG, repLearned.MeanRecall, repLearned.MeanMRR)

	if len(repLearned.Queries) != len(gold) {
		t.Errorf("learned A/B evaluated %d queries, want %d", len(repLearned.Queries), len(gold))
	}
	if repLearned.MeanNDCG < corpusThreshold("minLearnedNDCG") {
		t.Errorf("learned NDCG = %.4f collapsed well below RRF (%.4f) and the lexical floor — "+
			"the reranker is mis-wired, not merely worse", repLearned.MeanNDCG, repRRF.MeanNDCG)
	}
	_ = dRecall

	winBar := corpusThreshold("winBar")
	switch {
	case dNDCG > winBar:
		t.Logf("VERDICT: learned reranker BEATS RRF by %+.4f NDCG (> win bar %.4f) — a CANDIDATE to flip the default; corroborate on a larger/independent gold before shipping.", dNDCG, winBar)
	case dNDCG >= -winBar:
		t.Logf("VERDICT: learned reranker TIES RRF within judge noise (delta %+.4f NDCG, bar %.4f). RRF stays default.", dNDCG, winBar)
	default:
		t.Logf("VERDICT: learned reranker LOSES to RRF (delta %+.4f NDCG, beyond %.4f). RRF stays default — the honest, expected outcome: tuned RRF + the engineered arms already order this 82-query set better than a linear model trained on only ~%d positive rows.", dNDCG, winBar, pos)
	}
}
