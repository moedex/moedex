package server

// Real-index macro benchmarks for the agent-facing query path.
//
// The existing *_bench_test.go files (search, setops, fmindex, eval) measure the
// trigram core on synthetic inputs. These measure what an agent actually feels:
// a full search_context round trip — candidate generation, BM25, the symbol/path
// arms, RRF fusion, and context-window assembly — against the REAL corpus.
//
// They are opt-in: each skips unless a prebuilt shard dir is present (default
// ~/.moedex-index/shards, override with MOEDEX_BENCH_SHARDS), so `go test ./...`
// and CI never need the multi-GB index. The corpus loads once for the whole run
// (a single OpenRank, ~cold-start cost) and is shared across every benchmark.
//
// Run:  make bench-real           (or)
//       go test ./internal/server -run '^$' -bench 'Real|RankArms|Contextwin' -benchmem -benchtime=20x
//
// Decomposition. BenchmarkRankArms builds rankers with arm subsets off the SAME
// loaded corpus so the per-arm cost is a clean delta:
//   symbol arm cost ≈ Full − LexPath
//   path arm cost   ≈ LexPath − Lexical
// matching how the live daemon ranks (lexical + path + symbol, dense OFF).

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"moedex/internal/contextwin"
	"moedex/internal/rank"
)

var (
	benchOnce sync.Once
	benchRC   *RankCorpus
	benchErr  error
	benchSink int // keeps results live against dead-code elimination
)

// benchShardDir resolves the real shard dir to benchmark against.
func benchShardDir() string {
	if d := os.Getenv("MOEDEX_BENCH_SHARDS"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".moedex-index", "shards")
}

// loadBenchCorpus opens the real corpus once for the whole benchmark run. It
// matches the live daemon exactly: lexical + symbol + path arms, dense OFF
// (cfg.Emb nil). Skips the benchmark when no shard dir is present.
func loadBenchCorpus(b *testing.B) *RankCorpus {
	b.Helper()
	dir := benchShardDir()
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		b.Skipf("no bench shards at %s (set MOEDEX_BENCH_SHARDS); skipping real-index benchmark", dir)
	}
	benchOnce.Do(func() {
		start := time.Now()
		benchRC, benchErr = OpenRank(context.Background(), dir, RankConfig{TopK: 20})
		if benchErr == nil {
			b.Logf("loaded real corpus from %s in %s — %d blobs, %d docs, %d symbol blobs, %d dense chunks",
				dir, time.Since(start).Round(time.Millisecond),
				benchRC.NumBlobs(), benchRC.NumDocs(), benchRC.NumSymbolBlobs(), benchRC.DenseChunks())
		}
	})
	if benchErr != nil {
		b.Fatalf("OpenRank(%s): %v", dir, benchErr)
	}
	return benchRC
}

// benchQueries span the cost profiles an agent hits: a selective identifier, a
// very common token (large candidate set), multi-word lexical, a natural-language
// phrase, and a rare symbol name.
var benchQueries = []struct{ name, q string }{
	{"selective_ident", "RankConfig"},
	{"common_token", "func"},
	{"multiword", "intersect posting list"},
	{"natural_phrase", "token budget context assembly"},
	{"rare_symbol", "SetEnclosingBytes"},
}

// BenchmarkSearchContext_Real is the headline number: the exact path the MCP
// search_context tool runs, end to end (rank + context assembly), per query class.
// This is the in-process analog of scripts/bench-latency.sh (which adds HTTP).
func BenchmarkSearchContext_Real(b *testing.B) {
	rc := loadBenchCorpus(b)
	ctx := context.Background()
	for _, tc := range benchQueries {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				win, err := rc.SearchContext(ctx, tc.q, 4000, 12)
				if err != nil {
					b.Fatal(err)
				}
				benchSink += len(win.Blocks)
			}
		})
	}
}

// buildArmRanker reconstructs a ranker off the loaded corpus with a chosen subset
// of arms, mirroring OpenRank's wiring (UseTokenCandidates, dense off). Symbol arm
// is gated by SetSymbols; path arm by PathMinCoverage (-1 disables it in New).
func buildArmRanker(rc *RankCorpus, withSymbols, withPath bool) *rank.Ranker {
	cfg := rank.Config{} // defaults applied in New (K1 1.2, B .75, RRFk 60, ...)
	if !withPath {
		cfg.PathMinCoverage = -1 // disable the path arm (and skip building pathPostings)
	}
	rk := rank.New(rc.ix, rc.ti, nil, nil, cfg)
	rk.UseTokenCandidates(true)
	if withSymbols {
		rk.SetSymbols(rc.syms)
	}
	return rk
}

// BenchmarkRankArms attributes per-arm cost. The ranker for each arm subset is
// built once (outside the timed loop); the loop times only Rank.
func BenchmarkRankArms(b *testing.B) {
	rc := loadBenchCorpus(b)
	ctx := context.Background()
	arms := []struct {
		name              string
		symbols, withPath bool
	}{
		{"Lexical", false, false},
		{"LexPath", false, true},
		{"LexSymbol", true, false},
		{"Full", true, true}, // == the live daemon's ranker (dense off)
	}
	for _, a := range arms {
		rk := buildArmRanker(rc, a.symbols, a.withPath)
		for _, tc := range benchQueries {
			b.Run(a.name+"/"+tc.name, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					res, err := rk.Rank(ctx, tc.q, 12)
					if err != nil {
						b.Fatal(err)
					}
					benchSink += len(res)
				}
			})
		}
	}
}

// BenchmarkContextwin_Assemble isolates context-window assembly: ranking is done
// once up front, the loop times only Assemble (with real symbol-scoped blocks),
// so it can be compared against the full SearchContext number.
func BenchmarkContextwin_Assemble(b *testing.B) {
	rc := loadBenchCorpus(b)
	ctx := context.Background()
	rk := buildArmRanker(rc, true, true)
	enclosing := rc.syms.EnclosingBytesFunc()
	for _, tc := range benchQueries {
		results, err := rk.Rank(ctx, tc.q, 12)
		if err != nil {
			b.Fatalf("rank %q: %v", tc.q, err)
		}
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				win := contextwin.Assemble(rc.ix, results, contextwin.Options{
					TokenBudget:    4000,
					EnclosingBytes: enclosing,
				})
				benchSink += len(win.Blocks)
			}
		})
	}
}
