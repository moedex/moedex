package serve

import (
	"context"
	"errors"
	"testing"

	"moedex/internal/contextwin"
	"moedex/internal/embed"
	"moedex/internal/rank"
)

type runtimeFailureEmbedder struct{}

func (runtimeFailureEmbedder) Dim() int { return 2 }
func (runtimeFailureEmbedder) Embed(context.Context, []string) ([]embed.Vector, error) {
	return nil, errors.New("private backend unavailable")
}

func TestRankCorpusDenseFallbackIsNotCacheable(t *testing.T) {
	dir := buildDedupedDir(t, map[string]map[string]string{"repo": {"source.go": "package p\nfunc Lookup() {}\n"}})
	// Open/build dense while healthy, then configure the same persisted content
	// with a query-only failing embedder. No shared ranker is mutated.
	initial, err := OpenRank(context.Background(), dir, RankConfig{Emb: conceptEmbedder{}})
	if err != nil {
		t.Fatal(err)
	}
	defer initial.Close()
	failed, err := OpenRank(context.Background(), dir, RankConfig{Store: initial.store, Emb: runtimeFailureEmbedder{}, Rank: rank.Config{DenseMinQueryTerms: -1}})
	if err != nil {
		t.Fatal(err)
	}
	defer failed.Close()
	for _, query := range []string{"Lookup", "unmatchable"} {
		result, err := failed.SearchContextWithSnapshot(context.Background(), query, 1000, 5)
		if err != nil {
			t.Fatal(err)
		}
		if result.Snapshot.Cacheable || result.Snapshot.CorpusFingerprint == "" {
			t.Fatalf("snapshot=%+v", result.Snapshot)
		}
		if len(result.Window.Warnings) != 1 || result.Window.Warnings[0] != contextwin.WarningDenseUnavailable {
			t.Fatalf("warnings=%v", result.Window.Warnings)
		}
	}
}
