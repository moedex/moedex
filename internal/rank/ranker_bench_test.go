package rank

import (
	"fmt"
	"testing"

	"moedex/internal/index"
	"moedex/internal/tokenindex"
)

var benchSink int // keeps results live against dead-code elimination

// buildLexicalArmCorpus builds nBlobs blobs that all share nTerms common query
// terms (so every blob is a BM25 candidate for every term), plus a handful of
// blob-unique filler terms so DocLen/TermFreq vary across blobs. This is the
// shape that makes lexicalArm's per-candidate cost dominate: O(candidates x
// terms) idf/df lookups for terms whose idf is actually candidate-invariant.
func buildLexicalArmCorpus(nBlobs, nTerms int) (*index.Index, []string) {
	ix := index.New()
	terms := make([]string, nTerms)
	for i := range terms {
		terms[i] = fmt.Sprintf("commonterm%d", i)
	}
	for b := range nBlobs {
		content := fmt.Sprintf("filler%d filler%d ", b, b)
		for _, t := range terms {
			content += t + " "
		}
		sha := fmt.Sprintf("blob-%d", b)
		ix.AddFile("repo", fmt.Sprintf("f%d.go", b), fmt.Sprintf("/abs/f%d.go", b), sha, []byte(content))
	}
	return ix, terms
}

// BenchmarkLexicalArm_ManyCandidatesManyTerms measures lexicalArm over a query
// with several terms against many candidate blobs that all match every term —
// the case where per-candidate idf/df recomputation (rather than precomputing
// idf once per term before the candidate loop) dominates.
func BenchmarkLexicalArm_ManyCandidatesManyTerms(b *testing.B) {
	ix, terms := buildLexicalArmCorpus(5000, 8)
	ti := tokenindex.Build(ix)
	r := New(ix, ti, nil, nil, Config{})

	for b.Loop() {
		out := r.lexicalArm(terms)
		benchSink += len(out)
	}
}
