package serve

import (
	"context"
	"path/filepath"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/index"
)

// buildShard writes a one-shard index containing the given files to dir/name.
func buildShard(t *testing.T, dir, name string, files map[string]string) {
	t.Helper()
	ix := index.New()
	for path, content := range files {
		// Use the path as both relpath and a synthetic SHA so distinct content
		// across shards stays distinct.
		ix.AddFile("repoA", path, filepath.Join("/abs", path), path, []byte(content))
	}
	if err := diskstore.Save(ix, filepath.Join(dir, name)); err != nil {
		t.Fatalf("save shard %s: %v", name, err)
	}
}

func TestCorpusMergesAcrossShards(t *testing.T) {
	dir := t.TempDir()
	buildShard(t, dir, "shard-0000.idx", map[string]string{
		"a.go": "package main\nfunc hello() {}\n",
		"b.go": "var token = 42\n",
	})
	buildShard(t, dir, "shard-0001.idx", map[string]string{
		"c.go": "func goodbye() {}\nvar token2 = 7\n",
	})

	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	if c.NumShards() != 2 {
		t.Errorf("NumShards = %d, want 2", c.NumShards())
	}
	if c.NumBlobs() != 3 {
		t.Errorf("NumBlobs = %d, want 3", c.NumBlobs())
	}

	// A literal that appears in both shards must surface from both.
	matches, _, err := c.Literal(context.Background(), "token")
	if err != nil {
		t.Fatalf("Literal: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("Literal(token): %d matches, want 2 (one per shard): %+v", len(matches), matches)
	}
	// Results are ordered deterministically by repo/relpath/line.
	if matches[0].RelPath != "b.go" || matches[1].RelPath != "c.go" {
		t.Errorf("unexpected order: %s then %s", matches[0].RelPath, matches[1].RelPath)
	}
}

func TestCorpusRegexAcrossShards(t *testing.T) {
	dir := t.TempDir()
	buildShard(t, dir, "shard-0000.idx", map[string]string{
		"a.go": "func Alpha() {}\n",
	})
	buildShard(t, dir, "shard-0001.idx", map[string]string{
		"b.go": "func Beta() {}\n",
	})

	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	matches, _, err := c.Regex(context.Background(), `func \w+\(\)`)
	if err != nil {
		t.Fatalf("Regex: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("Regex: %d matches, want 2: %+v", len(matches), matches)
	}
}

func TestCorpusBadRegexReportsError(t *testing.T) {
	dir := t.TempDir()
	buildShard(t, dir, "shard-0000.idx", map[string]string{"a.go": "hi\n"})
	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	if _, _, err := c.Regex(context.Background(), "(unterminated"); err == nil {
		t.Error("expected error for malformed regex, got nil")
	}
}

func TestOpenEmptyDirErrors(t *testing.T) {
	if _, err := Open(t.TempDir()); err == nil {
		t.Error("expected error opening dir with no shards, got nil")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	buildShard(t, dir, "shard-0000.idx", map[string]string{"a.go": "hi\n"})
	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close should be no-op: %v", err)
	}
}
