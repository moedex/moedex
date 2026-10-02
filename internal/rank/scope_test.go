package rank

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/embed"
	"moedex/internal/index"
	"moedex/internal/sourcescope"
	"moedex/internal/symbol"
	"moedex/internal/tokenindex"
)

func addScopedFile(ix *index.Index, repo, path, content string) {
	data := []byte(content)
	ix.AddFile(repo, path, "/"+repo+"/"+path, diskstore.GitBlobSHA1(data), data)
}

func TestScopedRankingFiltersBeforeRRFAndTopK(t *testing.T) {
	ix := index.New()
	for i := 0; i < 100; i++ {
		addScopedFile(ix, "outside", fmt.Sprintf("%d.go", i), fmt.Sprintf("target target target %d", i))
	}
	addScopedFile(ix, "inside", "src/target.go", "target plus several other unrelated filler words")
	r := New(ix, tokenindex.Build(ix), nil, nil, Config{PathMinCoverage: -1})
	r.UseTokenCandidates(true)
	hits, status, err := r.RankScopedWithStatus(context.Background(), "target", 1, sourcescope.Scope{Repo: "inside", PathPrefix: "src", Language: "go"})
	if err != nil || status.DenseUnavailable || len(hits) != 1 {
		t.Fatalf("scoped hits=%+v status=%+v err=%v", hits, status, err)
	}
	if len(hits[0].Files) != 1 || hits[0].Files[0].Repo != "inside" {
		t.Fatalf("wrong location: %+v", hits)
	}
	if math.Abs(hits[0].Score-1.0/61) > 1e-12 {
		t.Fatalf("excluded lexical candidates affected arm rank: %v", hits[0].Score)
	}
}

func TestScopedSharedBlobUsesOnlyMatchingPathAndReferences(t *testing.T) {
	ix := index.New()
	shared := "package p\nfunc Shared() {}\n"
	addScopedFile(ix, "outside", "needle.ts", shared)
	addScopedFile(ix, "inside", "src/boring.go", shared)
	addScopedFile(ix, "inside", "other/needle.go", "package another\nfunc Unrelated() {}\n")
	r := New(ix, tokenindex.Build(ix), nil, nil, Config{})
	r.SetSymbols(symbol.BuildMulti(ix))
	r.UseTokenCandidates(true)
	scope := sourcescope.Scope{Repo: "inside", PathPrefix: "src", Language: "go"}
	hits, _, err := r.RankScopedWithStatus(context.Background(), "needle", 1, scope)
	if err != nil || len(hits) != 0 {
		t.Fatalf("excluded path boosted shared blob: %+v %v", hits, err)
	}
	hits, _, err = r.RankScopedWithStatus(context.Background(), "Shared", 1, scope)
	if err != nil || len(hits) != 1 || len(hits[0].Files) != 1 || hits[0].Files[0].RelPath != "src/boring.go" {
		t.Fatalf("matching reference missing/leaked: %+v %v", hits, err)
	}
	before, _, err := r.RankWithStatus(context.Background(), "needle", 10)
	if err != nil {
		t.Fatal(err)
	}
	empty, _, err := r.RankScopedWithStatus(context.Background(), "needle", 10, sourcescope.Scope{})
	if err != nil || !reflect.DeepEqual(before, empty) {
		t.Fatalf("empty scope changed behavior: %+v / %+v (%v)", before, empty, err)
	}
}

type scopedConceptEmbedder struct{}

func (scopedConceptEmbedder) Dim() int { return 2 }
func (scopedConceptEmbedder) Embed(_ context.Context, texts []string) ([]embed.Vector, error) {
	out := make([]embed.Vector, len(texts))
	for i, text := range texts {
		out[i] = embed.Vector{1, 0}
		if strings.Contains(text, "eligible") {
			out[i] = embed.Vector{0.5, 0.8660254}
		}
	}
	return out, nil
}

func TestScopedDenseFiltersBeforeChunkTop64(t *testing.T) {
	ix := index.New()
	for i := 0; i < 100; i++ {
		addScopedFile(ix, "outside", fmt.Sprintf("%d.txt", i), fmt.Sprintf("distractor document %d", i))
	}
	addScopedFile(ix, "inside", "target.go", "eligible semantic document")
	e := scopedConceptEmbedder{}
	store, err := embed.BuildStore(context.Background(), ix, e, 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r := New(ix, tokenindex.Build(ix), store, e, Config{DenseMinQueryTerms: -1, PathMinCoverage: -1})
	hits, status, err := r.RankScopedWithStatus(context.Background(), "concept", 1, sourcescope.Scope{Repo: "inside"})
	if err != nil || status.DenseUnavailable || len(hits) != 1 || hits[0].Files[0].Repo != "inside" || hits[0].Dense <= 0 {
		t.Fatalf("eligible dense chunk lost behind excluded top64: %+v %+v %v", hits, status, err)
	}
}

func TestScopedRankingConcurrentIsolationAndCancellation(t *testing.T) {
	ix := index.New()
	for _, repo := range []string{"one", "two"} {
		addScopedFile(ix, repo, "src/target.go", "package "+repo+"\nfunc Target() {}\n")
	}
	r := New(ix, tokenindex.Build(ix), nil, nil, Config{})
	r.SetSymbols(symbol.BuildMulti(ix))
	r.UseTokenCandidates(true)
	before, err := r.Rank(context.Background(), "Target", 10)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Go(func() {
			repo := []string{"one", "two"}[i%2]
			hits, _, err := r.RankScopedWithStatus(context.Background(), "Target", 1, sourcescope.Scope{Repo: repo})
			if err != nil || len(hits) != 1 || hits[0].Files[0].Repo != repo {
				t.Errorf("scope %s: %+v %v", repo, hits, err)
			}
		})
	}
	wg.Wait()
	after, err := r.Rank(context.Background(), "Target", 10)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("scoped calls mutated shared ranker")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if hits, _, err := r.RankScopedWithStatus(ctx, "Target", 1, sourcescope.Scope{Repo: "one"}); !errors.Is(err, context.Canceled) || hits != nil {
		t.Fatalf("canceled scope: %+v %v", hits, err)
	}
	if _, _, err := r.RankScopedWithStatus(context.Background(), "Target", 1, sourcescope.Scope{PathPrefix: "../src"}); err == nil {
		t.Fatal("invalid scope accepted")
	}
	if hits, _, err := r.RankScopedWithStatus(context.Background(), "Target", 1, sourcescope.Scope{Repo: "missing"}); err != nil || len(hits) != 0 {
		t.Fatalf("missing scope not empty: %+v %v", hits, err)
	}
}

func BenchmarkScopedMembership(b *testing.B) {
	ix := index.New()
	for i := 0; i < 10000; i++ {
		addScopedFile(ix, fmt.Sprintf("repo-%d", i%100), fmt.Sprintf("src/%d.go", i), fmt.Sprintf("package p\n// file %d\n", i))
	}
	r := New(ix, tokenindex.Build(ix), nil, nil, Config{})
	for name, scope := range map[string]sourcescope.Scope{"repo": {Repo: "repo-1"}, "path_only": {PathPrefix: "src"}} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := r.scopedView(context.Background(), scope); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
