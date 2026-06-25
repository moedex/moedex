package blobstore

// Compaction-GC SPACE / fixture gate (PROOF 2 of the compaction lane).
//
// Asserts the concrete reclaim, at the byte/blob level:
//   - the compacted store's content size == the sum of LIVE blob content (== a fresh
//     full build's size), STRICTLY LESS than the pre-compaction append-only size;
//   - every removed/dead blob's content hash is GONE from the store, and
//   - every live blob's content hash AND every live file path still surfaces.
// These are direct store-level assertions (not just search-result equality), the
// complement to TestCompactionParity.

import (
	"path/filepath"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/server"
)

// TestCompactionRemovesDeadBlobBytes is the byte-level fixture proof for the CAS pack.
func TestCompactionRemovesDeadBlobBytes(t *testing.T) {
	requireGit(t)
	corpus, casDir, _, _, _, _, _ := buildDeadBlobCorpus(t)
	_ = corpus

	// The live set = the union of the (refreshed) manifest's referenced blobs.
	mPre, err := LoadBlobManifest(filepath.Join(casDir, BlobManifestName))
	if err != nil {
		t.Fatal(err)
	}
	livePre := mPre.referencedBlobs()

	// Identify the DEAD blobs in the pack pre-compaction: present in the pack, absent
	// from the live set. There MUST be some (repoC removal + bravo churn created them).
	srcPre, err := Open(casDir)
	if err != nil {
		t.Fatal(err)
	}
	allPre := srcPre.SHAs()
	var dead []string
	var liveBytesPre int64
	for _, sha := range allPre {
		if livePre[sha] {
			e := srcPre.byID[sha]
			liveBytesPre += e.contentLen
		} else {
			dead = append(dead, sha)
		}
	}
	beforeBytes := srcPre.BytesStored()
	srcPre.Close()
	if len(dead) == 0 {
		t.Fatal("fixture produced no dead blobs in the CAS pack")
	}
	t.Logf("CAS pre-compaction: %d blobs (%dB), %d live, %d DEAD", len(allPre), beforeBytes, len(livePre), len(dead))

	// ---- COMPACT. -------------------------------------------------------------------
	st, err := CompactCAS(casDir)
	if err != nil {
		t.Fatalf("CompactCAS: %v", err)
	}

	// ---- SPACE INVARIANTS. ----------------------------------------------------------
	// after == live content sum, strictly less than before.
	if st.AfterBytes != liveBytesPre {
		t.Errorf("compacted CAS content = %d, want sum of live blob content %d", st.AfterBytes, liveBytesPre)
	}
	if st.AfterBytes >= beforeBytes {
		t.Errorf("compacted CAS not smaller: before %d, after %d", beforeBytes, st.AfterBytes)
	}
	if st.DeadBlobs != len(dead) {
		t.Errorf("compaction reported %d dead blobs, fixture had %d", st.DeadBlobs, len(dead))
	}

	// ---- Every DEAD blob hash is GONE; every LIVE blob hash REMAINS. ----------------
	post, err := Open(casDir)
	if err != nil {
		t.Fatal(err)
	}
	defer post.Close()
	for _, sha := range dead {
		if post.Has(sha) {
			t.Errorf("dead blob %s still present in compacted CAS pack", sha)
		}
	}
	for sha := range livePre {
		if !post.Has(sha) {
			t.Errorf("LIVE blob %s missing from compacted CAS pack (SACRED under-approximation)", sha)
		}
	}
	if post.Len() != len(livePre) {
		t.Errorf("compacted CAS holds %d blobs, want exactly the %d live", post.Len(), len(livePre))
	}

	// ---- A fresh full BuildCAS of the post-change corpus stores the SAME bytes. -----
	freshDir := t.TempDir()
	fresh, err := BuildCAS(corpus, freshDir)
	if err != nil {
		t.Fatalf("fresh BuildCAS: %v", err)
	}
	if st.AfterBytes != fresh.Stats.StoredBytes {
		t.Errorf("compacted CAS content %d != fresh full build %d", st.AfterBytes, fresh.Stats.StoredBytes)
	}
}

// TestCompactionDedupedStoreFixture is the byte/path-level fixture proof for the
// deduped served store: dead content hashes gone from blobs.dat, every live content
// hash kept, every live file path still served, footprint strictly smaller and ==
// a fresh full deduped export's footprint.
func TestCompactionDedupedStoreFixture(t *testing.T) {
	requireGit(t)
	const shardBytes = 1 << 11
	corpus, _, dedupDir, _, deadContentMarker, liveNewMarker, survivorMarker := buildDeadBlobCorpus(t)

	csPath := filepath.Join(dedupDir, diskstore.ContentStoreName)

	// Liveness from the live shards (the exact signal CompactDedupedShardDir uses).
	live := liveShardSHAs(t, dedupDir)

	// Identify dead content hashes pre-compaction: in blobs.dat but no live shard refs.
	csPre, err := diskstore.OpenContentStore(csPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes := csPre.BytesStored()
	beforeBlobs := csPre.Len()
	var deadHashes []string
	for _, sha := range allStoreSHAs(t, csPath) {
		if !live[sha] {
			deadHashes = append(deadHashes, sha)
		}
	}
	csPre.Close()
	if len(deadHashes) == 0 {
		t.Fatal("fixture produced no dead content in blobs.dat")
	}
	t.Logf("deduped pre-compaction: %d content records (%dB), %d live refs, %d DEAD", beforeBlobs, beforeBytes, len(live), len(deadHashes))

	// ---- COMPACT. -------------------------------------------------------------------
	st, err := CompactDedupedShardDir(dedupDir)
	if err != nil {
		t.Fatalf("CompactDedupedShardDir: %v", err)
	}

	// ---- SPACE. ---------------------------------------------------------------------
	if st.AfterBytes >= beforeBytes {
		t.Errorf("deduped not smaller: before %d, after %d", beforeBytes, st.AfterBytes)
	}
	if st.DeadBlobs != len(deadHashes) {
		t.Errorf("compaction reported %d dead, fixture had %d", st.DeadBlobs, len(deadHashes))
	}

	// ---- Every dead content hash GONE; every live content hash KEPT. ----------------
	csPost, err := diskstore.OpenContentStore(csPath)
	if err != nil {
		t.Fatal(err)
	}
	defer csPost.Close()
	for _, sha := range deadHashes {
		if _, ok := csPost.Content(sha); ok {
			t.Errorf("dead content %s still present in compacted blobs.dat", sha)
		}
	}
	for sha := range live {
		if _, ok := csPost.Content(sha); !ok {
			t.Errorf("LIVE content %s missing from compacted blobs.dat (SACRED under-approximation)", sha)
		}
	}
	if csPost.Len() != len(live) {
		t.Errorf("compacted blobs.dat holds %d records, want exactly the %d live", csPost.Len(), len(live))
	}
	// Content integrity verify (the default serving check) must pass on the compacted store.
	if err := csPost.Verify(diskstore.GitBlobSHA1); err != nil {
		t.Errorf("compacted blobs.dat fails content-integrity verify: %v", err)
	}

	// ---- A fresh full deduped export of the post-change corpus has the SAME footprint.
	freshCAS := t.TempDir()
	if _, err := BuildCAS(corpus, freshCAS); err != nil {
		t.Fatalf("fresh BuildCAS: %v", err)
	}
	freshDir := filepath.Join(t.TempDir(), "fresh-deduped")
	_, freshBytes, err := ExportDedupedShardDir(freshCAS, freshDir, shardBytes)
	if err != nil {
		t.Fatalf("fresh ExportDedupedShardDir: %v", err)
	}
	if st.AfterBytes != freshBytes {
		t.Errorf("compacted blobs.dat content %d != fresh full export %d", st.AfterBytes, freshBytes)
	}

	// ---- Every LIVE file PATH still surfaces; the dead ones do not. -----------------
	c, err := server.Open(dedupDir)
	if err != nil {
		t.Fatalf("server.Open compacted deduped: %v", err)
	}
	defer c.Close()
	mustFindLiteralCtx(t, c, liveNewMarker, "bravo.go")
	mustFindLiteralCtx(t, c, survivorMarker, "delta.go")
	mustFindLiteralCtx(t, c, "CrossRepoNeedle", "shared.go")
	mustNotFindLiteral(t, c, deadContentMarker)
}

// liveShardSHAs returns the union of content-hash refs across every shard recorded in
// the dir's manifest (mirrors CompactDedupedShardDir's liveness computation).
func liveShardSHAs(t *testing.T, dir string) map[string]bool {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.idx"))
	if err != nil {
		t.Fatal(err)
	}
	live := map[string]bool{}
	for _, p := range paths {
		shas, err := diskstore.DedupedShardSHAs(p)
		if err != nil {
			t.Fatalf("DedupedShardSHAs %s: %v", p, err)
		}
		for _, sha := range shas {
			live[sha] = true
		}
	}
	return live
}

// allStoreSHAs returns every content hash physically present in a MOECONT1 store, by
// walking its directory section.
func allStoreSHAs(t *testing.T, csPath string) []string {
	t.Helper()
	shas, err := diskstore.ContentStoreSHAs(csPath)
	if err != nil {
		t.Fatalf("ContentStoreSHAs: %v", err)
	}
	return shas
}
