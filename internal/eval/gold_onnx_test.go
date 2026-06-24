//go:build onnx

package eval

import (
	"context"
	"os"
	"strconv"
	"testing"

	"moedex/internal/embed"
)

// denseSlack is the allowed downward wobble when comparing the +dense full stack
// against the no-dense production stack (lexical+path+symbol). The dense arm is
// the noisiest signal in the hybrid: a single general-text all-MiniLM embedder
// over chunked code, fused by RRF — its per-query contribution swings a few
// hundredths on this 36-query single-judge gold (see the four-arm note in
// gold_gate_test.go). We therefore guard "must not regress MEANINGFULLY" rather
// than exact non-inferiority: dense may dip a hair below production-without-dense
// and still be acceptable, but a dense arm that craters the production stack
// (e.g. a chunking/cosine/fusion bug pulling irrelevant chunks to the top) blows
// past this slack and reds the test. ~0.04 ≈ half the ~0.08 margin the gate uses
// below its baselines; it is a regression tripwire, not a quality target.
const denseSlack = 0.04

// minDenseNDCG is the absolute floor the +dense full stack must clear. The
// no-dense production stack (lexical+path+symbol) measures NDCG ≈ 0.932 on the
// 36-query / 343-file pooled gold (gold_gate_test.go). We set the floor well
// below that — at the same conservative level the gate uses for its production
// floor — so it catches a genuinely broken dense arm without pinning to a noisy
// absolute. It is a regression tripwire, NOT a published quality claim.
const minDenseNDCG = 0.85

// TestCorpusONNXMeasurement gates the real-embedder dense arm using moedex's OWN
// in-process all-MiniLM-L6-v2 embedder (embed.NewONNXEmbedder), over the pooled
// multi-language gold corpus (C#/TS/SQL). Unlike the HTTP path it needs no
// external service — only the ONNX Runtime shared library. It still LOGS lexical
// vs +dense vs full-hybrid (the observability is valuable), but it now also
// ASSERTS that the +dense full stack neither regresses the no-dense production
// stack (lexical+path+symbol) beyond denseSlack nor falls below the absolute
// floor minDenseNDCG. The dense arm is thus promoted from measured-only to gated.
//
// Skips cleanly when the corpus root is absent or the ONNX Runtime can't load
// (set ONNXRUNTIME_LIB_PATH), so it never reds CI. Run it with:
//
//	ONNXRUNTIME_LIB_PATH=/path/to/libonnxruntime.dylib \
//	go test -tags onnx ./internal/eval/ -run TestCorpusONNXMeasurement -v
//
// Honesty note: absolute numbers from a SINGLE-JUDGE pooled gold set — a baseline
// to watch, not a published quality claim. The assertions are floors/tripwires,
// not a statement that dense improves ranking (on this gold it is ~neutral).
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

	// No-dense PRODUCTION baseline: lexical + path (on by default) + symbol. This
	// is the stack the +dense run must not regress past denseSlack. Fresh index so
	// it is independent of the lexical runner above.
	ixProd, _, _, _ := BuildGoldCorpusIndex()
	prod := NewRunner(ixProd)
	prod.EnableSymbols() // path arm is on by default; this adds the symbol arm.
	repProd, err := prod.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "pooled corpus / PRODUCTION (lexical + path + symbol, NO dense)", repProd)

	// + real (onnx) dense arm. Fresh index per runner so the corpora are independent.
	ixDense, _, _, _ := BuildGoldCorpusIndex()
	dense := NewRunner(ixDense)
	dense.SetDenseMinQueryTerms(-1) // measure RAW (ungated) dense — this test documents
	// the raw net-negative-on-answerable / net-positive-on-agent-NL behavior that
	// JUSTIFIES the production query-length gate (the gate is pinned additive by
	// TestCorpusDenseGateSweep).
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

	// Full hybrid: lexical + path (default) + dense + symbol — the production stack
	// WITH the dense arm. This is what the gate asserts on. RAW (ungated) dense, as above.
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

	// --- HARD GATE: dense promoted from measured-only to asserted. ---

	// 1) The +dense full stack must not regress the no-dense production stack
	//    beyond denseSlack. A dense arm that meaningfully drags the production
	//    ranking down (chunking/cosine/RRF-fusion bug) reds this.
	if repFull.MeanNDCG < repProd.MeanNDCG-denseSlack {
		t.Errorf("dense arm regressed production NDCG: production(no-dense)=%.4f full(+dense)=%.4f (slack %.4f)",
			repProd.MeanNDCG, repFull.MeanNDCG, denseSlack)
	}

	// 2) The +dense full stack must clear the absolute regression floor.
	if repFull.MeanNDCG < minDenseNDCG {
		t.Errorf("full hybrid (+dense) MeanNDCG = %.4f, below floor %.4f (regression)", repFull.MeanNDCG, minDenseNDCG)
	}

	// --- AGENT-NL SPLIT: the synonym-gap stratum where ONLY a semantic match can win
	// (corpusGoldAgentNL; the no-dense arms score ~0 here, pinned by
	// TestCorpusAgentNLGap). This is the home turf where the dense arm is supposed to
	// earn its keep, so it is the most honest single read on whether dense is worth
	// keeping. We LOG the deltas (discovery, not a gate) and only fail if the dense arm
	// makes this region WORSE than no-dense by more than denseSlack — dense should at
	// minimum not hurt where it is meant to help.
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

	if nlDense.MeanNDCG < nlProd.MeanNDCG-denseSlack {
		t.Errorf("dense arm HURTS its own synonym-gap home turf: agent-NL no-dense=%.4f +dense=%.4f (slack %.4f)",
			nlProd.MeanNDCG, nlDense.MeanNDCG, denseSlack)
	}
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
