// Package embed is moedex's local dense-retrieval arm: it chunks blobs, turns
// chunks into vectors via a local embedding server, stores them, and does
// brute-force cosine similarity search.
//
// Design decisions (locked):
//   - moedex stays pure-stdlib: embeddings come from a local HTTP server
//     (ollama / llama.cpp-server / any OpenAI-style /embeddings endpoint) behind
//     the Embedder interface. A fake Embedder backs the tests so they need no
//     network or model.
//   - the embedded unit is a fixed-size overlapping line-window of a blob
//     (Chunk), giving finer recall than one-vector-per-file and aligning with
//     context-block assembly.
//   - the Store is flat (brute-force cosine). At single-node corpus scale this
//     is fine; an ANN index is a later optimization behind the same Search API.
//
// CONTRACT FREEZE (Lane B implements behind these signatures; internal/rank's
// dense arm is written against them — do not change their shape):
//
//	type Vector []float32
//	type Embedder interface { Embed(ctx, texts) ([]Vector, error); Dim() int }
//	func NewHTTPEmbedder(baseURL, model string) *HTTPEmbedder
//	type Chunk struct { Blob uint64; StartLine, EndLine, StartByte, EndByte int }
//	func ChunkBlob(b *index.Blob, linesPerChunk, overlap int) []Chunk
//	func BuildStore(ctx, ix, e, linesPerChunk, overlap) (*Store, error)
//	type Hit struct { Chunk Chunk; Score float32 }
//	(*Store) Search(ctx, e, query, topK) ([]Hit, error)
//	(*Store) Save(path) error
//	func LoadStore(path) (*Store, error)
package embed

import (
	"bytes"
	"container/heap"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"

	"moedex/internal/index"
)

// Vector is a dense embedding.
type Vector []float32

// Embedder turns text into vectors. Implementations: HTTPEmbedder (a local
// embedding server) and an unexported fake used by tests.
type Embedder interface {
	// Embed returns one vector per input text, in order.
	Embed(ctx context.Context, texts []string) ([]Vector, error)
	// Dim is the embedding dimensionality.
	Dim() int
}

// HTTPEmbedder talks to a local OpenAI/ollama-style embeddings endpoint.
//
// Embed issues POST {baseURL}/embeddings with a JSON body
//
//	{"model": <model>, "input": [<texts...>]}
//
// and parses the OpenAI-style response
//
//	{"data": [{"embedding": [...]}, ...]}
//
// preserving order. A non-2xx status is returned as an error. The embedding
// dimension is discovered from the first successful response and cached.
type HTTPEmbedder struct {
	baseURL string
	model   string
	client  *http.Client
	dim     int
}

// NewHTTPEmbedder returns an Embedder backed by the embeddings endpoint at
// baseURL using the named model.
func NewHTTPEmbedder(baseURL, model string) *HTTPEmbedder {
	return &HTTPEmbedder{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{},
	}
}

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed implements Embedder.
func (h *HTTPEmbedder) Embed(ctx context.Context, texts []string) ([]Vector, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(embedRequest{Model: h.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("embed: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("embed: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embed: server returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}

	var parsed embedResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("embed: decode response: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("embed: expected %d vectors, got %d", len(texts), len(parsed.Data))
	}

	out := make([]Vector, len(parsed.Data))
	for i, d := range parsed.Data {
		out[i] = Vector(d.Embedding)
	}
	if len(out[0]) > 0 {
		h.dim = len(out[0])
	}
	return out, nil
}

// Dim implements Embedder. It returns the cached embedding dimension discovered
// from the first successful Embed call, or 0 if not yet known.
func (h *HTTPEmbedder) Dim() int { return h.dim }

// Chunk is a fixed-size line-window of a blob that gets one vector. Lines are
// 1-based inclusive; byte offsets bound the same span in Blob.Content.
type Chunk struct {
	Blob      uint64
	StartLine int
	EndLine   int
	StartByte int
	EndByte   int
}

// Text returns the chunk's slice of the blob's content.
func (c Chunk) Text(b *index.Blob) string {
	return string(b.Content[c.StartByte:c.EndByte])
}

// ChunkBlob splits a blob's content into overlapping windows of linesPerChunk
// lines, advancing by (linesPerChunk - overlap) lines each step.
//
// Edge behavior:
//   - empty content -> no chunks.
//   - content shorter than linesPerChunk -> a single chunk covering all lines.
//   - the final window is clamped to the last line (partial chunks allowed).
//   - if linesPerChunk <= 0 the whole blob is one chunk.
//   - if overlap >= linesPerChunk the stride would be <= 0; we clamp the stride
//     to a minimum of 1 line per step to guarantee forward progress (otherwise
//     chunking would never terminate). Duplicate windows are suppressed.
func ChunkBlob(b *index.Blob, linesPerChunk, overlap int) []Chunk {
	content := b.Content
	if len(content) == 0 {
		return nil
	}

	// Byte offset at which each 1-based line begins. lineStart[i] is the byte
	// offset of line (i+1); we also track total length as the end sentinel.
	var lineStarts []int
	lineStarts = append(lineStarts, 0)
	for i, c := range content {
		if c == '\n' {
			lineStarts = append(lineStarts, i+1)
		}
	}
	// numLines counts lines. A trailing newline does NOT create a phantom empty
	// final line for chunking purposes: lineStarts may have a sentinel start at
	// len(content) which we treat as the end boundary, not a line.
	numLines := len(lineStarts)
	if lineStarts[numLines-1] == len(content) && numLines > 1 {
		// Trailing newline: last start points past content; not a real line.
		numLines--
	}

	if linesPerChunk <= 0 {
		// Treat as "one chunk = whole blob".
		return []Chunk{wholeBlobChunk(b, numLines)}
	}

	stride := linesPerChunk - overlap
	if stride < 1 {
		stride = 1
	}

	// byteOf returns the byte offset where 1-based line n begins.
	byteOf := func(line1Based int) int {
		idx := line1Based - 1
		if idx >= len(lineStarts) {
			return len(content)
		}
		return lineStarts[idx]
	}

	var chunks []Chunk
	var lastStart, lastEnd int = -1, -1
	for start := 1; start <= numLines; start += stride {
		end := start + linesPerChunk - 1
		if end > numLines {
			end = numLines
		}
		startByte := byteOf(start)
		// End byte is the start of the line after `end`, i.e. inclusive of the
		// whole last line and its newline. For the final line clamp to len.
		var endByte int
		if end+1 <= numLines {
			endByte = byteOf(end + 1)
		} else {
			endByte = len(content)
		}

		// Suppress an identical trailing window (can happen at the tail when the
		// clamp pins start/end to the same span as the previous step).
		if start == lastStart && end == lastEnd {
			break
		}
		chunks = append(chunks, Chunk{
			Blob:      b.ID,
			StartLine: start,
			EndLine:   end,
			StartByte: startByte,
			EndByte:   endByte,
		})
		lastStart, lastEnd = start, end

		if end >= numLines {
			break
		}
	}
	return chunks
}

func wholeBlobChunk(b *index.Blob, numLines int) Chunk {
	return Chunk{
		Blob:      b.ID,
		StartLine: 1,
		EndLine:   numLines,
		StartByte: 0,
		EndByte:   len(b.Content),
	}
}

// Store is a flat (brute-force cosine) vector store over blob chunks. Vectors
// are stored L2-normalized so Search reduces to a dot product.
type Store struct {
	dim     int
	chunks  []Chunk
	vectors []Vector // parallel to chunks; each is unit-normalized
}

// Dim returns the stored embedding dimension.
func (s *Store) Dim() int { return s.dim }

// Len returns the number of stored chunks.
func (s *Store) Len() int { return len(s.chunks) }

const buildBatchSize = 64

// BuildStore chunks every blob in ix, embeds the chunks via e, and returns a
// populated Store. It batches calls to e.Embed.
func BuildStore(ctx context.Context, ix *index.Index, e Embedder, linesPerChunk, overlap int) (*Store, error) {
	var chunks []Chunk
	var texts []string
	n := ix.NumBlobs()
	for id := 0; id < n; id++ {
		b := ix.Blob(uint64(id))
		for _, c := range ChunkBlob(b, linesPerChunk, overlap) {
			chunks = append(chunks, c)
			texts = append(texts, c.Text(b))
		}
	}

	s := &Store{}
	if len(chunks) == 0 {
		s.dim = e.Dim()
		return s, nil
	}

	s.chunks = chunks
	s.vectors = make([]Vector, len(chunks))

	for start := 0; start < len(texts); start += buildBatchSize {
		end := start + buildBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := e.Embed(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		if len(vecs) != end-start {
			return nil, fmt.Errorf("embed: batch returned %d vectors for %d texts", len(vecs), end-start)
		}
		for i, v := range vecs {
			if s.dim == 0 {
				s.dim = len(v)
			} else if len(v) != s.dim {
				return nil, fmt.Errorf("embed: inconsistent vector dim %d (want %d)", len(v), s.dim)
			}
			s.vectors[start+i] = normalize(v)
		}
	}
	return s, nil
}

// Hit is a similarity result: a chunk and its cosine score against the query.
type Hit struct {
	Chunk Chunk
	Score float32
}

// Search embeds query via e and returns the topK most similar chunks, best
// first. Ties break stably by chunk index (lower index first).
func (s *Store) Search(ctx context.Context, e Embedder, query string, topK int) ([]Hit, error) {
	if topK <= 0 || len(s.chunks) == 0 {
		return nil, nil
	}
	vecs, err := e.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("embed: query embedding returned %d vectors", len(vecs))
	}
	q := normalize(vecs[0])
	if s.dim != 0 && len(q) != s.dim {
		return nil, fmt.Errorf("embed: query dim %d != store dim %d", len(q), s.dim)
	}

	// Score every chunk by cosine (vectors are unit-normalized, so dot == cosine).
	// The scan is O(chunks*dim) and dominates query latency at corpus scale, so the
	// dot products run in parallel across GOMAXPROCS shards (disjoint index ranges,
	// no synchronization needed — each goroutine writes its own slice region).
	n := len(s.vectors)
	scores := make([]float32, n)
	workers := runtime.GOMAXPROCS(0)
	if workers > n {
		workers = n
	}
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	step := (n + workers - 1) / workers
	for w := 0; w < workers; w++ {
		lo := w * step
		if lo >= n {
			break
		}
		hi := lo + step
		if hi > n {
			hi = n
		}
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				scores[i] = dot(q, s.vectors[i])
			}
		}(lo, hi)
	}
	wg.Wait()

	if topK > n {
		topK = n
	}
	// Bounded top-K selection: a min-heap of size topK whose root is the weakest kept
	// element (lowest score; higher index breaks ties). A candidate that betterThan the
	// root replaces it. This is O(n log topK) instead of sorting all n, and yields the
	// exact same top-K (and order) as a stable score-desc/index-asc sort.
	h := make(scoreHeap, 0, topK)
	for i := 0; i < n; i++ {
		sc := scored{idx: i, score: scores[i]}
		if len(h) < topK {
			heap.Push(&h, sc)
		} else if betterThan(sc, h[0]) {
			h[0] = sc
			heap.Fix(&h, 0)
		}
	}
	out := []scored(h)
	sort.Slice(out, func(a, b int) bool { return betterThan(out[a], out[b]) })
	hits := make([]Hit, len(out))
	for i, sc := range out {
		hits[i] = Hit{Chunk: s.chunks[sc.idx], Score: sc.score}
	}
	return hits, nil
}

// scored is one chunk's cosine score, kept with its index for tie-breaking.
type scored struct {
	idx   int
	score float32
}

// betterThan reports whether a outranks b in the final result: higher score first,
// ties broken by lower index (matching the prior stable sort).
func betterThan(a, b scored) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	return a.idx < b.idx
}

// scoreHeap is a min-heap ordered "weakest first" (the inverse of betterThan), so
// its root is the most evictable kept element for bounded top-K selection.
type scoreHeap []scored

func (h scoreHeap) Len() int           { return len(h) }
func (h scoreHeap) Less(i, j int) bool { return betterThan(h[j], h[i]) }
func (h scoreHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *scoreHeap) Push(x any)        { *h = append(*h, x.(scored)) }
func (h *scoreHeap) Pop() any {
	old := *h
	k := len(old)
	x := old[k-1]
	*h = old[:k-1]
	return x
}

// toTokenizerSafeText coerces s to valid UTF-8, replacing each run of invalid
// bytes with a single space. The forked byte-level tokenizer used by the
// in-process ONNX embedder PANICS on invalid UTF-8: Go's range over a string
// expands every invalid byte to a 3-byte U+FFFD, and the tokenizer's alignment
// bookkeeping indexes out of range on that expansion (normalizer.TransformRange).
// Legacy Windows-1252 ColdFusion carries such bytes (smart quote 0x92, nbsp 0xA0,
// en-dash 0x96). VALID multi-byte UTF-8 (accents, em-dash, emoji) tokenizes fine
// and is left untouched, so this only normalizes genuinely undecodable bytes — no
// loss for legitimate Unicode. A space (not "") preserves token boundaries.
func toTokenizerSafeText(s string) string {
	return strings.ToValidUTF8(s, " ")
}

func normalize(v Vector) Vector {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return append(Vector(nil), v...)
	}
	inv := float32(1.0 / math.Sqrt(sum))
	out := make(Vector, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

func dot(a, b Vector) float32 {
	if len(a) != len(b) {
		return 0
	}
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}
