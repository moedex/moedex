package eval

import (
	"context"
	"testing"
)

func TestCorpusAgentNLGap(t *testing.T) {
	ix, _, _, ok := BuildGoldCorpusIndex()
	if !ok {
		t.Skip("gold corpus absent (set MOEDEX_CORPUS_ROOT/MOEDEX_CORPUS or populate ~/.moedex-managed)")
	}
	const k, topK = 5, 20
	ctx := context.Background()
	gold := corpusGoldAgentNL()

	run := NewRunner(ix) // lexical + path (path on by default)
	repPath, err := run.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	run.DisablePathArm()
	repLex, err := run.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	run.SetPathCoverage(0.6)
	run.EnableSymbols()
	repFull, err := run.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "AGENT-NL stratum / lexical only", repLex)
	logReport(t, "AGENT-NL stratum / lexical+path+symbol (no dense)", repFull)
	t.Logf("AGENT-NL no-dense MeanNDCG: lexical=%.4f +path=%.4f +path+symbol=%.4f",
		repLex.MeanNDCG, repPath.MeanNDCG, repFull.MeanNDCG)

	maxNoDenseNDCG := corpusThreshold("maxNoDenseNDCG")
	if repFull.MeanNDCG > maxNoDenseNDCG {
		t.Errorf("agent-NL no-dense MeanNDCG = %.4f > %.4f: the stratum is no longer a synonym gap "+
			"(lexical/path/symbol now answer it) — re-derive the queries or revisit the threshold",
			repFull.MeanNDCG, maxNoDenseNDCG)
	}
}
