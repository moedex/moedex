package embed

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
)

func TestFilteredDenseSearchMatchesScopedSerialOracle(t *testing.T) {
	s := &Store{dim: 2}
	for i := 0; i < 257; i++ {
		s.chunks = append(s.chunks, Chunk{Blob: uint64(i)})
		s.vec = append(s.vec, normalize(Vector{float32(i%7 - 3), float32(i%11 - 5)})...)
	}
	s.keys = make([]ChunkKey, len(s.chunks))
	file := filepath.Join(t.TempDir(), "quant.store")
	if err := s.SaveQuantized(file); err != nil {
		t.Fatal(err)
	}
	quant, err := LoadStore(file)
	if err != nil {
		t.Fatal(err)
	}
	defer quant.Close()
	for name, store := range map[string]*Store{"float32": s, "int8": quant} {
		t.Run(name, func(t *testing.T) {
			e := &queryVectorEmbedder{vector: Vector{1, 0}}
			accept := func(id uint64) bool { return id%13 == 0 }
			want := make([]Hit, 0)
			for i, c := range store.chunks {
				if accept(c.Blob) {
					want = append(want, Hit{Chunk: c, Score: store.scoreAgainst(normalize(e.vector), i)})
				}
			}
			sort.SliceStable(want, func(i, j int) bool { return want[i].Score > want[j].Score })
			var wg sync.WaitGroup
			for _, k := range []int{1, 7, 64, 300} {
				wg.Go(func() {
					got, err := store.SearchFiltered(context.Background(), e, "query", k, accept)
					if err != nil || !reflect.DeepEqual(got, want[:min(k, len(want))]) {
						t.Errorf("k=%d got=%+v want=%+v err=%v", k, got, want[:min(k, len(want))], err)
					}
				})
			}
			wg.Wait()
			got, err := store.SearchFiltered(context.Background(), e, "query", 10, func(uint64) bool { return false })
			if err != nil || len(got) != 0 {
				t.Fatalf("all filtered: %+v %v", got, err)
			}
			unfiltered, err := store.Search(context.Background(), e, "query", 7)
			if err != nil {
				t.Fatal(err)
			}
			nilFilter, err := store.SearchFiltered(context.Background(), e, "query", 7, nil)
			if err != nil || !reflect.DeepEqual(unfiltered, nilFilter) {
				t.Fatal("nil filter changed search")
			}
		})
	}
}

func TestFilteredDenseSearchCancellation(t *testing.T) {
	s := &Store{dim: 2, chunks: []Chunk{{Blob: 1}}, vec: []float32{1, 0}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hits, err := s.SearchFiltered(ctx, &queryVectorEmbedder{vector: Vector{1, 0}}, "query", 1, func(uint64) bool { cancel(); return false })
	if !errors.Is(err, context.Canceled) || hits != nil {
		t.Fatalf("canceled filtering returned success: %+v %v", hits, err)
	}
}
