//go:build onnx

package eval

import (
	"context"
	"os"
	"strconv"
	"testing"

	"moedex/internal/embed"
)

func TestCorpusONNXMeasurement(t *testing.T) {
	ix, n, perRepo, ok := BuildGoldCorpusIndex()
	if !ok {
		t.Skip("gold corpus absent (set MOEDEX_CORPUS_ROOT/MOEDEX_CORPUS or populate ~/.moedex-managed)")
	}
	emb, err := embed.NewONNXEmbedder(os.Getenv("ONNXRUNTIME_LIB_PATH"))
	if err != nil {
		t.Skipf("onnx runtime unavailable (set ONNXRUNTIME_LIB_PATH): %v", err)
	}
	defer emb.Close()
	t.Logf("pooled index: %d files; per-repo kept = %v; embedder dim=%d", n, perRepo, emb.Dim())

	const k, topK = 5, 20
	const linesPerChunk, overlap = 40, 8
	ctx := context.Background()
	gold := CorpusGold()

	lexical := NewRunner(ix)
	repLex, err := lexical.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "pooled corpus / lexical", repLex)

	ixProd, _, _, _ := BuildGoldCorpusIndex()
	prod := NewRunner(ixProd)
	prod.EnableSymbols() // path arm is on by default; this adds the symbol arm.
	repProd, err := prod.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "pooled corpus / PRODUCTION (lexical + path + symbol, NO dense)", repProd)

	ixDense, _, _, _ := BuildGoldCorpusIndex()
	dense := NewRunner(ixDense)
	dense.SetDenseMinQueryTerms(-1) // measure RAW (ungated) dense — this test documents
	nChunks, err := dense.EnableDense(ctx, emb, linesPerChunk, overlap)
	if err != nil {
		t.Skipf("onnx dense build failed (%v); skipping", err)
	}
	if nChunks == 0 {
		t.Skip("onnx embedder produced 0 chunks; nothing to measure")
	}
	repDense, err := dense.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "pooled corpus / lexical + REAL onnx dense", repDense)

	ixFull, _, _, _ := BuildGoldCorpusIndex()
	full := NewRunner(ixFull)
	full.SetDenseMinQueryTerms(-1)
	if _, err := full.EnableDense(ctx, emb, linesPerChunk, overlap); err != nil {
		t.Skipf("onnx dense build failed for hybrid (%v)", err)
	}
	symCount := full.EnableSymbols()
	repFull, err := full.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "pooled corpus / full hybrid (lexical + path + onnx dense + symbol)", repFull)

	t.Logf("REAL onnx embedder: %d chunks, dim=%d, %d symbol blobs", nChunks, emb.Dim(), symCount)
	t.Logf("MeanNDCG    lexical=%.4f  production=%.4f  +dense=%.4f  full-hybrid=%.4f",
		repLex.MeanNDCG, repProd.MeanNDCG, repDense.MeanNDCG, repFull.MeanNDCG)
	t.Logf("MeanMRR     lexical=%.4f  production=%.4f  +dense=%.4f  full-hybrid=%.4f",
		repLex.MeanMRR, repProd.MeanMRR, repDense.MeanMRR, repFull.MeanMRR)
	t.Logf("MeanRecall@%d lexical=%.4f  production=%.4f  +dense=%.4f  full-hybrid=%.4f",
		k, repLex.MeanRecall, repProd.MeanRecall, repDense.MeanRecall, repFull.MeanRecall)

	if repFull.MeanNDCG < repProd.MeanNDCG-corpusThreshold("denseSlack") {
		t.Errorf("dense arm regressed production NDCG: production(no-dense)=%.4f full(+dense)=%.4f (slack %.4f)",
			repProd.MeanNDCG, repFull.MeanNDCG, corpusThreshold("denseSlack"))
	}

	if repFull.MeanNDCG < corpusThreshold("minDenseNDCG") {
		t.Errorf("full hybrid (+dense) MeanNDCG = %.4f, below floor %.4f (regression)", repFull.MeanNDCG, corpusThreshold("minDenseNDCG"))
	}

	nlGold := corpusGoldAgentNL()
	nlLex, err := lexical.Evaluate(ctx, nlGold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	nlProd, err := prod.Evaluate(ctx, nlGold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	nlDense, err := dense.Evaluate(ctx, nlGold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	nlFull, err := full.Evaluate(ctx, nlGold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "AGENT-NL / lexical+path (no dense)", nlLex)
	logReport(t, "AGENT-NL / lexical+path+dense", nlDense)
	t.Logf("AGENT-NL MeanNDCG    lexical+path=%.4f  +path+symbol=%.4f  +dense=%.4f  full(+dense+symbol)=%.4f",
		nlLex.MeanNDCG, nlProd.MeanNDCG, nlDense.MeanNDCG, nlFull.MeanNDCG)
	t.Logf("AGENT-NL MeanRecall@%d lexical+path=%.4f  +dense=%.4f  full=%.4f",
		k, nlLex.MeanRecall, nlDense.MeanRecall, nlFull.MeanRecall)
	t.Logf("AGENT-NL dense PAYOFF over no-dense production: %+.4f NDCG, %+.4f Recall@%d",
		nlDense.MeanNDCG-nlProd.MeanNDCG, nlDense.MeanRecall-nlProd.MeanRecall, k)

	if nlDense.MeanNDCG < nlProd.MeanNDCG-corpusThreshold("denseSlack") {
		t.Errorf("dense arm HURTS its own synonym-gap home turf: agent-NL no-dense=%.4f +dense=%.4f (slack %.4f)",
			nlProd.MeanNDCG, nlDense.MeanNDCG, corpusThreshold("denseSlack"))
	}
}

func TestCorpusCodeModelMeasurement(t *testing.T) {
	modelPath := os.Getenv("MOEDEX_CODE_MODEL")
	tokPath := os.Getenv("MOEDEX_CODE_TOKENIZER")
	if modelPath == "" || tokPath == "" {
		t.Skip("code model not configured (set MOEDEX_CODE_MODEL and MOEDEX_CODE_TOKENIZER)")
	}
	dim := 768
	if v := os.Getenv("MOEDEX_CODE_DIM"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			dim = n
		}
	}

	ix, n, perRepo, ok := BuildGoldCorpusIndex()
	if !ok {
		t.Skip("gold corpus absent (set MOEDEX_CORPUS_ROOT/MOEDEX_CORPUS or populate ~/.moedex-managed)")
	}
	emb, err := embed.NewONNXEmbedderFromFiles(os.Getenv("ONNXRUNTIME_LIB_PATH"), modelPath, tokPath,
		[]string{"input_ids", "attention_mask"}, dim, 256)
	if err != nil {
		t.Skipf("code model unavailable (%v)", err)
	}
	defer emb.Close()
	t.Logf("pooled index: %d files; per-repo kept = %v; CODE embedder dim=%d", n, perRepo, emb.Dim())

	const k, topK = 5, 20
	const linesPerChunk, overlap = 40, 8
	ctx := context.Background()
	gold := CorpusGold()

	lexical := NewRunner(ix)
	repLex, err := lexical.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "pooled corpus / lexical", repLex)

	ixDense, _, _, _ := BuildGoldCorpusIndex()
	dense := NewRunner(ixDense)
	nChunks, err := dense.EnableDense(ctx, emb, linesPerChunk, overlap)
	if err != nil {
		t.Skipf("code dense build failed (%v)", err)
	}
	if nChunks == 0 {
		t.Skip("code embedder produced 0 chunks")
	}
	repDense, err := dense.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "pooled corpus / lexical + CODE dense", repDense)

	ixFull, _, _, _ := BuildGoldCorpusIndex()
	full := NewRunner(ixFull)
	if _, err := full.EnableDense(ctx, emb, linesPerChunk, overlap); err != nil {
		t.Skipf("code dense build failed for hybrid (%v)", err)
	}
	full.EnableSymbols()
	repFull, err := full.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "pooled corpus / full hybrid (lexical + CODE dense + symbol)", repFull)

	t.Logf("CODE embedder: %d chunks, dim=%d", nChunks, emb.Dim())
	t.Logf("MeanNDCG    lexical=%.4f  +code-dense=%.4f  full-hybrid=%.4f",
		repLex.MeanNDCG, repDense.MeanNDCG, repFull.MeanNDCG)
	t.Logf("MeanMRR     lexical=%.4f  +code-dense=%.4f  full-hybrid=%.4f",
		repLex.MeanMRR, repDense.MeanMRR, repFull.MeanMRR)
	t.Logf("MeanRecall@%d lexical=%.4f  +code-dense=%.4f  full-hybrid=%.4f",
		k, repLex.MeanRecall, repDense.MeanRecall, repFull.MeanRecall)
}
