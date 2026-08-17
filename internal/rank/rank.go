// Package rank turns unordered match sets into scored, ordered results.
//
// The ranking architecture calls for BM25 plus learned signals by default, with
// dense scores fused and tuned for agent relevance. This package owns the result
// contract (RankedResult) and the fusion of two retrieval arms:
//
//   - lexical: BM25 over a persistent token index (internal/tokenindex)
//   - dense:   cosine similarity over chunk embeddings (internal/embed)
//
// fused via Reciprocal Rank Fusion (RRF), which needs no score calibration
// between arms and is the conservative default over a learned reranker.
//
// CONTRACT FREEZE: the exported types RankedResult and LineSpan are frozen so
// internal/contextwin can build against them while this package's fusion logic
// is implemented separately. Do not change their shape without updating
// contextwin.
package rank

import "moedex/internal/index"

// LineSpan is a 1-based inclusive range of lines within a blob that carries the
// evidence for a result — the salient region context assembly should center on.
type LineSpan struct {
	StartLine int
	EndLine   int
}

// RankedResult is one scored document (a deduped blob) with the evidence behind
// its score. Files are every path the blob's content appears at (content dedup);
// LineSpans are the salient line ranges that drove the score and seed context
// extraction.
type RankedResult struct {
	Blob      uint64
	Files     []index.FileRef
	Score     float64    // fused score; higher is better
	Lexical   float64    // BM25 component (raw, pre-fusion)
	Dense     float64    // dense cosine component (0 when no dense arm ran)
	LineSpans []LineSpan // salient regions, ascending by StartLine
}
