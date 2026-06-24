package eval

import (
	"context"
	"testing"
)

// TestCorpusAgentNLGap pins the PREMISE of the synonym-gap / agent-style stratum
// (corpusGoldAgentNL): its queries are phrased so their terms match NEITHER the
// definer's filename NOR its symbol name, so the no-dense arms (lexical, path,
// symbol) should be UNABLE to answer them well. This is an UPPER-bound guard — the
// mirror of TestCorpusGoldGate's lower bounds. If a future change makes these
// lexically answerable, the stratum has lost its reason to exist (it no longer
// isolates the region only a dense/semantic arm can win), and this test fails so we
// re-derive it deliberately rather than silently. The dense arm's EFFECT on this
// stratum is measured under -tags onnx (TestCorpusONNXMeasurement's agent-NL split).
//
// Skips cleanly when the corpus root is absent.
func TestCorpusAgentNLGap(t *testing.T) {
	ix, _, _, ok := BuildGoldCorpusIndex()
	if !ok {
		t.Skip("gold corpus absent (set MOEDEX_CORPUS_ROOT or place repos under ~/TCGitlab)")
	}
	const k, topK = 5, 20
	ctx := context.Background()
	gold := corpusGoldAgentNL()

	// One runner toggled across no-dense arm configs (rebuilds never mutate the index).
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

	// Upper-bound guard: the no-dense stack must REMAIN weak here (these queries are a
	// deliberate synonym gap). 0.45 sits well above the measured ~0.14 but far below an
	// "answered" set; crossing it means the stratum is no longer dense-only.
	const maxNoDenseNDCG = 0.45
	if repFull.MeanNDCG > maxNoDenseNDCG {
		t.Errorf("agent-NL no-dense MeanNDCG = %.4f > %.4f: the stratum is no longer a synonym gap "+
			"(lexical/path/symbol now answer it) — re-derive the queries or revisit the threshold",
			repFull.MeanNDCG, maxNoDenseNDCG)
	}
}
