package blobstore

// Compaction-GC ORPHAN-SHARD regression gate (SACRED, FIX #1).
//
// The SERVER decides what to serve by globbing `*.idx` (server.Open /
// loadUnified), NOT by reading the parity manifest's shard list. A GC tool that
// DELETES data must key off that identical signal. This test plants an ORPHAN shard
// (present on disk and globbed, but absent from the manifest) whose MOEDEX05 refs
// point at content present in blobs.dat, with a unique literal marker only that shard
// surfaces. After compaction the orphan's marker MUST still surface and its content
// MUST still be in blobs.dat — proving compaction carried the globbed-but-unrecorded
// shard + its content forward instead of dropping both (the under-approximation a
// manifest-driven compactor causes).
//
// This test FAILS on a manifest-driven CompactDedupedShardDir (the orphan's content is
// omitted from `live` and the shard file is never carried forward, so both are
// deleted by the swap) and PASSES once liveness + carry-forward derive from the glob
// set.

import (
	"context"
	"path/filepath"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/server"
)

// TestCompactDedupedCarriesOrphanShard is the FIX #1 regression gate.
func TestCompactDedupedCarriesOrphanShard(t *testing.T) {
	requireGit(t)
	const shardBytes = 1 << 11

	// A normal multi-repo deduped served dir.
	corpus := t.TempDir()
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	commitGitRepo(t, repoA, map[string]string{"a.go": "package a\nfunc AlphaThing() { /* manifest_repo_marker_a */ }\n" + largeBody("AlphaFiller", 30)})
	commitGitRepo(t, repoB, map[string]string{"b.go": "package b\nfunc BravoThing() { /* manifest_repo_marker_b */ }\n" + largeBody("BravoFiller", 30)})

	casDir := t.TempDir()
	if _, err := BuildCAS(corpus, casDir); err != nil {
		t.Fatalf("BuildCAS: %v", err)
	}
	dedupDir := filepath.Join(t.TempDir(), "deduped")
	if _, _, err := ExportDedupedShardDir(casDir, dedupDir, shardBytes); err != nil {
		t.Fatalf("ExportDedupedShardDir: %v", err)
	}

	// --- Plant an ORPHAN shard: a MOEDEX05 shard-9999.idx the manifest does NOT list,
	// whose unique content is appended to the EXISTING blobs.dat (so its refs resolve),
	// using the SAME diskstore primitives the real delta export uses. -----------------
	orphanMarker := "orphan_only_unique_marker_zzz"
	orphanContent := []byte("package orphan\nfunc OrphanThing() { /* " + orphanMarker + " */ }\n")
	orphanSHA := diskstore.GitBlobSHA1(orphanContent)

	csPath := filepath.Join(dedupDir, diskstore.ContentStoreName)
	appender, err := diskstore.OpenContentStoreAppender(csPath)
	if err != nil {
		t.Fatalf("OpenContentStoreAppender: %v", err)
	}
	appender.PutContent(orphanSHA, orphanContent)
	if err := appender.Write(csPath); err != nil { // extend blobs.dat in place
		t.Fatalf("appender.Write: %v", err)
	}

	// Build a one-blob index for the orphan and save it as shard-9999.idx against an
	// appender seeded from the now-extended store (PutContent is a dedup no-op since the
	// content is already present), so the shard's refs resolve at serve time.
	ix := index.New()
	ix.AddFile("orphanRepo", "orphan.go", "/orphanRepo/orphan.go", orphanSHA, orphanContent)
	ap2, err := diskstore.OpenContentStoreAppender(csPath)
	if err != nil {
		t.Fatalf("OpenContentStoreAppender (2): %v", err)
	}
	orphanShardPath := filepath.Join(dedupDir, "shard-9999.idx")
	if err := diskstore.SaveDedupedAppender(ix, orphanShardPath, ap2); err != nil {
		t.Fatalf("SaveDedupedAppender (orphan shard): %v", err)
	}
	// ap2 added no net-new content (orphan content already present), so blobs.dat is
	// unchanged; we do NOT rewrite it from ap2.
	if ap2.BytesAppended() != 0 {
		t.Fatalf("orphan shard unexpectedly added %d net-new bytes (content should already be present)", ap2.BytesAppended())
	}

	// --- PRE-compaction: the server (glob-driven) serves the orphan marker. ----------
	pre, err := server.Open(dedupDir)
	if err != nil {
		t.Fatalf("server.Open (pre): %v", err)
	}
	preMs, _, err := pre.Literal(context.Background(), orphanMarker)
	pre.Close()
	if err != nil {
		t.Fatalf("Literal(orphan, pre): %v", err)
	}
	if len(preMs) == 0 {
		t.Fatalf("setup failed: orphan marker not served pre-compaction (the orphan shard is not wired up)")
	}

	// --- COMPACT. --------------------------------------------------------------------
	if _, err := CompactDedupedShardDir(dedupDir); err != nil {
		t.Fatalf("CompactDedupedShardDir: %v", err)
	}

	// --- POST-compaction: the orphan content is STILL in blobs.dat AND the orphan
	// marker STILL surfaces (the shard file was carried forward + its content kept). ---
	shasAfter, err := diskstore.ContentStoreSHAs(csPath)
	if err != nil {
		t.Fatalf("ContentStoreSHAs (post): %v", err)
	}
	foundContent := false
	for _, s := range shasAfter {
		if s == orphanSHA {
			foundContent = true
			break
		}
	}
	if !foundContent {
		t.Errorf("SACRED VIOLATION: orphan shard's content hash %s was dropped from blobs.dat by compaction", orphanSHA)
	}

	post, err := server.Open(dedupDir)
	if err != nil {
		t.Fatalf("server.Open (post): %v", err)
	}
	defer post.Close()
	postMs, _, err := post.Literal(context.Background(), orphanMarker)
	if err != nil {
		t.Fatalf("Literal(orphan, post): %v", err)
	}
	if len(postMs) == 0 {
		t.Errorf("SACRED VIOLATION: orphan shard's marker %q no longer surfaces after compaction (shard file or content dropped)", orphanMarker)
	}
	// The recorded manifest repo markers still surface too (sanity: compaction did not
	// break the normal shards).
	mustFindLiteralCtx(t, post, "manifest_repo_marker_a", "a.go")
	mustFindLiteralCtx(t, post, "manifest_repo_marker_b", "b.go")
}
