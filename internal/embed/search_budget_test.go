package embed

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDenseWorkerBudgetCancellationAndReuse(t *testing.T) {
	var b workerBudget
	b.once.Do(func() { b.slots = make(chan struct{}, 2) })
	n, release, err := b.acquire(context.Background(), 8)
	if err != nil || n != 2 {
		t.Fatalf("acquire = %d, %v; want available ceiling 2", n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, undo, err := b.acquire(ctx, 1)
		if undo != nil {
			undo()
		}
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued query error = %v", err)
	}
	release()
	n, release, err = b.acquire(context.Background(), 2)
	if err != nil || n != 2 {
		t.Fatalf("budget leaked after cancellation: %d, %v", n, err)
	}
	release()
}

type queryVectorEmbedder struct {
	vector Vector
	cancel context.CancelFunc
	calls  atomic.Int32
}

func (e *queryVectorEmbedder) Dim() int { return len(e.vector) }
func (e *queryVectorEmbedder) Embed(context.Context, []string) ([]Vector, error) {
	e.calls.Add(1)
	if e.cancel != nil {
		e.cancel()
	}
	return []Vector{e.vector}, nil
}

func TestDenseSearchCancellationDoesNotReturnPartialSuccess(t *testing.T) {
	s := &Store{dim: 2, chunks: []Chunk{{Blob: 1}}, vec: []float32{1, 0}}
	for _, cancelBefore := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		e := &queryVectorEmbedder{vector: Vector{1, 0}, cancel: cancel}
		if cancelBefore {
			cancel()
		}
		hits, err := s.Search(ctx, e, "query", 1)
		cancel()
		if !errors.Is(err, context.Canceled) || hits != nil {
			t.Fatalf("canceled search returned %v, %v", hits, err)
		}
		if cancelBefore && e.calls.Load() != 0 {
			t.Fatal("already canceled search called embedder")
		}
	}
}

// Cancel on a later context checkpoint rather than a wall-clock delay. This
// makes cancellation during the CPU scan deterministic even on fast machines.
type checkpointCancellation struct {
	context.Context
	cancel context.CancelFunc
	checks atomic.Int32
	after  int32
}

func (c *checkpointCancellation) Err() error {
	if c.checks.Add(1) == c.after {
		c.cancel()
	}
	return c.Context.Err()
}

func TestDenseSearchCancellationDuringScoring(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checked := &checkpointCancellation{Context: ctx, cancel: cancel, after: int32(runtime.GOMAXPROCS(0) + 20)}
	const n = 100_000
	s := &Store{dim: 2, chunks: make([]Chunk, n), vec: make([]float32, n*2)}
	e := &queryVectorEmbedder{vector: Vector{1, 0}}
	hits, err := s.Search(checked, e, "query", 10)
	if !errors.Is(err, context.Canceled) || hits != nil {
		t.Fatalf("scan cancellation returned %d hits, error %v", len(hits), err)
	}
	// A canceled scan must release its workers and leave the immutable store
	// usable for the next request.
	if hits, err := s.Search(context.Background(), e, "query", 10); err != nil || len(hits) != 10 {
		t.Fatalf("search after canceled scan: %d hits, %v", len(hits), err)
	}
}

func TestDenseParallelTopKMatchesSerialOracle(t *testing.T) {
	const n, dim = 2051, 8
	s := &Store{dim: dim}
	for i := 0; i < n; i++ {
		v := make(Vector, dim)
		// Repeated and negative vectors exercise exact tie order and weak scores.
		for j := range v {
			v[j] = float32((i*13+j*7)%31 - 15)
		}
		s.chunks = append(s.chunks, Chunk{Blob: uint64(i)})
		s.vec = append(s.vec, normalize(v)...)
	}
	e := &queryVectorEmbedder{vector: Vector{1, -2, 3, 0, 2, 1, 0, -1}}
	q := normalize(e.vector)
	want := make([]Hit, n)
	for i := range want {
		want[i] = Hit{Chunk: s.chunks[i], Score: s.scoreAgainst(q, i)}
	}
	sort.SliceStable(want, func(i, j int) bool { return want[i].Score > want[j].Score })
	var wg sync.WaitGroup
	for _, k := range []int{1, 7, 64, n, n + 10} {
		wg.Go(func() {
			got, err := s.Search(context.Background(), e, "query", k)
			if err != nil {
				t.Errorf("topK %d: %v", k, err)
				return
			}
			if !reflect.DeepEqual(got, want[:min(k, n)]) {
				t.Errorf("topK %d differs from serial exact oracle", k)
			}
		})
	}
	wg.Wait()
}
