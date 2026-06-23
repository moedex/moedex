package eval

import (
	"fmt"
	"math/rand"
	"testing"

	"moedex/internal/index"
	"moedex/internal/query"
	"moedex/internal/search"
)

// benchCorpus builds a synthetic-but-substantial in-memory index for profiling
// the hot paths. It is deterministic (fixed seed) so benchmark runs are
// comparable. Sizing (~2000 blobs * ~1.5KB = ~3MB of content, a few hundred
// thousand trigram postings) is chosen large enough that trigram intersection
// and scanning timings are meaningful while keeping `go test -bench` fast.
//
// Content is pseudo-code drawn from a small vocabulary so that:
//   - a rare token ("zqxj_marker") is highly selective (few candidate blobs),
//   - a common token ("func"/"return") hits nearly every blob,
//   - alternations and character classes exercise the regex path.
func benchCorpus(tb testing.TB) *index.Index {
	tb.Helper()
	const nBlobs = 2000
	rng := rand.New(rand.NewSource(1))

	// A modest vocabulary of identifier-ish tokens.
	vocab := []string{
		"func", "return", "value", "handler", "request", "response", "buffer",
		"client", "server", "config", "context", "error", "result", "payload",
		"session", "token", "encode", "decode", "parse", "build", "render",
		"compute", "process", "validate", "connect", "listen", "accept", "write",
	}

	ix := index.New()
	for b := 0; b < nBlobs; b++ {
		var sb []byte
		sb = append(sb, []byte(fmt.Sprintf("package pkg%d\n", b%37))...)
		// ~40 lines per blob.
		for line := 0; line < 40; line++ {
			w1 := vocab[rng.Intn(len(vocab))]
			w2 := vocab[rng.Intn(len(vocab))]
			w3 := vocab[rng.Intn(len(vocab))]
			sb = append(sb, []byte(fmt.Sprintf("func %s_%d(%s int) { return %s(%d) }\n", w1, line, w2, w3, rng.Intn(1000)))...)
		}
		// Plant a rare marker in exactly ~1% of blobs so selective queries have
		// a small, non-empty candidate set.
		if b%100 == 0 {
			sb = append(sb, []byte("// zqxj_marker unique sentinel here\n")...)
		}
		rel := fmt.Sprintf("dir%d/file%d.go", b%50, b)
		abs := "/synthetic/" + rel
		sha := fmt.Sprintf("sha-%d", b) // distinct content -> distinct blob
		ix.AddFile("synthrepo", rel, abs, sha, sb)
	}
	return ix
}

// BenchmarkQueryEval_Selective times trigram intersection for a highly selective
// literal: the rare marker, present in ~1% of blobs.
func BenchmarkQueryEval_Selective(b *testing.B) {
	ix := benchCorpus(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q, err := query.FromRegexp("zqxj_marker")
		if err != nil {
			b.Fatal(err)
		}
		_ = q.Eval(ix)
	}
}

// BenchmarkQueryEval_Common times trigram intersection for a common literal
// ("return") that appears in nearly every blob — the large-candidate-set case.
func BenchmarkQueryEval_Common(b *testing.B) {
	ix := benchCorpus(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q, err := query.FromRegexp("return")
		if err != nil {
			b.Fatal(err)
		}
		_ = q.Eval(ix)
	}
}

// BenchmarkQueryEval_Alternation times the trigram query built from an
// alternation regex (Cox-reduction over an OR of literals).
func BenchmarkQueryEval_Alternation(b *testing.B) {
	ix := benchCorpus(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q, err := query.FromRegexp("handler|response|payload")
		if err != nil {
			b.Fatal(err)
		}
		_ = q.Eval(ix)
	}
}

// BenchmarkSearchLiteral_Selective times the full literal search (candidate
// trigram pass + line scan) for a selective term.
func BenchmarkSearchLiteral_Selective(b *testing.B) {
	ix := benchCorpus(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = search.Literal(ix, "zqxj_marker")
	}
}

// BenchmarkSearchLiteral_Common times literal search for a common term, where
// the line-scanning phase dominates.
func BenchmarkSearchLiteral_Common(b *testing.B) {
	ix := benchCorpus(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = search.Literal(ix, "return")
	}
}

// BenchmarkSearchRegex_Alternation times regex search over an alternation —
// trigram candidate selection plus regexp line matching.
func BenchmarkSearchRegex_Alternation(b *testing.B) {
	ix := benchCorpus(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := search.Regex(ix, "handler|response|payload"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSearchRegex_Class times regex search for a class-only pattern. Such a
// pattern yields few/no usable trigrams, so this stresses the fallback scan path
// and is a candidate hot spot for future SIMD work.
func BenchmarkSearchRegex_Class(b *testing.B) {
	ix := benchCorpus(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := search.Regex(ix, "func_[0-9]+"); err != nil {
			b.Fatal(err)
		}
	}
}
