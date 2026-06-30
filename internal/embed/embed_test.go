package embed

import (
	"bytes"
	"context"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"moedex/internal/index"
)

// fakeEmbedder is a deterministic, network-free Embedder for tests. It maps each
// text to a fixed-dim vector via a hashed bag-of-words: every whitespace token
// is hashed (FNV-1a) into one of dim buckets and increments that bucket. Texts
// that share tokens therefore land in overlapping buckets and score higher
// cosine similarity, which is exactly what Search relies on.
type fakeEmbedder struct{ dim int }

func newFakeEmbedder(dim int) *fakeEmbedder { return &fakeEmbedder{dim: dim} }

func (f *fakeEmbedder) Dim() int { return f.dim }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([]Vector, error) {
	out := make([]Vector, len(texts))
	for i, t := range texts {
		v := make(Vector, f.dim)
		for _, tok := range strings.Fields(strings.ToLower(t)) {
			h := fnv.New32a()
			h.Write([]byte(tok))
			v[h.Sum32()%uint32(f.dim)]++
		}
		out[i] = v
	}
	return out, nil
}

// makeBlob builds an index.Blob with the given content via the real index API.
func makeBlob(t *testing.T, content string) *index.Blob {
	t.Helper()
	ix := index.New()
	ix.AddFile("repo", "f.txt", "/abs/f.txt", "sha-"+content[:min(8, len(content))], []byte(content))
	return ix.Blob(0)
}

func TestChunkBlob_Boundaries(t *testing.T) {
	// 6 lines, each "lineN\n". Byte offsets: line k starts at (k-1)*6.
	content := "line1\nline2\nline3\nline4\nline5\nline6\n"
	b := makeBlob(t, content)

	// linesPerChunk=3, overlap=1 -> stride=2. Windows: [1-3],[3-5],[5-6].
	got := ChunkBlob(b, 3, 1)
	want := []Chunk{
		{Blob: 0, StartLine: 1, EndLine: 3, StartByte: 0, EndByte: 18},
		{Blob: 0, StartLine: 3, EndLine: 5, StartByte: 12, EndByte: 30},
		{Blob: 0, StartLine: 5, EndLine: 6, StartByte: 24, EndByte: 36},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("chunks mismatch\n got: %+v\nwant: %+v", got, want)
	}

	// Verify the byte spans actually slice the expected text.
	if s := got[0].Text(b); s != "line1\nline2\nline3\n" {
		t.Errorf("chunk0 text = %q", s)
	}
	if s := got[2].Text(b); s != "line5\nline6\n" {
		t.Errorf("chunk2 text = %q", s)
	}
}

func TestChunkBlob_NoOverlap(t *testing.T) {
	content := "a\nb\nc\nd\n" // 4 lines, each 2 bytes
	b := makeBlob(t, content)
	got := ChunkBlob(b, 2, 0) // stride 2 -> [1-2],[3-4]
	want := []Chunk{
		{Blob: 0, StartLine: 1, EndLine: 2, StartByte: 0, EndByte: 4},
		{Blob: 0, StartLine: 3, EndLine: 4, StartByte: 4, EndByte: 8},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("chunks mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

func TestChunkBlob_ShorterThanChunk(t *testing.T) {
	content := "only\ntwo\n"
	b := makeBlob(t, content)
	got := ChunkBlob(b, 10, 2)
	want := []Chunk{{Blob: 0, StartLine: 1, EndLine: 2, StartByte: 0, EndByte: 9}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("chunks mismatch\n got: %+v\nwant: %+v", got, want)
	}
	if got[0].Text(b) != content {
		t.Errorf("text = %q", got[0].Text(b))
	}
}

func TestChunkBlob_NoTrailingNewline(t *testing.T) {
	content := "x\ny\nz" // 3 lines, last has no newline
	b := makeBlob(t, content)
	got := ChunkBlob(b, 2, 0) // [1-2],[3-3]
	want := []Chunk{
		{Blob: 0, StartLine: 1, EndLine: 2, StartByte: 0, EndByte: 4},
		{Blob: 0, StartLine: 3, EndLine: 3, StartByte: 4, EndByte: 5},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("chunks mismatch\n got: %+v\nwant: %+v", got, want)
	}
	if got[1].Text(b) != "z" {
		t.Errorf("last text = %q", got[1].Text(b))
	}
}

func TestChunkBlob_Empty(t *testing.T) {
	ix := index.New()
	ix.AddFile("r", "e.txt", "/e.txt", "empty", []byte{})
	if got := ChunkBlob(ix.Blob(0), 3, 1); got != nil {
		t.Fatalf("expected nil for empty content, got %+v", got)
	}
}

func TestChunkBlob_OverlapGEChunk_ClampsStride(t *testing.T) {
	// overlap >= linesPerChunk would make stride <= 0; we clamp to 1 and make
	// forward progress, never an infinite loop / duplicate windows.
	content := "a\nb\nc\nd\n"
	b := makeBlob(t, content)
	got := ChunkBlob(b, 2, 5) // stride clamps to 1 -> [1-2],[2-3],[3-4]
	want := []Chunk{
		{Blob: 0, StartLine: 1, EndLine: 2, StartByte: 0, EndByte: 4},
		{Blob: 0, StartLine: 2, EndLine: 3, StartByte: 2, EndByte: 6},
		{Blob: 0, StartLine: 3, EndLine: 4, StartByte: 4, EndByte: 8},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("chunks mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

func TestChunkBlob_NonPositiveLines_WholeBlob(t *testing.T) {
	content := "a\nb\nc\n"
	b := makeBlob(t, content)
	got := ChunkBlob(b, 0, 0)
	want := []Chunk{{Blob: 0, StartLine: 1, EndLine: 3, StartByte: 0, EndByte: 6}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("chunks mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

func buildTestIndex(t *testing.T) *index.Index {
	t.Helper()
	ix := index.New()
	// Blob 0: about cats. Blob 1: about databases.
	ix.AddFile("r", "cats.txt", "/cats.txt", "sha-cats",
		[]byte("the cat sat on the mat\nthe cat likes warm milk\n"))
	ix.AddFile("r", "db.txt", "/db.txt", "sha-db",
		[]byte("postgres index btree scan\nquery planner cost estimate\n"))
	return ix
}

func TestBuildStoreAndSearch_Ranking(t *testing.T) {
	ctx := context.Background()
	fe := newFakeEmbedder(64)
	ix := buildTestIndex(t)

	store, err := BuildStore(ctx, ix, fe, 1, 0) // one chunk per line
	if err != nil {
		t.Fatalf("BuildStore: %v", err)
	}
	if store.Len() == 0 {
		t.Fatal("store is empty")
	}
	if store.Dim() != 64 {
		t.Fatalf("dim = %d, want 64", store.Dim())
	}

	hits, err := store.Search(ctx, fe, "cat warm milk", 4)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}

	// Top hit must come from the cats blob (id 0), not the db blob.
	if hits[0].Chunk.Blob != 0 {
		t.Errorf("top hit blob = %d, want 0 (cats)", hits[0].Chunk.Blob)
	}
	// Scores must be descending.
	for i := 1; i < len(hits); i++ {
		if hits[i].Score > hits[i-1].Score {
			t.Errorf("scores not descending: %v", hits)
		}
	}
	// The "milk" line should outrank any db line.
	if hits[0].Score <= 0 {
		t.Errorf("top score should be positive, got %f", hits[0].Score)
	}
}

func TestSearch_TopKRespected(t *testing.T) {
	ctx := context.Background()
	fe := newFakeEmbedder(32)
	ix := buildTestIndex(t)
	store, err := BuildStore(ctx, ix, fe, 1, 0)
	if err != nil {
		t.Fatalf("BuildStore: %v", err)
	}
	hits, err := store.Search(ctx, fe, "cat", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("len(hits) = %d, want 2", len(hits))
	}
}

func TestSearch_TopKZeroAndEmpty(t *testing.T) {
	ctx := context.Background()
	fe := newFakeEmbedder(16)
	ix := buildTestIndex(t)
	store, _ := BuildStore(ctx, ix, fe, 1, 0)

	if h, _ := store.Search(ctx, fe, "cat", 0); h != nil {
		t.Errorf("topK=0 should return nil, got %v", h)
	}
	empty := &Store{dim: 16}
	if h, _ := empty.Search(ctx, fe, "cat", 5); h != nil {
		t.Errorf("empty store should return nil, got %v", h)
	}
}

// TestSearch_MixedDimStore_ReturnsError guards against a corrupt/mixed-dim
// store (e.g. an incremental rebuild that reused a vector from a store built
// with a different embedder) silently scoring every chunk 0 via dot's
// length-mismatch guard. Search must surface a clear error instead.
func TestSearch_MixedDimStore_ReturnsError(t *testing.T) {
	ctx := context.Background()
	fe := newFakeEmbedder(3)
	store := &Store{
		dim:    3,
		chunks: []Chunk{{Blob: 0}, {Blob: 1}},
		vectors: []Vector{
			{1, 0, 0},
			{1, 0}, // wrong dim
		},
		keys: []ChunkKey{{}, {}},
	}
	hits, err := store.Search(ctx, fe, "cat", 2)
	if err == nil {
		t.Fatalf("Search on mixed-dim store: want error, got hits %v", hits)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	ctx := context.Background()
	fe := newFakeEmbedder(48)
	ix := buildTestIndex(t)
	store, err := BuildStore(ctx, ix, fe, 1, 0)
	if err != nil {
		t.Fatalf("BuildStore: %v", err)
	}

	before, err := store.Search(ctx, fe, "query planner cost", 5)
	if err != nil {
		t.Fatalf("Search before: %v", err)
	}

	path := filepath.Join(t.TempDir(), "store.bin")
	if err := store.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if loaded.Dim() != store.Dim() {
		t.Errorf("dim mismatch: %d vs %d", loaded.Dim(), store.Dim())
	}
	if loaded.Len() != store.Len() {
		t.Errorf("len mismatch: %d vs %d", loaded.Len(), store.Len())
	}

	after, err := loaded.Search(ctx, fe, "query planner cost", 5)
	if err != nil {
		t.Fatalf("Search after: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("hits differ after round-trip\nbefore: %+v\nafter:  %+v", before, after)
	}
}

func TestHTTPEmbedder_ParsesInOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("path = %q, want /embeddings", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		// Two input texts -> two distinct vectors, order matters.
		w.Write([]byte(`{"data":[{"embedding":[1.0,0.0,0.0]},{"embedding":[0.0,2.0,0.0]}]}`))
	}))
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "test-model")
	vecs, err := e.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("got %d vectors", len(vecs))
	}
	if !reflect.DeepEqual([]float32(vecs[0]), []float32{1, 0, 0}) {
		t.Errorf("vec0 = %v", vecs[0])
	}
	if !reflect.DeepEqual([]float32(vecs[1]), []float32{0, 2, 0}) {
		t.Errorf("vec1 = %v", vecs[1])
	}
	if e.Dim() != 3 {
		t.Errorf("Dim() = %d, want 3 (cached)", e.Dim())
	}
}

// TestHTTPEmbedder_DimRaceSafe is the regression for F-083: HTTPEmbedder.dim is
// written at the end of every Embed call and read by both Dim() and Embed's own
// maxResponseBytes. Concurrent Embed/Dim calls on a shared embedder (e.g. a
// future caller that parallelizes embedding) must not race. Run with -race.
func TestHTTPEmbedder_DimRaceSafe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"embedding":[1.0,2.0,3.0]}]}`))
	}))
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "test-model")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := e.Embed(context.Background(), []string{"text"}); err != nil {
				t.Errorf("Embed: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			_ = e.Dim()
		}()
	}
	wg.Wait()
}

func TestHTTPEmbedder_Non200IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "missing")
	_, err := e.Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("expected error on non-200, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention status: %v", err)
	}
}

func TestNewHTTPEmbedder_SetsDefaultTimeout(t *testing.T) {
	e := NewHTTPEmbedder("http://127.0.0.1:0", "model")
	if e.client.Timeout <= 0 {
		t.Fatalf("client.Timeout = %v, want a positive default so a hung embedding server can't stall a build forever", e.client.Timeout)
	}
}

func TestHTTPEmbedder_Embed_BoundedByClientTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"embedding":[1,0,0]}]}`))
	}))
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "test-model")
	e.client.Timeout = 80 * time.Millisecond // far below the server's 400ms response

	start := time.Now()
	_, err := e.Embed(context.Background(), []string{"x"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error from a server slower than the client timeout, got nil")
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("Embed took %v to return an error, want bounded by the 80ms client timeout (server takes 400ms)", elapsed)
	}
}

func TestHTTPEmbedder_Embed_BoundsResponseSize(t *testing.T) {
	const oversized = 10 << 20 // 10 MiB: far above any sane single-text embedding response
	filler := bytes.Repeat([]byte("x"), oversized)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(filler)
	}))
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "test-model")

	_, err := e.Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("expected an error for an oversized response, got nil")
	}
	if !strings.Contains(err.Error(), "too large") && !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should explain the response was rejected for size, got: %v", err)
	}
}
