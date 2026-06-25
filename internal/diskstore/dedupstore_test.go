package diskstore

import (
	"context"
	"path/filepath"
	"testing"

	"moedex/internal/index"
	"moedex/internal/search"
)

// buildSampleIndex returns an index with three blobs (two unique, one duplicated
// across two file refs), keyed by git blob SHA so the content store dedups
// correctly. It mirrors the shapes the export path produces.
func buildSampleIndex(t *testing.T) *index.Index {
	t.Helper()
	ix := index.New()
	a := []byte("package main\nfunc Alpha() {}\n")
	b := []byte("package main\nfunc Bravo() {}\n")
	ix.AddFile("repo", "a.go", "/repo/a.go", gitBlobSHA(a), a)
	ix.AddFile("repo", "b.go", "/repo/b.go", gitBlobSHA(b), b)
	// a third FILE that is byte-identical to a.go: same SHA => one blob, two refs.
	ix.AddFile("repo", "a_copy.go", "/repo/a_copy.go", gitBlobSHA(a), a)
	return ix
}

// TestContentStoreRoundTrip writes a content store and asserts every blob's
// content reads back byte-identical, and an absent SHA is reported as absent.
func TestContentStoreRoundTrip(t *testing.T) {
	cw := NewContentStoreWriter()
	a := []byte("alpha content")
	b := []byte("bravo content longer")
	ra := cw.PutContent("sha-a", a)
	cw.PutContent("sha-b", b)
	// Idempotent: re-Put of sha-a returns the same ref and does not grow the store.
	if r := cw.PutContent("sha-a", a); r != ra {
		t.Errorf("re-Put ref = %+v, want %+v (idempotent)", r, ra)
	}
	if cw.Len() != 2 {
		t.Errorf("Len = %d, want 2 (dedup on sha)", cw.Len())
	}
	if cw.BytesStored() != int64(len(a)+len(b)) {
		t.Errorf("BytesStored = %d, want %d", cw.BytesStored(), len(a)+len(b))
	}

	path := filepath.Join(t.TempDir(), ContentStoreName)
	if err := cw.Write(path); err != nil {
		t.Fatalf("Write: %v", err)
	}
	cs, err := OpenContentStore(path)
	if err != nil {
		t.Fatalf("OpenContentStore: %v", err)
	}
	defer cs.Close()

	if cs.Len() != 2 {
		t.Errorf("loaded Len = %d, want 2", cs.Len())
	}
	if cs.BytesStored() != int64(len(a)+len(b)) {
		t.Errorf("loaded BytesStored = %d, want %d", cs.BytesStored(), len(a)+len(b))
	}
	gotA, ok := cs.Content("sha-a")
	if !ok || string(gotA) != string(a) {
		t.Errorf("Content(sha-a) = %q,%v, want %q,true", gotA, ok, a)
	}
	gotB, ok := cs.Content("sha-b")
	if !ok || string(gotB) != string(b) {
		t.Errorf("Content(sha-b) = %q,%v, want %q,true", gotB, ok, b)
	}
	if _, ok := cs.Content("sha-missing"); ok {
		t.Errorf("Content(sha-missing) reported present, want absent")
	}
}

// TestSaveDedupedRoundTrip saves an index in MOEDEX05 + a shared content store and
// asserts the reloaded index (LoadMmapDeduped) is identical to the source: same
// blob SHAs, file refs, content (resolved from the store), and that it serves the
// same literal/regex matches as the in-memory index — proving the content-less
// shard + shared store is a faithful, searchable round-trip.
func TestSaveDedupedRoundTrip(t *testing.T) {
	ix := buildSampleIndex(t)

	dir := t.TempDir()
	shardPath := filepath.Join(dir, "shard-0000.idx")
	cw := NewContentStoreWriter()
	if err := SaveDeduped(ix, shardPath, cw); err != nil {
		t.Fatalf("SaveDeduped: %v", err)
	}
	if err := cw.Write(filepath.Join(dir, ContentStoreName)); err != nil {
		t.Fatalf("content store Write: %v", err)
	}

	if !IsDeduped(shardPath) {
		t.Fatal("IsDeduped reported false for a MOEDEX05 shard")
	}

	cs, err := OpenContentStore(filepath.Join(dir, ContentStoreName))
	if err != nil {
		t.Fatalf("OpenContentStore: %v", err)
	}
	defer cs.Close()

	got, region, err := LoadMmapDeduped(shardPath, cs)
	if err != nil {
		t.Fatalf("LoadMmapDeduped: %v", err)
	}
	defer region.Close()

	if got.NumBlobs() != ix.NumBlobs() {
		t.Fatalf("NumBlobs = %d, want %d", got.NumBlobs(), ix.NumBlobs())
	}
	for id := uint64(0); id < uint64(ix.NumBlobs()); id++ {
		want := ix.Blob(id)
		gb := got.Blob(id)
		if gb.SHA != want.SHA {
			t.Errorf("blob %d SHA = %q, want %q", id, gb.SHA, want.SHA)
		}
		if string(gb.Content) != string(want.Content) {
			t.Errorf("blob %d content = %q, want %q", id, gb.Content, want.Content)
		}
		if len(gb.Files) != len(want.Files) {
			t.Errorf("blob %d has %d file refs, want %d", id, len(gb.Files), len(want.Files))
		}
	}

	// The deduped blob (a.go == a_copy.go) carries BOTH file refs.
	dup := got.Blob(0)
	if len(dup.Files) != 2 {
		t.Errorf("deduped blob has %d file refs, want 2 (a.go + a_copy.go)", len(dup.Files))
	}

	// Searchable: a literal in the duplicated content surfaces BOTH paths.
	matches, err := search.Literal(context.Background(), got, "func Alpha")
	if err != nil {
		t.Fatalf("Literal: %v", err)
	}
	paths := map[string]bool{}
	for _, m := range matches {
		paths[m.RelPath] = true
	}
	if !paths["a.go"] || !paths["a_copy.go"] {
		t.Errorf("deduped content did not surface both paths: got %v", paths)
	}

	// LoadBlobsDeduped (the ranker path) sees the same blob set + content.
	bs, err := LoadBlobsDeduped(shardPath, cs)
	if err != nil {
		t.Fatalf("LoadBlobsDeduped: %v", err)
	}
	if len(bs) != ix.NumBlobs() {
		t.Errorf("LoadBlobsDeduped returned %d blobs, want %d", len(bs), ix.NumBlobs())
	}
	if string(bs[0].Content) != string(ix.Blob(0).Content) {
		t.Errorf("LoadBlobsDeduped blob 0 content mismatch")
	}
}

// TestLoadMmapDedupedMissingContentErrors asserts that a shard referencing a SHA
// absent from the shared store fails loudly rather than silently dropping content
// (which would under-approximate — a SACRED parity violation).
func TestLoadMmapDedupedMissingContentErrors(t *testing.T) {
	ix := buildSampleIndex(t)
	dir := t.TempDir()
	shardPath := filepath.Join(dir, "shard-0000.idx")
	cw := NewContentStoreWriter()
	if err := SaveDeduped(ix, shardPath, cw); err != nil {
		t.Fatalf("SaveDeduped: %v", err)
	}
	// Write an EMPTY content store (drops every blob's content).
	empty := NewContentStoreWriter()
	csPath := filepath.Join(dir, ContentStoreName)
	if err := empty.Write(csPath); err != nil {
		t.Fatalf("Write empty store: %v", err)
	}
	cs, err := OpenContentStore(csPath)
	if err != nil {
		t.Fatalf("OpenContentStore: %v", err)
	}
	defer cs.Close()
	if _, region, err := LoadMmapDeduped(shardPath, cs); err == nil {
		region.Close()
		t.Fatal("LoadMmapDeduped succeeded against an empty content store; want a missing-content error")
	}
}

// TestIsDedupedRejectsLegacy asserts IsDeduped is false for a MOEDEX03 shard, so
// the serving spine keeps loading legacy dirs via the inlined-content path.
func TestIsDedupedRejectsLegacy(t *testing.T) {
	ix := buildSampleIndex(t)
	path := filepath.Join(t.TempDir(), "legacy.idx")
	if err := Save(ix, path); err != nil { // MOEDEX03
		t.Fatalf("Save: %v", err)
	}
	if IsDeduped(path) {
		t.Error("IsDeduped reported true for a MOEDEX03 shard")
	}
}
