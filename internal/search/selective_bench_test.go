package search_test

import (
	"context"
	"crypto/sha1"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/query"
	"moedex/internal/search"
)

// benchCorpus builds a deterministic synthetic fixture: many small "source"
// blobs sharing a heavy common vocabulary (so some trigrams are near-universal,
// like real code), plus rare planted tokens the queries target. The shape is
// what FREE-style selection is designed for: drop the near-universal grams.
func benchCorpus(seed int64, nBlobs int) map[string]string {
	rng := rand.New(rand.NewSource(seed))
	common := []string{"return", "public", "string", "func", "var", "class", "import", "value", "result", "error"}
	rareTokens := []string{"Zaphod42", "Trogdor99", "Wibblewobble", "Quuxinator", "Frobnicate7"}
	blobs := make(map[string]string, nBlobs)
	for i := 0; i < nBlobs; i++ {
		var lines []string
		for l := 0; l < 12; l++ {
			// Each line mixes 2-3 common words (frequent grams) and occasionally a
			// rare token.
			n := 2 + rng.Intn(2)
			parts := make([]string, 0, n+1)
			for k := 0; k < n; k++ {
				parts = append(parts, common[rng.Intn(len(common))])
			}
			if rng.Intn(20) == 0 {
				parts = append(parts, rareTokens[rng.Intn(len(rareTokens))])
			}
			lines = append(lines, fmt.Sprintf("  %s_%d = %s;", parts[0], l, join(parts, " ")))
		}
		blobs[fmt.Sprintf("src/file_%04d.go", i)] = join(lines, "\n") + "\n"
	}
	return blobs
}

func join(s []string, sep string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += sep
		}
		out += x
	}
	return out
}

func buildEager(blobs map[string]string) *index.Index {
	ix := index.New()
	names := sortedKeys(blobs)
	for _, n := range names {
		sha := fmt.Sprintf("%x", sha1.Sum([]byte(blobs[n])))
		ix.AddFile("r", n, "/abs/"+n, sha, []byte(blobs[n]))
	}
	return ix
}

func buildSel(blobs map[string]string, frac float64) *index.Index {
	b := index.NewSelective(index.FrequencyThresholdSelector{MaxDocFraction: frac})
	names := sortedKeys(blobs)
	for _, n := range names {
		sha := fmt.Sprintf("%x", sha1.Sum([]byte(blobs[n])))
		b.AddFile("r", n, "/abs/"+n, sha, []byte(blobs[n]))
	}
	return b.Finalize()
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func diskSize(t testing.TB, ix *index.Index) int64 {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.idx")
	if err := diskstore.Save(ix, p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	return fi.Size()
}

// candidateBlobs returns how many blobs the trigram query selects BEFORE the
// exact RE2/byte verify — the prefilter precision probe. Fewer is more precise.
func candidateBlobs(ix *index.Index, pattern string) int {
	q, err := query.FromRegexp(pattern)
	if err != nil {
		return ix.NumBlobs()
	}
	return len(q.Eval(ix))
}

func trueMatchBlobs(ix *index.Index, pattern string) int {
	ms, err := search.Regex(context.Background(), ix, pattern)
	if err != nil {
		return 0
	}
	seen := map[string]struct{}{}
	for _, m := range ms {
		seen[m.AbsPath] = struct{}{}
	}
	return len(seen)
}

var benchPatterns = []string{
	"Zaphod42", "Trogdor99", "Wibblewobble", "Quuxinator", "Frobnicate7",
	"return", "public", "func", "class",
	"Zaphod42|Trogdor99",
}

// TestSelectiveTradeoffReport builds the SAME fixture all-trigram and selective
// at several df thresholds and reports a size / precision table. It is a
// measurement, not a pass/fail gate (it only fails if a selective candidate set
// ever UNDER-approximates the all-trigram one — a parity break). Run with
//
//	go test ./internal/search/ -run TestSelectiveTradeoffReport -v
//
// Latency is measured separately by BenchmarkSelectiveQuery below.
func TestSelectiveTradeoffReport(t *testing.T) {
	blobs := benchCorpus(20260624, 600)
	eager := buildEager(blobs)
	eagerSize := diskSize(t, eager)

	t.Logf("fixture: %d blobs", eager.NumBlobs())
	t.Logf("%-10s %-14s %-12s %-10s %s", "build", "disk-bytes", "vs-all", "distinct-grams", "avg-precision(cand/true over battery)")

	report := func(label string, ix *index.Index) {
		size := diskSize(t, ix)
		var sumCand, sumTrue int
		for _, pat := range benchPatterns {
			cand := candidateBlobs(ix, pat)
			tru := trueMatchBlobs(ix, pat)
			// Parity guard: selective candidate set must be >= true matches always,
			// and >= the all-trigram candidate set for the SAME pattern.
			if cand < tru {
				t.Errorf("%s pattern %q: candidates(%d) < true matches(%d) — UNDER-APPROX (parity break)", label, pat, cand, tru)
			}
			sumCand += cand
			sumTrue += tru
		}
		prec := 0.0
		if sumCand > 0 {
			prec = float64(sumTrue) / float64(sumCand)
		}
		t.Logf("%-10s %-14d %-12.3f %-10d precision=%.3f (sumTrue=%d / sumCand=%d)",
			label, size, float64(size)/float64(eagerSize), len(ix.Trigrams()), prec, sumTrue, sumCand)
	}

	report("all-trigram", eager)
	for _, frac := range []float64{0.9, 0.5, 0.25, 0.1} {
		report(fmt.Sprintf("sel-%.2f", frac), buildSel(blobs, frac))
	}

	// Cross-check: for every pattern and threshold, selective candidates ⊇
	// all-trigram candidates (superset = sound).
	for _, frac := range []float64{0.9, 0.5, 0.25, 0.1} {
		sel := buildSel(blobs, frac)
		for _, pat := range benchPatterns {
			if candidateBlobs(sel, pat) < candidateBlobs(eager, pat) {
				t.Errorf("frac=%.2f pattern %q: selective candidates < all-trigram candidates (not a superset)", frac, pat)
			}
		}
	}
}

// BenchmarkSelectiveQuery measures per-query latency for the all-trigram build
// vs selective builds at two thresholds, over the rare-token battery (the case
// where selective should be competitive: rare grams are kept, so the positional
// path still fires). Reports ns/op; the tradeoff (coarser filter -> more verify)
// is left for the numbers to show, not assumed.
func BenchmarkSelectiveQuery(b *testing.B) {
	blobs := benchCorpus(20260624, 600)
	builds := []struct {
		name string
		ix   *index.Index
	}{
		{"all-trigram", buildEager(blobs)},
		{"sel-0.50", buildSel(blobs, 0.5)},
		{"sel-0.10", buildSel(blobs, 0.1)},
	}
	pats := []string{"Zaphod42", "return", "Zaphod42|Trogdor99"}
	for _, bd := range builds {
		for _, pat := range pats {
			b.Run(bd.name+"/"+pat, func(b *testing.B) {
				ctx := context.Background()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := search.Regex(ctx, bd.ix, pat); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
