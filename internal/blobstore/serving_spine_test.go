package blobstore

import (
	"context"
	"path/filepath"
	"testing"

	"moedex/internal/search"
	server "moedex/internal/serve"
)

// mustFindLiteral runs q against the corpus and asserts at least one match whose
// RelPath contains wantRel, returning all matches for further inspection.
func mustFindLiteral(t *testing.T, c *server.Corpus, q, wantRel string) []search.Match {
	t.Helper()
	matches, _, err := c.Literal(context.Background(), q)
	if err != nil {
		t.Fatalf("Literal(%q): %v", q, err)
	}
	found := false
	for _, m := range matches {
		if filepath.Base(m.RelPath) == wantRel {
			found = true
		}
	}
	if !found {
		t.Errorf("Literal(%q) did not return a match in %s; got %d matches", q, wantRel, len(matches))
	}
	return matches
}

// TestExportOpensThroughServingSpine asserts the CAS-exported shard dir is
// byte-for-byte servable: it opens through the real serving-spine entry points
// (server.Open and server.OpenRank) with zero changes, and NumBlobs equals the
// unique-content count the corpus produced. This is the parity-preserving bridge:
// the warm daemon consumes a CAS export exactly as it consumes a direct build.
func TestExportOpensThroughServingSpine(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	shared := "package shared\nfunc Help() {}\n"
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	commitGitRepo(t, repoA, map[string]string{"shared.go": shared, "a.go": "package a\nfunc MarkerA() {}\n"})
	commitGitRepo(t, repoB, map[string]string{"shared.go": shared, "b.go": "package b\nfunc MarkerB() {}\n"})

	casDir := t.TempDir()
	m, err := BuildCAS(corpus, casDir)
	if err != nil {
		t.Fatalf("BuildCAS: %v", err)
	}
	shardDir := filepath.Join(t.TempDir(), "shards")
	if _, err := ExportShardDir(casDir, shardDir, 1<<30); err != nil {
		t.Fatalf("ExportShardDir: %v", err)
	}

	wantUnique := distinctContentCount(t, m)

	// server.Open: the warm retrieval spine.
	c, err := server.Open(shardDir)
	if err != nil {
		t.Fatalf("server.Open exported dir: %v", err)
	}
	defer c.Close()
	if c.NumShards() < 1 {
		t.Errorf("NumShards = %d, want >=1", c.NumShards())
	}
	if c.NumBlobs() != wantUnique {
		t.Errorf("server.Open NumBlobs = %d, want %d (within-shard dedup)", c.NumBlobs(), wantUnique)
	}

	// Retrieval actually returns the exported content with correct paths: a
	// repoA-only marker resolves to repoA's file, a repoB-only marker to repoB's.
	mustFindLiteral(t, c, "MarkerA", "a.go")
	mustFindLiteral(t, c, "MarkerB", "b.go")
	// The deduped shared blob carries BOTH repos' file refs: a query for its
	// content surfaces shared.go under each repo (no line ripgrep would return is
	// dropped by dedup — the union of paths is conserved).
	sharedMatches := mustFindLiteral(t, c, "func Help", "shared.go")
	repos := map[string]bool{}
	for _, mm := range sharedMatches {
		repos[mm.Repo] = true
	}
	if !repos["repoA"] || !repos["repoB"] {
		t.Errorf("shared blob did not surface both repos' file refs: got %v", repos)
	}

	// server.OpenRank: the ranked, agent-facing spine (pure-lexical, no dense arm).
	rc, err := server.OpenRank(context.Background(), shardDir, server.RankConfig{})
	if err != nil {
		t.Fatalf("server.OpenRank exported dir: %v", err)
	}
	if rc.NumBlobs() != wantUnique {
		t.Errorf("OpenRank NumBlobs = %d, want %d", rc.NumBlobs(), wantUnique)
	}
}

// TestBOMAndNonBOMDedupToOneBlobBothPathsSurface documents and proves the
// intentional BOM dedup semantics (RESIDUAL 2). ingest.Repo strips a leading
// UTF-8 BOM before content reaches the CAS, and contentKey hashes those
// post-strip bytes — so a BOM-prefixed file and a non-BOM file with identical
// post-strip content dedup to ONE CAS blob (a direct moedex-index build, keying
// its index by git's blob SHA of the UNstripped file, would keep two). This is
// parity-safe: both paths trigram-index and search the SAME BOM-stripped bytes,
// so the (file,line) results are identical; the CAS merely dedups more, carrying
// BOTH file refs on the single blob. This test asserts the single blob AND that
// BOTH paths still surface for a query on the shared content — no path is lost.
func TestBOMAndNonBOMDedupToOneBlobBothPathsSurface(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	repo := filepath.Join(corpus, "repo")

	body := "package shared\nfunc BomNeedle() {}\n"
	const bom = "\xEF\xBB\xBF" // UTF-8 BOM
	commitGitRepo(t, repo, map[string]string{
		"with_bom.go": bom + body, // on-disk bytes carry the BOM
		"no_bom.go":   body,       // identical post-strip content
	})

	// Premise: the two files have DIFFERENT git blob SHAs (one carries the BOM),
	// so a direct build would index them as two blobs.
	if lsFilesSHA(t, repo, "with_bom.go") == lsFilesSHA(t, repo, "no_bom.go") {
		t.Fatal("test premise: BOM and non-BOM files must have different git SHAs")
	}

	casDir := t.TempDir()
	m, err := BuildCAS(corpus, casDir)
	if err != nil {
		t.Fatalf("BuildCAS: %v", err)
	}

	// Both files key by the SAME content key (BOM stripped) -> exactly ONE blob.
	keyBOM := shaInRepo(t, m, repo, "with_bom.go")
	keyNo := shaInRepo(t, m, repo, "no_bom.go")
	if keyBOM != keyNo {
		t.Fatalf("BOM and non-BOM files must share a content key, got %q vs %q", keyBOM, keyNo)
	}
	s := openStore(t, casDir)
	defer s.Close()
	if s.Len() != 1 {
		t.Errorf("store.Len = %d, want 1 (BOM + non-BOM dedup to one blob)", s.Len())
	}
	// The stored blob is the BOM-STRIPPED body (what is indexed/searched).
	got, err := s.Get(keyBOM)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("stored content = %q, want BOM-stripped body %q", got, body)
	}

	// Export and serve: a query for the shared content must surface BOTH paths
	// (proving the dedup dropped no file ref).
	shardDir := filepath.Join(t.TempDir(), "shards")
	if _, err := ExportShardDir(casDir, shardDir, 1<<30); err != nil {
		t.Fatalf("ExportShardDir: %v", err)
	}
	c, err := server.Open(shardDir)
	if err != nil {
		t.Fatalf("server.Open: %v", err)
	}
	defer c.Close()

	matches, _, err := c.Literal(context.Background(), "BomNeedle")
	if err != nil {
		t.Fatalf("Literal: %v", err)
	}
	paths := map[string]bool{}
	for _, mm := range matches {
		paths[filepath.Base(mm.RelPath)] = true
	}
	if !paths["with_bom.go"] || !paths["no_bom.go"] {
		t.Errorf("both paths must surface for the shared content; got %v", paths)
	}
}
