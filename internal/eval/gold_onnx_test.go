//go:build onnx

package eval

import (
	"context"
	"os"
	"strconv"
	"testing"

	"moedex/internal/embed"
)

// TestCorpusONNXMeasurement is the real-embedder measurement using moedex's OWN
// in-process all-MiniLM-L6-v2 embedder (embed.NewONNXEmbedder) as the dense arm,
// over the pooled multi-language gold corpus (C#/TS/SQL). Unlike the HTTP path it
// needs no external service — only the ONNX Runtime shared library. It reports
// lexical vs +dense vs +symbol vs full-hybrid so we can finally see what the
// shipped embedder does on real code.
//
// Skips cleanly when the corpus root is absent or the ONNX Runtime can't load
// (set ONNXRUNTIME_LIB_PATH), so it never reds CI. Run it with:
//
//	ONNXRUNTIME_LIB_PATH=/path/to/libonnxruntime.dylib \
//	go test -tags onnx ./internal/eval/ -run TestCorpusONNXMeasurement -v
//
// Honesty note: absolute numbers from a SINGLE-JUDGE pooled gold set — a baseline
// to watch, not a published quality claim.
func TestCorpusONNXMeasurement(t *testing.T) {
	ix, n, perRepo, ok := BuildGoldCorpusIndex()
	if !ok {
		t.Skip("gold corpus absent (set MOEDEX_CORPUS_ROOT or place repos under ~/TCGitlab)")
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

	// Lexical baseline on the same gold set.
	lexical := NewRunner(ix)
	repLex, err := lexical.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "pooled corpus / lexical", repLex)

	// + real (onnx) dense arm. Fresh index per runner so the corpora are independent.
	ixDense, _, _, _ := BuildGoldCorpusIndex()
	dense := NewRunner(ixDense)
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

	// Full hybrid: dense + symbol arm together.
	ixFull, _, _, _ := BuildGoldCorpusIndex()
	full := NewRunner(ixFull)
	if _, err := full.EnableDense(ctx, emb, linesPerChunk, overlap); err != nil {
		t.Skipf("onnx dense build failed for hybrid (%v)", err)
	}
	symCount := full.EnableSymbols()
	repFull, err := full.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "pooled corpus / full hybrid (lexical + onnx dense + symbol)", repFull)

	t.Logf("REAL onnx embedder: %d chunks, dim=%d, %d symbol blobs", nChunks, emb.Dim(), symCount)
	t.Logf("MeanNDCG    lexical=%.4f  +dense=%.4f  full-hybrid=%.4f",
		repLex.MeanNDCG, repDense.MeanNDCG, repFull.MeanNDCG)
	t.Logf("MeanMRR     lexical=%.4f  +dense=%.4f  full-hybrid=%.4f",
		repLex.MeanMRR, repDense.MeanMRR, repFull.MeanMRR)
	t.Logf("MeanRecall@%d lexical=%.4f  +dense=%.4f  full-hybrid=%.4f",
		k, repLex.MeanRecall, repDense.MeanRecall, repFull.MeanRecall)
	// No hard assertion on the dense delta: this is the number we are OBSERVING.
}

// TestCorpusCodeModelMeasurement A/Bs a CODE-TRAINED embedder against the bundled
// general-text all-MiniLM, on the same pooled gold corpus. The general-text model
// was ~neutral on code (see TestCorpusONNXMeasurement); this asks whether a model
// trained on code search (e.g. st-codesearch-distilroberta-base) does better.
//
// The code model is loaded from disk (not bundled) so we measure before deciding
// whether to vendor it. Point it at an exported encoder:
//
//	MOEDEX_CODE_MODEL=/path/model.onnx \
//	MOEDEX_CODE_TOKENIZER=/path/tokenizer.json \
//	MOEDEX_CODE_DIM=768 \
//	ONNXRUNTIME_LIB_PATH=/path/libonnxruntime.dylib \
//	go test -tags onnx ./internal/eval/ -run TestCorpusCodeModelMeasurement -v
//
// Skips cleanly when the model files, corpus, or runtime are absent.
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
		t.Skip("gold corpus absent (set MOEDEX_CORPUS_ROOT or place repos under ~/TCGitlab)")
	}
	// RoBERTa-style code models have no token_type_ids.
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
