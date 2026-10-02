package embed

import (
	"context"
	"runtime"
	"sync"
)

// searchWorkers bounds dense query scoring across stores and serving generations.
// One query uses idle capacity, while concurrent queries share the same budget.
// The ceiling is GOMAXPROCS when dense search is first used. Embedding generation
// and offline graph construction have their own lifecycles and are not covered.
var searchWorkers workerBudget

type workerBudget struct {
	once  sync.Once
	slots chan struct{}
}

func (b *workerBudget) acquire(ctx context.Context, wanted int) (int, func(), error) {
	b.once.Do(func() { b.slots = make(chan struct{}, runtime.GOMAXPROCS(0)) })
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	select {
	case b.slots <- struct{}{}:
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
	n := 1
	for n < wanted {
		select {
		case b.slots <- struct{}{}:
			n++
		default:
			return n, b.release(n), nil
		}
	}
	return n, b.release(n), nil
}

func (b *workerBudget) release(n int) func() {
	return func() {
		for i := 0; i < n; i++ {
			<-b.slots
		}
	}
}
