package eval

import (
	"context"
	"os"
	"testing"

	"moedex/internal/embed"
)

// TestCorpusMeasurement runs the pooled, multi-language gold set (CorpusGold)
// against a real index built from three ~/TCGitlab repos (C# / TS / SQL). It
// reports lexical vs lexical+symbol numbers. It SKIPS cleanly when the corpus
// root is absent, mirroring TestTCSslApiMeasurement.
//
// Honesty note: these are absolute numbers from a SINGLE-JUDGE pooled gold set.
// They are a baseline to watch for regressions, not a published quality claim.
func TestCorpusMeasurement(t *testing.T) {
	ix, n, perRepo, ok := BuildGoldCorpusIndex()
	if !ok {
		t.Skip("gold corpus absent (set MOEDEX_CORPUS_ROOT or place repos under ~/TCGitlab)")
	}
	t.Logf("pooled index: %d files; per-repo kept = %v", n, perRepo)

	// Every repo's sample must be non-empty, or the gold labels for that language
	// can never be found and the metric is silently meaningless.
	for repo, c := range perRepo {
		if c == 0 {
			t.Errorf("repo %q contributed 0 files — its gold labels are unreachable", repo)
		}
	}

	const k, topK = 5, 20
	ctx := context.Background()
	gold := CorpusGold()

	lexical := NewRunner(ix)
	repLex, err := lexical.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "pooled corpus (C#/TS/SQL) / lexical", repLex)

	// Symbol arm (a fresh runner over the same index files would re-ingest; instead
	// build a second index from the same repos to keep runners independent).
	ixSym, _, _, _ := BuildGoldCorpusIndex()
	withSym := NewRunner(ixSym)
	symCount := withSym.EnableSymbols()
	repSym, err := withSym.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "pooled corpus / lexical + symbol", repSym)
	t.Logf("symbol arm: %d blobs carry symbols (C# + TS; SQL has no extractor)", symCount)

	t.Logf("MeanMRR  lexical=%.4f  +symbol=%.4f", repLex.MeanMRR, repSym.MeanMRR)
	t.Logf("MeanNDCG lexical=%.4f  +symbol=%.4f", repLex.MeanNDCG, repSym.MeanNDCG)
	t.Logf("MeanRecall@%d lexical=%.4f  +symbol=%.4f", k, repLex.MeanRecall, repSym.MeanRecall)

	// The symbol arm must NOT regress aggregate ranking quality on this pooled set
	// (same guard as the fixture test — pins the coverage-gate fix in place).
	if repSym.MeanNDCG < repLex.MeanNDCG-1e-9 {
		t.Errorf("symbol arm regressed MeanNDCG on pooled corpus: with=%.4f without=%.4f",
			repSym.MeanNDCG, repLex.MeanNDCG)
	}

	// Observational floor on MRR (the honest signal for sparse single-judge labels,
	// per the TCSslApiGold reasoning): the first relevant hit should usually appear.
	if repLex.MeanMRR < 0.25 {
		t.Errorf("pooled-corpus lexical MeanMRR = %.3f, want >= 0.25", repLex.MeanMRR)
	}
}

// TestCorpusRealEmbedderMeasurement runs the pooled gold set with the REAL HTTP
// embedder (embed.NewHTTPEmbedder) as the dense arm, against MOEDEX_EMBED_URL /
// MOEDEX_EMBED_MODEL. This is the ONE path that produces a real-embedder number
// instead of only the fake. It does NOT hard-depend on the service: it skips
// cleanly when either env var is unset OR the corpus is absent, so CI stays green.
//
// To run it locally point it at a local embeddings server, e.g.:
//
//	MOEDEX_EMBED_URL=http://localhost:11434/v1 \
//	MOEDEX_EMBED_MODEL=nomic-embed-text \
//	go test ./internal/eval/ -run TestCorpusRealEmbedderMeasurement -v
func TestCorpusRealEmbedderMeasurement(t *testing.T) {
	url := os.Getenv("MOEDEX_EMBED_URL")
	model := os.Getenv("MOEDEX_EMBED_MODEL")
	if url == "" || model == "" {
		t.Skip("real embedder not configured (set MOEDEX_EMBED_URL and MOEDEX_EMBED_MODEL)")
	}

	ix, n, perRepo, ok := BuildGoldCorpusIndex()
	if !ok {
		t.Skip("gold corpus absent (set MOEDEX_CORPUS_ROOT or place repos under ~/TCGitlab)")
	}
	t.Logf("pooled index: %d files; per-repo kept = %v", n, perRepo)

	const k, topK = 5, 20
	const linesPerChunk, overlap = 40, 8
	ctx := context.Background()
	gold := CorpusGold()

	emb := embed.NewHTTPEmbedder(url, model)

	// Lexical baseline on the same index, so the real-embedder number is comparable.
	lexical := NewRunner(ix)
	repLex, err := lexical.Evaluate(ctx, gold, k, topK)
	if err != nil {
		t.Fatal(err)
	}
	logReport(t, "pooled corpus / lexical (real-embedder run)", repLex)

	// Build a fresh index for the dense runner (EnableDense embeds the corpus via
	// the live server — this is the network-dependent step).
	ixDense, _, _, _ := BuildGoldCorpusIndex()
	dense := NewRunner(ixDense)
	nChunks, err := dense.EnableDense(ctx, emb, linesPerChunk, overlap)
	if err != nil {
		// The service was configured but unreachable/failing: report and skip rather
		// than fail, so a flaky local server never reds the suite.
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
	// No hard assertion on the real-embedder delta: it depends on the model and is
	// the number we are trying to OBSERVE, not gate on, in this first run.
}
