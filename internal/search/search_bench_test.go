package search_test

import (
	"context"
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
		if _, err := search.Regex(context.Background(), ix,"handler|response|payload"); err != nil {
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
		_, _ = search.Literal(context.Background(), ix, "return")
	}
}

// BenchmarkRegexClassOnly times a pattern with a leading literal followed by a
// character class ("func_[0-9]+"). The literal prefix "func_" lets the
// prefilter reject lines cheaply before invoking regexp.
func BenchmarkRegexClassOnly(b *testing.B) {
	ix := benchIndex(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := search.Regex(context.Background(), ix,"func_[0-9]+"); err != nil {
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
		if _, err := search.Regex(context.Background(), ix,"zqxj_marker"); err != nil {
			b.Fatal(err)
		}
	}
}

// bigBlobIndex models the full-corpus latency tail: a handful of LARGE blobs
// (minified/generated files, ~3000 lines each) that survive trigram candidate
// selection. The cost there is NOT regexp — only a few lines reach RE2 — but the
// per-line prefilter scan over millions of candidate lines. The rare tokens
// ("razavi", "itemanswer") appear on just a couple of lines per blob, so a
// case-insensitive alternation has a tiny match set yet a huge candidate-line
// set: exactly the #907-style tail from the parity report.
func bigBlobIndex(tb testing.TB) *index.Index {
	tb.Helper()
	const nBlobs = 120
	const linesPerBlob = 3000
	rng := rand.New(rand.NewSource(7))
	vocab := []string{
		"function", "return", "value", "handler", "request", "response", "buffer",
		"element", "config", "context", "render", "compute", "process", "validate",
		"window", "document", "prototype", "callback", "property", "attribute",
	}
	ix := index.New()
	for blob := 0; blob < nBlobs; blob++ {
		var sb []byte
		sb = append(sb, []byte(fmt.Sprintf("// generated bundle %d\n", blob))...)
		for line := 0; line < linesPerBlob; line++ {
			w1 := vocab[rng.Intn(len(vocab))]
			w2 := vocab[rng.Intn(len(vocab))]
			w3 := vocab[rng.Intn(len(vocab))]
			sb = append(sb, []byte(fmt.Sprintf("var %s_%d=function(%s){return %s(%d)};\n", w1, line, w2, w3, rng.Intn(100000)))...)
		}
		// Two rare tokens, each on exactly one line of this blob.
		sb = append(sb, []byte(fmt.Sprintf("var Razavi_%d = itemAnswer(%d);\n", blob, blob))...)
		rel := fmt.Sprintf("bundles/app%d.min.js", blob)
		abs := "/synthetic/" + rel
		ix.AddFile("bundlerepo", rel, abs, fmt.Sprintf("bigsha-%d", blob), sb)
	}
	return ix
}

// BenchmarkRegexFoldedAlternationBigBlobs is the headline tail-latency benchmark:
// a case-insensitive alternation of two rare tokens over large blobs. Candidate
// selection keeps every blob (both tokens are present), so the whole corpus is
// scanned, but only ~2 lines per blob can match. The old per-line prefilter ran
// up to ~16 bytes.Contains calls on each of ~360k candidate lines; the buffer
// scan should find the few candidate lines with a handful of bytes.Index sweeps.
func BenchmarkRegexFoldedAlternationBigBlobs(b *testing.B) {
	ix := bigBlobIndex(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := search.Regex(context.Background(), ix,"(?i)razavi|itemanswer"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLiteralSubTrigramBigBlobs covers the sub-trigram literal tail (no
// trigram to intersect, every blob scanned line by line). "->" is rarer than
// ";", so the buffer scan should skip most lines.
func BenchmarkLiteralSubTrigramBigBlobs(b *testing.B) {
	ix := bigBlobIndex(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = search.Literal(context.Background(), ix, "=>")
	}
}
