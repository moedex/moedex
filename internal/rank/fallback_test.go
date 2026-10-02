package rank

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"moedex/internal/embed"
	"moedex/internal/symbol"
	"moedex/internal/tokenindex"
)

type failingQueryEmbedder struct{ err error }

func (e failingQueryEmbedder) Dim() int { return 8 }
func (e failingQueryEmbedder) Embed(context.Context, []string) ([]embed.Vector, error) {
	return nil, e.err
}

type selectiveQueryEmbedder struct{}

func (selectiveQueryEmbedder) Dim() int { return 8 }
func (selectiveQueryEmbedder) Embed(ctx context.Context, texts []string) ([]embed.Vector, error) {
	if strings.Contains(texts[0], "failure") {
		return nil, errors.New("private backend endpoint/token")
	}
	return fakeEmbedder{dim: 8}.Embed(ctx, texts)
}

type cancelingQueryEmbedder struct{ cancel context.CancelFunc }

func (cancelingQueryEmbedder) Dim() int { return 8 }
func (e cancelingQueryEmbedder) Embed(context.Context, []string) ([]embed.Vector, error) {
	e.cancel()
	return nil, errors.New("backend failed after request cancellation")
}

func TestRankWithStatusRetainsAllNonDenseArms(t *testing.T) {
	ix := buildIndexWithPaths([2]string{"failure.go", "package p\nfunc Failure() {}\n"}, [2]string{"other.go", "package p\n// failure failure\nfunc Other() {}\n"})
	ti := tokenindex.Build(ix)
	store, err := embed.BuildStore(context.Background(), ix, fakeEmbedder{dim: 8}, 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := Config{DenseMinQueryTerms: -1}
	baseline := New(ix, ti, nil, nil, cfg)
	baseline.SetSymbols(symbol.BuildMulti(ix))
	broken := New(ix, ti, store, failingQueryEmbedder{errors.New("private backend endpoint/token")}, cfg)
	broken.SetSymbols(symbol.BuildMulti(ix))
	want, err := baseline.Rank(context.Background(), "failure", 20)
	if err != nil {
		t.Fatal(err)
	}
	got, status, err := broken.RankWithStatus(context.Background(), "failure", 20)
	if err != nil || !status.DenseUnavailable || len(got) == 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback differs from all non-dense arms: got=%+v want=%+v status=%+v err=%v", got, want, status, err)
	}
	if _, err := broken.Rank(context.Background(), "failure", 20); err == nil {
		t.Fatal("strict Rank hid backend error")
	}
	if _, err := broken.Features(context.Background(), "failure"); err == nil {
		t.Fatal("strict Features hid backend error")
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		broken.SetDense(store, failingQueryEmbedder{fmt.Errorf("backend: %w", cause)})
		got, status, err := broken.RankWithStatus(context.Background(), "failure", 20)
		if !errors.Is(err, cause) || got != nil || status.DenseUnavailable {
			t.Fatalf("cancellation degraded: %v %+v %v", got, status, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, _, err := baseline.RankWithStatus(ctx, "failure", 20); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("canceled lexical query=%v/%v", got, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	broken.SetDense(store, cancelingQueryEmbedder{cancel})
	if got, status, err := broken.RankWithStatus(ctx, "failure", 20); !errors.Is(err, context.Canceled) || got != nil || status.DenseUnavailable {
		t.Fatalf("mid-query cancellation degraded: %v %+v %v", got, status, err)
	}
}

func TestRankWithStatusIsRequestLocal(t *testing.T) {
	ix := buildIndex("healthy failure source")
	store, err := embed.BuildStore(context.Background(), ix, fakeEmbedder{dim: 8}, 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r := New(ix, tokenindex.Build(ix), store, selectiveQueryEmbedder{}, Config{DenseMinQueryTerms: -1})
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Go(func() {
			query := "healthy"
			if i%2 == 0 {
				query = "failure"
			}
			got, status, err := r.RankWithStatus(context.Background(), query, 5)
			if err != nil || len(got) == 0 || status.DenseUnavailable != (query == "failure") {
				t.Errorf("query=%s got=%v status=%+v err=%v", query, got, status, err)
			}
			for _, hit := range got {
				if status.DenseUnavailable && hit.Dense != 0 {
					t.Errorf("failed dense arm contributed score %v", hit.Dense)
				}
			}
		})
	}
	wg.Wait()
}
