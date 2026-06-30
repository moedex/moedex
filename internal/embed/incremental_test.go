package embed

import (
	"bufio"
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"unsafe"

	"moedex/internal/index"
)

// buildTestIndex builds a content-only index from name->content, one blob per
// distinct name (SHA keyed by name so each file is its own blob). Names are
// inserted in sorted order, so renaming a file to sort earlier shifts every
// later blob's ID — exactly the positional-ID churn a real refresh causes.
func buildFileIndex(t *testing.T, files map[string]string) *index.Index {
	t.Helper()
	ix := index.New()
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		ix.AddFile("repo", n, "/abs/"+n, "sha-"+n, []byte(files[n]))
	}
	return ix
}

// chunk params that put each small test blob in a single chunk.
const tcLines, tcOverlap = 40, 10

func mustIncremental(t *testing.T, ix *index.Index, e Embedder, reuse map[ChunkKey]Vector) (*Store, BuildStats) {
	t.Helper()
	s, st, err := BuildStoreIncremental(context.Background(), ix, e, tcLines, tcOverlap, reuse)
	if err != nil {
		t.Fatalf("BuildStoreIncremental: %v", err)
	}
	return s, st
}

// vectorsEqual compares two stores chunk-for-chunk (chunks, keys, and vectors).
func storesEqual(t *testing.T, a, b *Store) {
	t.Helper()
	if !reflect.DeepEqual(a.chunks, b.chunks) {
		t.Fatalf("chunks differ:\n a=%+v\n b=%+v", a.chunks, b.chunks)
	}
	if !reflect.DeepEqual(a.keys, b.keys) {
		t.Fatalf("keys differ")
	}
	if !reflect.DeepEqual(a.vectors, b.vectors) {
		t.Fatalf("vectors differ:\n a=%+v\n b=%+v", a.vectors, b.vectors)
	}
}

// TestIncremental_NilReuseEmbedsEverythingWithKeys verifies a from-scratch
// incremental build embeds every chunk and attaches a content key per chunk.
func TestIncremental_NilReuseEmbedsEverythingWithKeys(t *testing.T) {
	e := newFakeEmbedder(16)
	ix := buildFileIndex(t, map[string]string{
		"a.go": "package a\nfunc A() {}\n",
		"b.go": "package b\nfunc B() {}\n",
	})
	s, st := mustIncremental(t, ix, e, nil)

	if s.Len() != 2 {
		t.Fatalf("Len=%d want 2", s.Len())
	}
	if !s.HasKeys() {
		t.Fatal("store has no keys")
	}
	if st.Total != 2 || st.Reused != 0 || st.Embedded != 2 {
		t.Fatalf("stats=%+v want Total2 Reused0 Embedded2", st)
	}
	// Every vector must equal the deterministic fake embedding of its chunk text.
	for i, c := range s.chunks {
		text := ix.Blob(c.Blob).Content[c.StartByte:c.EndByte]
		want := normalize(mustEmbedOne(t, e, string(text)))
		if !reflect.DeepEqual(s.vectors[i], want) {
			t.Fatalf("chunk %d vector mismatch", i)
		}
	}
}

// TestIncremental_UnchangedReusesAll verifies that rebuilding an unchanged corpus
// from a prior store reuses every vector and embeds nothing.
func TestIncremental_UnchangedReusesAll(t *testing.T) {
	e := newFakeEmbedder(16)
	ix := buildFileIndex(t, map[string]string{
		"a.go": "package a\nfunc A() {}\n",
		"b.go": "package b\nfunc B() {}\n",
		"c.go": "package c\nfunc C() {}\n",
	})
	full, _ := mustIncremental(t, ix, e, nil)

	// A panicking embedder proves the incremental path embeds NOTHING.
	reuse := full.KeyVectors()
	inc, st := mustIncremental(t, ix, panicEmbedder{16}, reuse)

	if st.Reused != full.Len() || st.Embedded != 0 {
		t.Fatalf("stats=%+v want Reused=%d Embedded=0", st, full.Len())
	}
	storesEqual(t, full, inc)
}

// TestIncremental_PartialChange is the core correctness test: a corpus that
// changes one file and adds another must reuse the unchanged files' vectors and
// produce a store byte-identical to a full rebuild — even though blob IDs shift.
func TestIncremental_PartialChange(t *testing.T) {
	e := newFakeEmbedder(16)
	v1 := buildFileIndex(t, map[string]string{
		"a.go": "package a\nfunc Alpha() {}\n",
		"b.go": "package b\nfunc Beta() {}\n",
		"c.go": "package c\nfunc Gamma() {}\n",
	})
	store1, _ := mustIncremental(t, v1, e, nil)

	// v2: a.go unchanged, b.go changed, c.go unchanged, plus a NEW file that sorts
	// first ("0new.go") so every blob ID shifts by one.
	v2 := buildFileIndex(t, map[string]string{
		"0new.go": "package z\nfunc Zeta() {}\n",
		"a.go":    "package a\nfunc Alpha() {}\n",
		"b.go":    "package b\nfunc BetaChanged() {}\n",
		"c.go":    "package c\nfunc Gamma() {}\n",
	})

	inc, st := mustIncremental(t, v2, e, store1.KeyVectors())

	// a.go and c.go reuse; b.go (changed) and 0new.go (new) embed.
	if st.Total != 4 {
		t.Fatalf("Total=%d want 4", st.Total)
	}
	if st.Reused != 2 {
		t.Fatalf("Reused=%d want 2 (a.go, c.go)", st.Reused)
	}
	if st.Embedded != 2 {
		t.Fatalf("Embedded=%d want 2 (b.go changed, 0new.go added)", st.Embedded)
	}

	// The incremental store must equal a full rebuild of v2 exactly.
	fullV2, _ := mustIncremental(t, v2, e, nil)
	storesEqual(t, fullV2, inc)
}

// TestIncremental_DedupsRepeatedNewText verifies identical NEW text appearing in
// several blobs is embedded once, with all occurrences sharing the vector.
func TestIncremental_DedupsRepeatedNewText(t *testing.T) {
	e := newFakeEmbedder(16)
	ix := buildFileIndex(t, map[string]string{
		"a.go": "package dup\nfunc Same() {}\n",
		"b.go": "package dup\nfunc Same() {}\n", // identical content, different name -> distinct blob
	})
	s, st := mustIncremental(t, ix, e, nil)
	if st.Total != 2 {
		t.Fatalf("Total=%d want 2", st.Total)
	}
	if st.Embedded != 1 {
		t.Fatalf("Embedded=%d want 1 (identical text embedded once)", st.Embedded)
	}
	if !reflect.DeepEqual(s.vectors[0], s.vectors[1]) {
		t.Fatal("identical chunks should share a vector")
	}
}

// TestStoreCodec_V2RoundTrip checks a v2 store (with keys) round-trips on disk.
func TestStoreCodec_V2RoundTrip(t *testing.T) {
	e := newFakeEmbedder(16)
	ix := buildFileIndex(t, map[string]string{
		"a.go": "package a\nfunc A() {}\n",
		"b.go": "package b\nfunc B() {}\n",
	})
	orig, _ := mustIncremental(t, ix, e, nil)

	path := filepath.Join(t.TempDir(), "s.store")
	if err := orig.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if !got.HasKeys() {
		t.Fatal("loaded store lost its keys")
	}
	storesEqual(t, orig, got)
}

// TestStore_SaveRefusesKeyless guards the invariant that a persisted store always
// carries keys (so it can seed the next incremental build).
func TestStore_SaveRefusesKeyless(t *testing.T) {
	e := newFakeEmbedder(16)
	ix := buildFileIndex(t, map[string]string{"a.go": "package a\n"})
	s, _ := mustIncremental(t, ix, e, nil)
	s.keys = nil // simulate a legacy keyless store
	if err := s.Save(filepath.Join(t.TempDir(), "s.store")); err == nil {
		t.Fatal("Save should refuse a keyless non-empty store")
	}
}

// TestStoreCodec_V1BackCompatAndMigrate is the live-migration guard: a legacy v1
// file (no keys) must load, search, and then re-key via FillKeys to a v2 store —
// without re-embedding — matching a native v2 build.
func TestStoreCodec_V1BackCompatAndMigrate(t *testing.T) {
	e := newFakeEmbedder(16)
	ix := buildFileIndex(t, map[string]string{
		"a.go": "package a\nfunc A() {}\n",
		"b.go": "package b\nfunc B() {}\n",
	})
	native, _ := mustIncremental(t, ix, e, nil)

	// Write the same chunks+vectors in the legacy v1 layout (no key block).
	path := filepath.Join(t.TempDir(), "legacy.store")
	writeV1Store(t, path, native)

	loaded, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore v1: %v", err)
	}
	if loaded.HasKeys() {
		t.Fatal("v1 store should load without keys")
	}
	if loaded.Len() != native.Len() {
		t.Fatalf("v1 Len=%d want %d", loaded.Len(), native.Len())
	}
	if !reflect.DeepEqual(loaded.vectors, native.vectors) {
		t.Fatal("v1 vectors mismatch")
	}

	// Re-key in place against the same corpus — no embedding — then it must equal
	// the native v2 store and survive a save/load.
	if err := loaded.FillKeys(ix); err != nil {
		t.Fatalf("FillKeys: %v", err)
	}
	if !loaded.HasKeys() {
		t.Fatal("FillKeys did not attach keys")
	}
	storesEqual(t, native, loaded)
}

// TestFillKeys_RejectsMismatchedIndex ensures re-keying against the wrong corpus
// fails loudly rather than producing wrong keys.
func TestFillKeys_RejectsMismatchedIndex(t *testing.T) {
	e := newFakeEmbedder(16)
	big := buildFileIndex(t, map[string]string{
		"a.go": "package a\n", "b.go": "package b\n", "c.go": "package c\n",
	})
	s, _ := mustIncremental(t, big, e, nil)

	small := buildFileIndex(t, map[string]string{"a.go": "package a\n"})
	if err := s.FillKeys(small); err == nil {
		t.Fatal("FillKeys should reject a store whose chunks exceed the index")
	}
}

func TestBuildStoreIncremental_SkipsNilBlobSlots(t *testing.T) {
	e := newFakeEmbedder(16)
	ix := buildFileIndex(t, map[string]string{
		"a.go": "package a\nfunc A() {}\n",
		"b.go": "package b\nfunc B() {}\n",
	})
	clearIndexBlobForTest(t, ix, 0)

	s, st := mustIncremental(t, ix, e, nil)
	if st.Total != 1 || st.Embedded != 1 || st.Reused != 0 {
		t.Fatalf("stats=%+v want Total1 Embedded1 Reused0", st)
	}
	if s.Len() != 1 {
		t.Fatalf("Len=%d want 1", s.Len())
	}
	if got := s.chunks[0].Blob; got != 1 {
		t.Fatalf("chunk blob=%d want surviving blob 1", got)
	}
}

func TestFillKeys_RejectsNilBlobSlots(t *testing.T) {
	ix := buildFileIndex(t, map[string]string{"a.go": "package a\n"})
	clearIndexBlobForTest(t, ix, 0)

	s := &Store{chunks: []Chunk{{Blob: 0, StartByte: 0, EndByte: 1}}}
	if err := s.FillKeys(ix); err == nil {
		t.Fatal("FillKeys should reject an in-range nil blob")
	}
}

// --- test helpers ---

func mustEmbedOne(t *testing.T, e Embedder, text string) Vector {
	t.Helper()
	vs, err := e.Embed(context.Background(), []string{text})
	if err != nil || len(vs) != 1 {
		t.Fatalf("embed one: %v (n=%d)", err, len(vs))
	}
	return vs[0]
}

func clearIndexBlobForTest(t *testing.T, ix *index.Index, id int) {
	t.Helper()
	v := reflect.ValueOf(ix).Elem().FieldByName("blobs")
	if id < 0 || id >= v.Len() {
		t.Fatalf("blob id %d out of range", id)
	}
	blobs := reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem()
	blobs.Index(id).Set(reflect.Zero(blobs.Type().Elem()))
}

// panicEmbedder fails if asked to embed anything — proving a path embeds nothing.
type panicEmbedder struct{ dim int }

func (p panicEmbedder) Dim() int { return p.dim }
func (p panicEmbedder) Embed(_ context.Context, texts []string) ([]Vector, error) {
	if len(texts) > 0 {
		panic("embedder called but all chunks should have been reused")
	}
	return nil, nil
}

// writeV1Store writes s's chunks+vectors in the legacy version-1 on-disk layout
// (no key block), mirroring the historical codec so back-compat is exercised.
func writeV1Store(t *testing.T, path string, s *Store) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	le := binary.LittleEndian
	var u32 [4]byte
	var u64 [8]byte
	w.Write(storeMagic[:])
	le.PutUint32(u32[:], 1) // version 1
	w.Write(u32[:])
	le.PutUint32(u32[:], uint32(s.dim))
	w.Write(u32[:])
	le.PutUint32(u32[:], uint32(len(s.chunks)))
	w.Write(u32[:])
	for _, c := range s.chunks {
		le.PutUint64(u64[:], c.Blob)
		w.Write(u64[:])
		le.PutUint32(u32[:], uint32(c.StartLine))
		w.Write(u32[:])
		le.PutUint32(u32[:], uint32(c.EndLine))
		w.Write(u32[:])
		le.PutUint64(u64[:], uint64(c.StartByte))
		w.Write(u64[:])
		le.PutUint64(u64[:], uint64(c.EndByte))
		w.Write(u64[:])
	}
	for _, v := range s.vectors {
		for _, x := range v {
			le.PutUint32(u32[:], math.Float32bits(x))
			w.Write(u32[:])
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
}
