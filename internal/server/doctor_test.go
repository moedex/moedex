package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestInspectShardDir covers the read-only shard-dir snapshot doctor relies on:
// layout/refresh-command detection and dense-sidecar version + freshness.
func TestInspectShardDir(t *testing.T) {
	dir := t.TempDir()
	buildShard(t, dir, "shard-0000.idx", map[string]string{
		"auth/login.go": "package auth\n\nfunc Authenticate(token string) bool { return token != \"\" }\n",
		"calc/add.go":   "package calc\n\nfunc Add(a, b int) int { return a + b }\n",
	})

	// Before embedding: shards present, no dense store yet.
	info, err := InspectShardDir(dir)
	if err != nil {
		t.Fatalf("InspectShardDir: %v", err)
	}
	if info.Shards != 1 {
		t.Fatalf("Shards = %d, want 1", info.Shards)
	}
	if info.Deduped {
		t.Error("a diskstore.Save shard should be INLINED, not deduped")
	}
	if info.StoreExists {
		t.Error("no embedding store should exist yet")
	}
	if got, want := info.RefreshCommand(), "moedex-index refresh -shard-dir "+dir; got != want {
		t.Errorf("RefreshCommand = %q, want %q", got, want)
	}

	// Build a v2 dense store via the incremental builder, then re-inspect.
	storePath := filepath.Join(dir, "corpus-embeddings.store")
	cfg := RankConfig{TopK: 5, Emb: conceptEmbedder{}, EmbedModel: "concept-v1", StorePath: storePath, LinesPerChunk: 20, Overlap: 5}
	if _, err := RefreshEmbeddings(context.Background(), dir, cfg); err != nil {
		t.Fatalf("RefreshEmbeddings: %v", err)
	}
	info, err = InspectShardDir(dir)
	if err != nil {
		t.Fatalf("InspectShardDir after embed: %v", err)
	}
	if !info.StoreExists || !info.StoreMetaPresent {
		t.Fatalf("store should exist with meta, got %+v", info)
	}
	if info.StoreVersion != 2 {
		t.Errorf("StoreVersion = %d, want 2 (incremental-ready)", info.StoreVersion)
	}
	if info.StoreChunks == 0 {
		t.Error("StoreChunks should be > 0")
	}
	if info.StoreModel != "concept-v1" {
		t.Errorf("StoreModel = %q, want concept-v1", info.StoreModel)
	}
	if !info.StoreFresh {
		t.Error("store should be FRESH immediately after building over the same shards")
	}

	// Changing the shard set must make the store read STALE (fingerprint drift).
	buildShard(t, dir, "shard-0001.idx", map[string]string{"x/y.go": "package x\nfunc New() {}\n"})
	info, err = InspectShardDir(dir)
	if err != nil {
		t.Fatalf("InspectShardDir after shard add: %v", err)
	}
	if info.Shards != 2 {
		t.Fatalf("Shards = %d, want 2", info.Shards)
	}
	if info.StoreFresh {
		t.Error("store should be STALE after the shard set changed")
	}
}

// TestInspectShardDir_CASLayout verifies a blobmanifest.json flips the recommended
// refresh command to cas-refresh (the wrong one was run in the index-loss incident).
func TestInspectShardDir_CASLayout(t *testing.T) {
	dir := t.TempDir()
	buildShard(t, dir, "shard-0000.idx", map[string]string{"a/x.go": "package a\nfunc Foo() {}\n"})
	if err := os.WriteFile(filepath.Join(dir, "blobmanifest.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := InspectShardDir(dir)
	if err != nil {
		t.Fatalf("InspectShardDir: %v", err)
	}
	if !info.HasBlobManifest {
		t.Fatal("HasBlobManifest should be true")
	}
	if got, want := info.RefreshCommand(), "moedex-index cas-refresh -cas-dir "+dir; got != want {
		t.Errorf("RefreshCommand = %q, want %q", got, want)
	}
}

// TestInspectShardDir_NoShards surfaces the dangerous "undetectable layout" case
// as an error (doctor turns this into a CRITICAL).
func TestInspectShardDir_NoShards(t *testing.T) {
	if _, err := InspectShardDir(t.TempDir()); err == nil {
		t.Fatal("expected an error for a dir with no shards")
	}
}
