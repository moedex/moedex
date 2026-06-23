package server

import (
	"context"
	"strings"
	"testing"
)

func TestRankCorpusRanksAcrossShards(t *testing.T) {
	dir := t.TempDir()
	// Two shards, each with a distinct Go definition. The symbol arm should fire
	// (Go extractor) and the BM25 arm should pull the defining file to the top.
	buildShard(t, dir, "shard-0000.idx", map[string]string{
		"billing/refund.go": "package billing\n\n// Refund reverses a captured charge.\nfunc Refund(id string) error {\n\treturn nil\n}\n",
		"util/strings.go":    "package util\n\nfunc TrimSpace(s string) string { return s }\n",
	})
	buildShard(t, dir, "shard-0001.idx", map[string]string{
		"auth/login.go": "package auth\n\n// Authenticate verifies user credentials.\nfunc Authenticate(user, pass string) bool {\n\treturn false\n}\n",
	})

	rc, err := OpenRank(dir, RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("OpenRank: %v", err)
	}
	if rc.NumBlobs() != 3 {
		t.Errorf("NumBlobs = %d, want 3", rc.NumBlobs())
	}
	if rc.NumDocs() != 3 {
		t.Errorf("NumDocs = %d, want 3", rc.NumDocs())
	}
	if rc.NumSymbolBlobs() == 0 {
		t.Error("expected Go symbol extraction to populate symbol blobs, got 0")
	}

	// Query for a term that defines a symbol in shard 0; the top context block
	// must come from the defining file, proving ranking works over the merged
	// global blob-ID space.
	win, err := rc.SearchContext(context.Background(), "refund", 800, 5)
	if err != nil {
		t.Fatalf("SearchContext: %v", err)
	}
	if len(win.Blocks) == 0 {
		t.Fatal("expected at least one context block for 'refund', got none")
	}
	if !strings.Contains(win.Blocks[0].RelPath, "refund.go") {
		t.Errorf("top block = %q, want billing/refund.go", win.Blocks[0].RelPath)
	}

	// A term that only exists in shard 1 must still be reachable from the merged
	// corpus, proving the second shard's blobs are ranked too.
	win2, err := rc.SearchContext(context.Background(), "authenticate", 800, 5)
	if err != nil {
		t.Fatalf("SearchContext authenticate: %v", err)
	}
	if len(win2.Blocks) == 0 || !strings.Contains(win2.Blocks[0].RelPath, "login.go") {
		t.Errorf("expected auth/login.go on top for 'authenticate', got %+v", win2.Blocks)
	}
}

func TestOpenRankEmptyDirErrors(t *testing.T) {
	if _, err := OpenRank(t.TempDir(), RankConfig{}); err == nil {
		t.Error("expected error opening dir with no shards, got nil")
	}
}
