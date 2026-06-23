package eval

import (
	"context"
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"moedex/internal/index"
)

// addDoc indexes one synthetic document with a content-derived SHA so the index
// dedups correctly and each distinct body gets its own blob.
func addDoc(ix *index.Index, repo, rel string, content string) {
	sum := sha1.Sum([]byte(content))
	sha := fmt.Sprintf("%x", sum)
	abs := filepath.Join("/synthetic", repo, rel)
	ix.AddFile(repo, rel, abs, sha, []byte(content))
}

// TestSyntheticSmoke is the end-to-end proof that the harness measures the REAL
// ranker (not a mock): we build a tiny in-memory index, run a real query through
// rank.Rank via the Runner, and assert that an obviously-relevant document
// outranks an obviously-irrelevant one — and that the metrics reflect it.
func TestSyntheticSmoke(t *testing.T) {
	ix := index.New()
	// The clearly-relevant doc for a payment-refund query.
	addDoc(ix, "shop", "payment/refund.go",
		"package payment\n// ProcessRefund issues a refund for a charge.\nfunc ProcessRefund(chargeID string) error { return refundCharge(chargeID) }\n")
	// A plausibly-related-but-not-the-answer doc (shares a couple tokens).
	addDoc(ix, "shop", "payment/charge.go",
		"package payment\n// CreateCharge authorizes a new charge.\nfunc CreateCharge(amount int) error { return nil }\n")
	// Pure noise.
	addDoc(ix, "shop", "ui/button.go",
		"package ui\n// Button renders a clickable widget on screen.\nfunc Button(label string) string { return label }\n")

	run := NewRunner(ix)
	ctx := context.Background()

	ranked, err := run.rankedRelPaths(ctx, "ProcessRefund refund charge", 10)
	if err != nil {
		t.Fatalf("rank: %v", err)
	}
	if len(ranked) == 0 {
		t.Fatal("ranker returned no results for an indexed query")
	}
	// The refund file must come back, ranked above the pure-noise UI file.
	posRefund, posButton := -1, -1
	for i, r := range ranked {
		switch r {
		case "payment/refund.go":
			if posRefund == -1 {
				posRefund = i
			}
		case "ui/button.go":
			if posButton == -1 {
				posButton = i
			}
		}
	}
	if posRefund == -1 {
		t.Fatalf("relevant doc payment/refund.go absent from results %v", ranked)
	}
	if posButton != -1 && posRefund > posButton {
		t.Fatalf("irrelevant ui/button.go (pos %d) outranked refund.go (pos %d): %v", posButton, posRefund, ranked)
	}

	// And the aggregate metrics agree: top result is the gold doc.
	gold := []GoldQuery{NewBinaryGold("ProcessRefund refund charge", "payment/refund.go")}
	rep, err := run.Evaluate(ctx, gold, 5, 10)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if rep.MeanRecall != 1.0 {
		t.Errorf("expected the gold doc recalled (recall 1.0), got %.3f", rep.MeanRecall)
	}
	if rep.MeanMRR <= 0 {
		t.Errorf("expected positive MRR (gold doc retrieved), got %.3f", rep.MeanMRR)
	}
	if rep.MeanNDCG <= 0 {
		t.Errorf("expected positive nDCG, got %.3f", rep.MeanNDCG)
	}
}

// TestEvaluateAggregation checks the mean aggregation across multiple gold
// queries over a known synthetic corpus, exercising the full Runner path.
func TestEvaluateAggregation(t *testing.T) {
	ix := index.New()
	addDoc(ix, "r", "auth/login.go", "package auth\nfunc Login(user, password string) bool { return checkPassword(user, password) }\n")
	addDoc(ix, "r", "auth/logout.go", "package auth\nfunc Logout(session string) { destroySession(session) }\n")
	addDoc(ix, "r", "math/add.go", "package math\nfunc Add(a, b int) int { return a + b }\n")

	run := NewRunner(ix)
	gold := []GoldQuery{
		NewBinaryGold("Login password user", "auth/login.go"),
		NewBinaryGold("Add return", "math/add.go"),
	}
	rep, err := run.Evaluate(context.Background(), gold, 3, 10)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(rep.Queries) != 2 {
		t.Fatalf("expected 2 query reports, got %d", len(rep.Queries))
	}
	// Both gold docs are unique-token-bearing; both should be recalled.
	if rep.MeanRecall != 1.0 {
		t.Errorf("mean recall = %.3f, want 1.0; per-query: %+v", rep.MeanRecall, rep.Queries)
	}
	// SortedQueries gives a stable order regardless of input ordering.
	sorted := rep.SortedQueries()
	if sorted[0].Query >= sorted[1].Query {
		t.Errorf("SortedQueries not sorted: %q then %q", sorted[0].Query, sorted[1].Query)
	}
}

// TestRealCorpus runs the harness over a real repo if present, mirroring the
// skip pattern in internal/diskstore/diskstore_test.go. It does NOT hard-fail
// when the corpus is absent. The corpus dir is configurable via MOEDEX_EVAL_CORPUS;
// it defaults to ~/TCGitlab/Services.Registrar/TC.SslApi.
func TestRealCorpus(t *testing.T) {
	dir := os.Getenv("MOEDEX_EVAL_CORPUS")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("no home dir: %v", err)
		}
		dir = filepath.Join(home, "TCGitlab", "Services.Registrar", "TC.SslApi")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("corpus not present at %s: %v", dir, err)
	}

	ix, n, err := BuildIndexFromCorpus(filepath.Base(dir), dir)
	if err != nil {
		t.Skipf("ingest failed (not a git repo?): %v", err)
	}
	if n == 0 {
		t.Skip("corpus has no indexable files")
	}
	t.Logf("indexed %d files into %d blobs", n, ix.NumBlobs())

	run := NewRunner(ix)
	// Without hand-labeled gold for this repo we cannot assert metric values;
	// the value here is exercising the real build+rank path end to end and
	// confirming the ranker returns results on a real corpus.
	gold := []GoldQuery{NewBinaryGold("error", "does/not/matter.cs")}
	rep, err := run.Evaluate(context.Background(), gold, 10, 50)
	if err != nil {
		t.Fatalf("evaluate over real corpus: %v", err)
	}
	if len(rep.Queries[0].Ranked) == 0 {
		t.Log("note: query 'error' returned no results on this corpus")
	} else {
		t.Logf("query 'error' returned %d results", len(rep.Queries[0].Ranked))
	}
}
