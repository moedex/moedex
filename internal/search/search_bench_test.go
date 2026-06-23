package search_test

import (
	"fmt"
	"math/rand"
	"testing"

	"moedex/internal/index"
	"moedex/internal/search"
)

// benchIndex builds a synthetic in-memory index for benchmarking the hot verify
// paths. It mirrors internal/eval/bench_test.go's corpus shape (deterministic,
// fixed seed) so the search-package numbers are comparable to the eval profile
// that motivated this work: ~2000 blobs, ~40 lines each, a rare marker in ~1%
// of blobs for selective queries, and a common vocabulary so terms like
// "return" hit nearly every blob.
func benchIndex(tb testing.TB) *index.Index {
	tb.Helper()
	const nBlobs = 2000
	rng := rand.New(rand.NewSource(1))

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
		for line := 0; line < 40; line++ {
			w1 := vocab[rng.Intn(len(vocab))]
			w2 := vocab[rng.Intn(len(vocab))]
			w3 := vocab[rng.Intn(len(vocab))]
			sb = append(sb, []byte(fmt.Sprintf("func %s_%d(%s int) { return %s(%d) }\n", w1, line, w2, w3, rng.Intn(1000)))...)
		}
		if b%100 == 0 {
			sb = append(sb, []byte("// zqxj_marker unique sentinel here\n")...)
		}
		rel := fmt.Sprintf("dir%d/file%d.go", b%50, b)
		abs := "/synthetic/" + rel
		sha := fmt.Sprintf("sha-%d", b)
		ix.AddFile("synthrepo", rel, abs, sha, sb)
	}
	return ix
}

// BenchmarkRegexAlternation times the regex verify path over an alternation of
// common literals — many candidate blobs survive trigram filtering, so the
// per-line regexp.Match dominates. This is the ~110ms hot path from the eval
// profile.
func BenchmarkRegexAlternation(b *testing.B) {
	ix := benchIndex(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := search.Regex(ix, "handler|response|payload"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLiteralCommon times the literal verify path for a common term
// ("return") present on nearly every line — the begin/end-gram positional
// intersection plus the bytes.Equal confirmation. This is the ~37ms hot path.
func BenchmarkLiteralCommon(b *testing.B) {
	ix := benchIndex(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = search.Literal(ix, "return")
	}
}

// BenchmarkRegexClassOnly times a pattern with a leading literal followed by a
// character class ("func_[0-9]+"). The literal prefix "func_" lets the
// prefilter reject lines cheaply before invoking regexp.
func BenchmarkRegexClassOnly(b *testing.B) {
	ix := benchIndex(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := search.Regex(ix, "func_[0-9]+"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRegexSelective times the regex verify path for a highly selective
// literal pattern (the rare marker, ~1% of blobs). Few candidates, so this
// measures the setup/overhead floor rather than line-scan throughput.
func BenchmarkRegexSelective(b *testing.B) {
	ix := benchIndex(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := search.Regex(ix, "zqxj_marker"); err != nil {
			b.Fatal(err)
		}
	}
}
