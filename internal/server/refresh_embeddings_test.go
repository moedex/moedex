package server

import (
	"context"
	"path/filepath"
	"testing"

	"moedex/internal/embed"
	"moedex/internal/rank"
)

// countingEmbedder wraps a deterministic embedder and tallies how many texts it
// actually embeds, so a test can prove an incremental refresh embeds ONLY changed
// chunks (and an up-to-date refresh embeds nothing).
type countingEmbedder struct {
	inner embed.Embedder
	calls *int
}

func (c countingEmbedder) Dim() int { return c.inner.Dim() }
func (c countingEmbedder) Embed(ctx context.Context, texts []string) ([]embed.Vector, error) {
	*c.calls += len(texts)
	return c.inner.Embed(ctx, texts)
}

func TestRefreshEmbeddings_IncrementalReusesUnchanged(t *testing.T) {
	dir := t.TempDir()
	buildShard(t, dir, "shard-0000.idx", map[string]string{
		"auth/login.go": "package auth\n\nfunc Authenticate(token string) bool { return token != \"\" }\n",
		"calc/add.go":   "package calc\n\nfunc Add(a, b int) int { return a + b }\n",
	})
	storePath := filepath.Join(dir, "corpus-embeddings.store")
	var calls int
	emb := countingEmbedder{inner: conceptEmbedder{}, calls: &calls}
	cfg := RankConfig{TopK: 5, Emb: emb, EmbedModel: "concept-v1", StorePath: storePath, LinesPerChunk: 20, Overlap: 5}

	// 1. First refresh: full build.
	s1, err := RefreshEmbeddings(context.Background(), dir, cfg)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if s1.UpToDate || s1.Migrated {
		t.Fatalf("first refresh should be a full build, got %+v", s1)
	}
	if s1.TotalChunks == 0 || s1.Reused != 0 || s1.Embedded != s1.TotalChunks {
		t.Fatalf("first refresh stats = %+v, want all embedded none reused", s1)
	}
	builtCalls := calls
	if builtCalls != s1.TotalChunks {
		t.Fatalf("embedder saw %d texts, want %d", builtCalls, s1.TotalChunks)
	}

	// 2. Unchanged corpus: nothing to embed.
	calls = 0
	s2, err := RefreshEmbeddings(context.Background(), dir, cfg)
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if !s2.UpToDate {
		t.Fatalf("unchanged corpus should be up to date, got %+v", s2)
	}
	if calls != 0 {
		t.Fatalf("up-to-date refresh embedded %d texts, want 0", calls)
	}

	// 3. Change one file, add a new one: only those re-embed; the rest reuse.
	calls = 0
	buildShard(t, dir, "shard-0000.idx", map[string]string{
		"auth/login.go": "package auth\n\nfunc Authenticate(token string) bool { return token != \"\" }\n", // unchanged
		"calc/add.go":   "package calc\n\nfunc Add(a, b, c int) int { return a + b + c }\n",                // changed
		"calc/mul.go":   "package calc\n\nfunc Mul(a, b int) int { return a * b }\n",                        // new
	})
	s3, err := RefreshEmbeddings(context.Background(), dir, cfg)
	if err != nil {
		t.Fatalf("incremental refresh: %v", err)
	}
	if s3.UpToDate || s3.Migrated {
		t.Fatalf("changed corpus should be an incremental build, got %+v", s3)
	}
	if s3.Reused == 0 {
		t.Fatal("incremental refresh reused nothing — the unchanged file should have been reused")
	}
	if s3.Embedded == 0 || s3.Embedded >= s3.TotalChunks {
		t.Fatalf("incremental refresh embedded %d/%d — expected only the changed+new chunks", s3.Embedded, s3.TotalChunks)
	}
	if calls != s3.Embedded {
		t.Fatalf("embedder saw %d texts, want %d (only the misses)", calls, s3.Embedded)
	}

	// 4. The freshly written sidecar must serve and load straight from cache (its
	// meta fingerprint matches the new shard set).
	rc, err := OpenRank(context.Background(), dir, cfg)
	if err != nil {
		t.Fatalf("OpenRank after incremental refresh: %v", err)
	}
	defer rc.Close()
	if !rc.DenseFromCache() {
		t.Error("daemon should LOAD the incrementally refreshed sidecar, not rebuild it")
	}
	if rc.DenseChunks() != s3.TotalChunks {
		t.Errorf("served %d chunks, refresh wrote %d", rc.DenseChunks(), s3.TotalChunks)
	}
}

func TestRefreshEmbeddings_ModelChangeForcesFullEmbed(t *testing.T) {
	dir := t.TempDir()
	buildShard(t, dir, "shard-0000.idx", map[string]string{
		"a/x.go": "package a\nfunc Foo() {}\n",
		"b/y.go": "package b\nfunc Bar() {}\n",
	})
	storePath := filepath.Join(dir, "corpus-embeddings.store")
	var calls int
	emb := countingEmbedder{inner: conceptEmbedder{}, calls: &calls}
	cfg := RankConfig{TopK: 5, Emb: emb, EmbedModel: "m1", StorePath: storePath, LinesPerChunk: 20, Overlap: 5, Rank: rank.Config{}}

	first, err := RefreshEmbeddings(context.Background(), dir, cfg)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}

	// Same corpus, DIFFERENT model: vectors must NOT be carried over (a model's
	// vectors are incomparable), so it is a clean full re-embed.
	calls = 0
	cfg2 := cfg
	cfg2.EmbedModel = "m2"
	second, err := RefreshEmbeddings(context.Background(), dir, cfg2)
	if err != nil {
		t.Fatalf("model-change refresh: %v", err)
	}
	if second.UpToDate || second.Reused != 0 {
		t.Fatalf("a model change must not reuse vectors, got %+v", second)
	}
	if calls != first.TotalChunks {
		t.Fatalf("model change embedded %d texts, want a full re-embed of %d", calls, first.TotalChunks)
	}
}
