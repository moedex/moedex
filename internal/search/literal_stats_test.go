package search_test

import (
	"context"
	"testing"

	"moedex/internal/search"
)

// TestLiteralStatsDedupesCandidateBlobs locks in the contract that Stats
// counts each matching blob once even when the literal occurs on multiple
// lines within that blob: the per-blob bookkeeping in the positional
// begin/end-gram merge-join must not double count a blob it has already
// charged to CandidateBlobs/CandidateBytes/CandidateLines.
func TestLiteralStatsDedupesCandidateBlobs(t *testing.T) {
	blobs := map[string]string{
		"a.txt": "foobar\nfoobar\n", // two occurrences, same blob
		"b.txt": "xfoobarx\n",       // one occurrence, different blob
	}
	ix := buildIndex(blobs)

	_, stats, err := search.LiteralWithStats(context.Background(), ix, "foobar")
	if err != nil {
		t.Fatalf("LiteralWithStats: %v", err)
	}
	if stats.CandidateBlobs != 2 {
		t.Errorf("CandidateBlobs = %d, want 2", stats.CandidateBlobs)
	}
	wantBytes := int64(len(blobs["a.txt"]) + len(blobs["b.txt"]))
	if stats.CandidateBytes != wantBytes {
		t.Errorf("CandidateBytes = %d, want %d", stats.CandidateBytes, wantBytes)
	}
	if stats.CandidateLines != 3 {
		t.Errorf("CandidateLines = %d, want 3", stats.CandidateLines)
	}
}
