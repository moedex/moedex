package eval

import (
	"context"
	"os"
	"testing"

	"moedex/internal/embed"
)

func TestCorpusMeasurement(t *testing.T) {
	ix, n, perRepo, ok := BuildGoldCorpusIndex()
	if !ok {
		t.Skip("gold corpus absent (set MOEDEX_CORPUS_ROOT/MOEDEX_CORPUS or populate ~/.moedex-managed)")
	}
	t.Logf("pooled index: %d files; per-repo kept = %v", n, perRepo)

	for repo, c := range perRepo {
		if c == 0 {
			t.Errorf("repo %q contributed 0 files — its gold labels are unreachable", repo)
		}
	}

	const k, topK = 5, 20
	ctx := context.Background()
	gold := CorpusGold()

	run := NewRunner(ix)

	run.DisablePathArm()
	repLexOnly, err := run.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "pooled corpus (C#/TS/SQL/CF + non-aligned) / lexical only", repLexOnly)

	run.SetPathCoverage(0.6)
	symCount := run.EnableSymbols()
	repFull, err := run.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "pooled corpus / lexical+path+symbol (PRODUCTION)", repFull)
	t.Logf("symbol arm: %d blobs carry symbols (C# + TS + ColdFusion + SQL)", symCount)

	t.Logf("MeanMRR  lexical=%.4f  PRODUCTION=%.4f", repLexOnly.MeanMRR, repFull.MeanMRR)
	t.Logf("MeanNDCG lexical=%.4f  PRODUCTION=%.4f", repLexOnly.MeanNDCG, repFull.MeanNDCG)
	t.Logf("MeanRecall@%d lexical=%.4f  PRODUCTION=%.4f", k, repLexOnly.MeanRecall, repFull.MeanRecall)

	if repFull.MeanNDCG < repLexOnly.MeanNDCG-1e-9 {
		t.Errorf("production stack regressed MeanNDCG below plain lexical: full=%.4f lexical=%.4f",
			repFull.MeanNDCG, repLexOnly.MeanNDCG)
	}

	if repLexOnly.MeanMRR < corpusThreshold("minObservedMRR") {
		t.Errorf("pooled-corpus lexical MeanMRR = %.3f, below configured floor", repLexOnly.MeanMRR)
	}
}

func TestCorpusRealEmbedderMeasurement(t *testing.T) {
	url := os.Getenv("MOEDEX_EMBED_URL")
	model := os.Getenv("MOEDEX_EMBED_MODEL")
	if url == "" || model == "" {
		t.Skip("real embedder not configured (set MOEDEX_EMBED_URL and MOEDEX_EMBED_MODEL)")
	}

	ix, n, perRepo, ok := BuildGoldCorpusIndex()
	if !ok {
		t.Skip("gold corpus absent (set MOEDEX_CORPUS_ROOT/MOEDEX_CORPUS or populate ~/.moedex-managed)")
	}
	t.Logf("pooled index: %d files; per-repo kept = %v", n, perRepo)

	const k, topK = 5, 20
	const linesPerChunk, overlap = 40, 8
	ctx := context.Background()
	gold := CorpusGold()

	emb := embed.NewHTTPEmbedder(url, model)

	lexical := NewRunner(ix)
	repLex, err := lexical.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "pooled corpus / lexical (real-embedder run)", repLex)

	ixDense, _, _, _ := BuildGoldCorpusIndex()
	dense := NewRunner(ixDense)
	nChunks, err := dense.EnableDense(ctx, emb, linesPerChunk, overlap)
	if err != nil {
		t.Skipf("real embedder unreachable/failed (%v); skipping real-embedder measurement", err)
	}
	if nChunks == 0 {
		t.Skip("real embedder produced 0 chunks; nothing to measure")
	}
	repDense, err := dense.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "pooled corpus / lexical + REAL dense", repDense)
	t.Logf("REAL embedder: %d chunks, dim=%d", nChunks, emb.Dim())
	t.Logf("MeanNDCG  lexical=%.4f  +real-dense=%.4f  (delta %+.4f)",
		repLex.MeanNDCG, repDense.MeanNDCG, repDense.MeanNDCG-repLex.MeanNDCG)
	t.Logf("MeanRecall@%d lexical=%.4f  +real-dense=%.4f", k, repLex.MeanRecall, repDense.MeanRecall)
}
