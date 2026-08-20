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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

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

	dimMu sync.Mutex
	dim   int // guards concurrent Embed/Dim calls on a shared embedder
}

// defaultEmbedTimeout bounds a single embeddings HTTP request (covers both the
// build path, which has no per-call context deadline, and as a backstop on the
// context-bounded query path) so a hung or slow embedding server can't stall
// indefinitely.
const defaultEmbedTimeout = 60 * time.Second

// NewHTTPEmbedder returns an Embedder backed by the embeddings endpoint at
// baseURL using the named model.
func NewHTTPEmbedder(baseURL, model string) *HTTPEmbedder {
	return &HTTPEmbedder{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{Timeout: defaultEmbedTimeout},
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

	limit := h.maxResponseBytes(len(texts))
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("embed: read response: %w", err)
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("embed: response exceeds %d byte limit for %d text(s)", limit, len(texts))
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
		h.dimMu.Lock()
		h.dim = len(out[0])
		h.dimMu.Unlock()
	}
	return out, nil
}

// Dim implements Embedder. It returns the cached embedding dimension discovered
// from the first successful Embed call, or 0 if not yet known.
func (h *HTTPEmbedder) Dim() int {
	h.dimMu.Lock()
	defer h.dimMu.Unlock()
	return h.dim
}

const (
	// assumedMaxDim bounds the per-vector size estimate before the real
	// embedding dimension is known (Dim() is 0 until the first successful
	// Embed call).
	assumedMaxDim = 4096
	// bytesPerFloatJSON generously bounds how many bytes one JSON-encoded
	// float32 array element occupies (sign, digits, decimal point, comma).
	bytesPerFloatJSON = 24
	// responseSafetyFactor leaves headroom over the raw vector payload for
	// JSON structure (keys, brackets, any extra metadata field).
	responseSafetyFactor = 4
	// minResponseBytes floors the cap so small batches aren't pinned to an
	// unreasonably tight limit by JSON overhead.
	minResponseBytes = 1 << 20 // 1 MiB
)

// maxResponseBytes bounds how many bytes Embed will read for a response
// covering n texts, so a hung, misbehaving, or compromised embedding server
// can't stream unbounded data into memory.
func (h *HTTPEmbedder) maxResponseBytes(n int) int64 {
	dim := h.Dim()
	if dim == 0 {
		dim = assumedMaxDim
	}
	limit := int64(n) * int64(dim) * bytesPerFloatJSON * responseSafetyFactor
	if limit < minResponseBytes {
		return minResponseBytes
	}
	return limit
}

// Chunk is a fixed-size line-window of a blob that gets one vector. Lines are
// 1-based inclusive; byte offsets bound the same span in Blob.Content.
type Chunk struct {
	Blob      uint64
	StartLine int
	EndLine   int
	StartByte int
	EndByte   int
}

// ChunkKey is a content-stable identity for a chunk's text: the first 16 bytes of
// sha256(text). Two chunks with identical text always share a key (and therefore an
// embedding), regardless of which blob or position they occupy — so a refreshed
// corpus can REUSE the vector of every unchanged chunk and embed only genuinely new
// text. 128 bits makes a collision across a corpus of a few million chunks
// effectively impossible. The blob-positional Chunk.Blob ID is NOT stable across
// refreshes (it is reassigned by shard concatenation order); this key is.
type ChunkKey [16]byte

// chunkKey returns the content key for a chunk's raw text bytes. It hashes the raw
// bytes (not the tokenizer-safe coercion) so the key is a pure function of the
// stored content: identical content -> identical key -> identical embedding; any
// byte difference -> a fresh embed. Hashing the whole corpus is memory-bandwidth
// bound (~seconds), negligible beside even a 1% re-embed.
func chunkKey(text []byte) ChunkKey {
	sum := sha256.Sum256(text)
	var k ChunkKey
	copy(k[:], sum[:])
	return k
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
	vectors []Vector   // parallel to chunks; each is unit-normalized
	keys    []ChunkKey // parallel to chunks; content keys for incremental reuse (nil for a legacy v1 store)
}

// Dim returns the stored embedding dimension.
func (s *Store) Dim() int { return s.dim }

// Len returns the number of stored chunks.
func (s *Store) Len() int { return len(s.chunks) }

// HasKeys reports whether the store carries a content key per chunk (format v2).
// A legacy v1 store loaded from disk has none, so it cannot seed incremental reuse
// and cannot be re-saved until its keys are filled (see FillKeys).
func (s *Store) HasKeys() bool { return len(s.keys) == len(s.chunks) && len(s.chunks) > 0 }

// KeyVectors returns a content-key -> unit-vector map for seeding an incremental
// rebuild's reuse set. Returns nil if the store carries no keys (legacy v1). When a
// key repeats (identical text in several places) the first vector wins; they are by
// construction equal, so the choice is immaterial. The returned vectors ALIAS the
// store's slices (read-only by Search), so reuse costs no extra vector memory.
func (s *Store) KeyVectors() map[ChunkKey]Vector {
	if !s.HasKeys() {
		return nil
	}
	m := make(map[ChunkKey]Vector, len(s.keys))
	for i, k := range s.keys {
		if _, ok := m[k]; !ok {
			m[k] = s.vectors[i]
		}
	}
	return m
}

// FillKeys recomputes the content key for every chunk from ix and attaches them,
// upgrading a legacy keyless store to v2 WITHOUT re-embedding. It is valid only
// when the store's chunks index into ix — i.e. ix is the SAME corpus the store was
// built over (the caller proves this via a matching corpus fingerprint). Any
// out-of-range chunk means the store does not match ix and is reported as an error
// rather than silently producing wrong keys.
func (s *Store) FillKeys(ix *index.Index) error {
	n := uint64(ix.NumBlobs())
	keys := make([]ChunkKey, len(s.chunks))
	for i, c := range s.chunks {
		if c.Blob >= n {
			return fmt.Errorf("embed: fill keys: chunk %d references blob %d out of range (%d blobs)", i, c.Blob, n)
		}
		b := ix.Blob(c.Blob)
		if b == nil {
			return fmt.Errorf("embed: fill keys: chunk %d references nil blob %d", i, c.Blob)
		}
		if c.StartByte < 0 || c.EndByte > len(b.Content) || c.StartByte > c.EndByte {
			return fmt.Errorf("embed: fill keys: chunk %d span [%d,%d) out of range for blob %d (len %d)", i, c.StartByte, c.EndByte, c.Blob, len(b.Content))
		}
		keys[i] = chunkKey(b.Content[c.StartByte:c.EndByte])
	}
	s.keys = keys
	return nil
}

const buildBatchSize = 64

// BuildStats reports how an (incremental) build sourced its chunk vectors.
type BuildStats struct {
	Total    int // chunks in the resulting store
	Reused   int // chunks whose vector was reused from the prior store (no embed)
	Embedded int // DISTINCT new texts actually sent to the embedder
}

// BuildProgress reports completed distinct embedding inputs. Total excludes
// reused chunks and duplicate texts, matching BuildStats.Embedded.
type BuildProgress struct {
	Embedded int
	Total    int
}

// BuildStore chunks every blob in ix, embeds the chunks via e, and returns a
// populated Store. It batches calls to e.Embed. (Contract-frozen signature; it is
// exactly BuildStoreIncremental with no reuse — a full embed.)
func BuildStore(ctx context.Context, ix *index.Index, e Embedder, linesPerChunk, overlap int) (*Store, error) {
	s, _, err := BuildStoreIncremental(ctx, ix, e, linesPerChunk, overlap, nil)
	return s, err
}

// BuildStoreIncremental builds a Store over ix, REUSING vectors from `reuse` (a
// content-key -> unit-vector map, e.g. from a prior Store.KeyVectors()) for every
// chunk whose text is unchanged, and embedding only the rest. Within a single build
// it also de-duplicates identical new texts so each distinct new text is embedded
// once. The result is byte-identical to a full BuildStore for every reused chunk —
// the embedder is deterministic for identical text — so ranking is unchanged; a
// nil/empty reuse map degrades cleanly to a full embed.
//
// The returned store always carries content keys (format v2), so it can in turn
// seed the NEXT incremental build.
func BuildStoreIncremental(ctx context.Context, ix *index.Index, e Embedder, linesPerChunk, overlap int, reuse map[ChunkKey]Vector) (*Store, BuildStats, error) {
	return BuildStoreIncrementalWithProgress(ctx, ix, e, linesPerChunk, overlap, reuse, nil)
}

// BuildStoreIncrementalWithProgress is BuildStoreIncremental with a callback at
// preparation completion and after each inference batch. The callback runs
// synchronously and must return promptly.
func BuildStoreIncrementalWithProgress(ctx context.Context, ix *index.Index, e Embedder, linesPerChunk, overlap int, reuse map[ChunkKey]Vector, progress func(BuildProgress)) (*Store, BuildStats, error) {
	var (
		chunks  []Chunk
		keys    []ChunkKey
		vectors []Vector
		stats   BuildStats

		// Distinct new texts to embed, and the slot each maps to. keySlot lets a
		// repeated new text resolve to one embed; it is also how reused-vs-new is
		// scattered back after embedding.
		embedTexts []string
		keySlot    = map[ChunkKey]int{}
	)

	n := ix.NumBlobs()
	for id := 0; id < n; id++ {
		b := ix.Blob(uint64(id))
		if b == nil {
			continue
		}
		for _, c := range ChunkBlob(b, linesPerChunk, overlap) {
			k := chunkKey(b.Content[c.StartByte:c.EndByte])
			chunks = append(chunks, c)
			keys = append(keys, k)
			vectors = append(vectors, nil)
			if _, ok := reuse[k]; ok {
				continue // reused — vector filled in the scatter pass
			}
			if _, ok := keySlot[k]; !ok {
				keySlot[k] = len(embedTexts)
				embedTexts = append(embedTexts, string(b.Content[c.StartByte:c.EndByte]))
			}
		}
	}

	s := &Store{}
	stats.Total = len(chunks)
	if len(chunks) == 0 {
		s.dim = e.Dim()
		return s, stats, nil
	}

	// Embed the distinct misses in batches.
	embedVecs := make([]Vector, len(embedTexts))
	if progress != nil {
		progress(BuildProgress{Total: len(embedTexts)})
	}
	for start := 0; start < len(embedTexts); start += buildBatchSize {
		end := start + buildBatchSize
		if end > len(embedTexts) {
			end = len(embedTexts)
		}
		vecs, err := e.Embed(ctx, embedTexts[start:end])
		if err != nil {
			return nil, stats, err
		}
		if len(vecs) != end-start {
			return nil, stats, fmt.Errorf("embed: batch returned %d vectors for %d texts", len(vecs), end-start)
		}
		for i, v := range vecs {
			if s.dim == 0 {
				s.dim = len(v)
			} else if len(v) != s.dim {
				return nil, stats, fmt.Errorf("embed: inconsistent vector dim %d (want %d)", len(v), s.dim)
			}
			embedVecs[start+i] = normalize(v)
		}
		if progress != nil {
			progress(BuildProgress{Embedded: end, Total: len(embedTexts)})
		}
	}
	// A reuse-only build (every chunk reused) embeds nothing; take the dim from the
	// reused vectors so the store still reports a correct dimension.
	if s.dim == 0 {
		for _, v := range reuse {
			s.dim = len(v)
			break
		}
	}

	// Scatter: each chunk's vector is either reused or its distinct embedded text.
	for i, k := range keys {
		if v, ok := reuse[k]; ok {
			vectors[i] = v
			stats.Reused++
			continue
		}
		vectors[i] = embedVecs[keySlot[k]]
	}
	stats.Embedded = len(embedTexts)

	s.chunks = chunks
	s.vectors = vectors
	s.keys = keys
	return s, stats, nil
}

// Hit is a similarity result: a chunk and its cosine score against the query.
type Hit struct {
	Chunk Chunk
	Score float32
}

// Similarity is one directed nearest-neighbor relationship between two stored
// chunks. Score is their cosine similarity. Source and Target never identify
// the same stored chunk.
type Similarity struct {
	Source Chunk
	Target Chunk
	Score  float32
}

// Similar returns, for every stored chunk, its topK nearest OTHER chunks whose
// cosine similarity is at least threshold. Results are grouped in store order
// by source; each source's neighbors are ordered by score descending, with
// store order breaking ties. The store's vectors are already unit-normalized,
// so the corpus-wide comparison requires no additional embedding calls.
//
// This is an exact brute-force all-neighbors pass. It is intended for offline
// builders (such as the graph), not request-time search.
func (s *Store) Similar(ctx context.Context, topK int, threshold float32) ([]Similarity, error) {
	if s == nil || topK <= 0 || len(s.chunks) < 2 {
		return nil, nil
	}
	if math.IsNaN(float64(threshold)) || threshold < -1 || threshold > 1 {
		return nil, fmt.Errorf("embed: similarity threshold %g is outside [-1,1]", threshold)
	}
	if len(s.vectors) != len(s.chunks) {
		return nil, fmt.Errorf("embed: %d chunks have %d vectors", len(s.chunks), len(s.vectors))
	}
	for i, v := range s.vectors {
		if len(v) != s.dim {
			return nil, fmt.Errorf("embed: chunk %d vector dim %d != store dim %d", i, len(v), s.dim)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if topK >= len(s.chunks) {
		topK = len(s.chunks) - 1
	}
	perSource := make([][]Similarity, len(s.chunks))
	workers := runtime.GOMAXPROCS(0)
	if workers > len(s.chunks) {
		workers = len(s.chunks)
	}
	if workers < 1 {
		workers = 1
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for source := range jobs {
				if ctx.Err() != nil {
					continue
				}
				h := make(scoreHeap, 0, topK)
				for target, vector := range s.vectors {
					if source == target {
						continue
					}
					if target&255 == 0 && ctx.Err() != nil {
						break
					}
					score := dot(s.vectors[source], vector)
					if score < threshold {
						continue
					}
					candidate := scored{idx: target, score: score}
					if len(h) < topK {
						heap.Push(&h, candidate)
					} else if betterThan(candidate, h[0]) {
						h[0] = candidate
						heap.Fix(&h, 0)
					}
				}
				if ctx.Err() != nil {
					continue
				}
				scores := []scored(h)
				sort.Slice(scores, func(i, j int) bool { return betterThan(scores[i], scores[j]) })
				neighbors := make([]Similarity, len(scores))
				for i, candidate := range scores {
					neighbors[i] = Similarity{
						Source: s.chunks[source],
						Target: s.chunks[candidate.idx],
						Score:  candidate.score,
					}
				}
				perSource[source] = neighbors
			}
		}()
	}
	for i := range s.chunks {
		if ctx.Err() != nil {
			break
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var out []Similarity
	for _, neighbors := range perSource {
		out = append(out, neighbors...)
	}
	return out, nil
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
	// A corrupt/mixed-dim store (e.g. an incremental rebuild that reused a vector
	// from a store built with a different embedder) would otherwise score the
	// offending chunks 0 via dot's length-mismatch guard instead of erroring.
	for i, v := range s.vectors {
		if len(v) != s.dim {
			return nil, fmt.Errorf("embed: chunk %d vector dim %d != store dim %d", i, len(v), s.dim)
		}
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
