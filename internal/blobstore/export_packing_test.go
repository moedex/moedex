package blobstore

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestExportPackingIdenticalAcrossLegacyAndDeduped pins the invariant called out
// in CODE_REVIEW_2026-06-30.md F-043: ExportShardDir and ExportDedupedShardDir
// each implement their own copy of the repo-to-shard packing loop (same
// manifest-order traversal, same per-repo "seen" abspath dedup, same
// shardBytes-threshold flush), and nothing currently asserts the two stay
// byte-for-byte identical in *which repos land in which shard* — only that the
// shared-content win holds (TestDedupedExportStoresSharedBlobOnce). A change to
// one copy's flush/dedup logic that isn't mirrored in the other would silently
// desync the packing without failing any existing test. Both exports must
// produce the SAME shard count and the SAME (Repos, ContentBytes) per shard
// for an identical input CAS + shardBytes.
func TestExportPackingIdenticalAcrossLegacyAndDeduped(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()

	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	repoC := filepath.Join(corpus, "repoC")
	shared := "package shared\n" + largeBody("PackingNeedle", 300)
	commitGitRepo(t, repoA, map[string]string{"shared.go": shared, "a.go": "package a\nfunc MarkerA() {}\n"})
	commitGitRepo(t, repoB, map[string]string{"shared.go": shared, "b.go": "package b\nfunc MarkerB() {}\n"})
	commitGitRepo(t, repoC, map[string]string{"shared.go": shared, "c.go": "package c\nfunc MarkerC() {}\n"})

	casDir := t.TempDir()
	if _, err := BuildCAS(corpus, casDir); err != nil {
		t.Fatalf("BuildCAS: %v", err)
	}

	// Small enough that each repo forces its own shard flush, so the comparison
	// actually exercises multiple shards rather than trivially matching at 1.
	const shardBytes = 1 << 10

	legacyDir := filepath.Join(t.TempDir(), "legacy")
	legacy, err := ExportShardDir(casDir, legacyDir, shardBytes)
	if err != nil {
		t.Fatalf("ExportShardDir: %v", err)
	}

	dedupedDir := filepath.Join(t.TempDir(), "deduped")
	deduped, _, err := ExportDedupedShardDir(casDir, dedupedDir, shardBytes)
	if err != nil {
		t.Fatalf("ExportDedupedShardDir: %v", err)
	}

	if len(legacy.Shards) < 2 {
		t.Fatalf("expected the multi-repo corpus to span >= 2 shards, got %d", len(legacy.Shards))
	}
	if len(legacy.Shards) != len(deduped.Shards) {
		t.Fatalf("shard count differs: legacy=%d deduped=%d", len(legacy.Shards), len(deduped.Shards))
	}
	for i := range legacy.Shards {
		l, d := legacy.Shards[i], deduped.Shards[i]
		if !reflect.DeepEqual(l.Repos, d.Repos) {
			t.Errorf("shard %d Repos differ: legacy=%v deduped=%v", i, l.Repos, d.Repos)
		}
		if l.ContentBytes != d.ContentBytes {
			t.Errorf("shard %d ContentBytes differ: legacy=%d deduped=%d", i, l.ContentBytes, d.ContentBytes)
		}
	}
	if !reflect.DeepEqual(legacy.Heads, deduped.Heads) {
		t.Errorf("Heads differ between exports:\nlegacy=%v\ndeduped=%v", legacy.Heads, deduped.Heads)
	}
}
