package eval

import (
	"context"
	"sort"

	"moedex/internal/index"
	"moedex/internal/ingest"
	"moedex/internal/rank"
	"moedex/internal/symbol"
	"moedex/internal/tokenindex"
)

// QueryReport holds the per-query metric values for one GoldQuery.
type QueryReport struct {
	Query       string
	NumRelevant int      // relevant docs in the gold set for this query
	Ranked      []string // the RelPaths the ranker returned, best first
	RecallAtK   float64  // Recall@K
	PrecAtK     float64  // Precision@K
	MRR         float64  // reciprocal rank of first relevant
	NDCGAtK     float64  // nDCG@K (graded gains)
}

// Report is the aggregate of running a gold set through the ranker: one
// QueryReport per query plus the mean of each metric across queries (the
// MeanMRR field is the canonical MRR — Mean Reciprocal Rank — over the set).
type Report struct {
	K          int
	Queries    []QueryReport
	MeanRecall float64
	MeanPrec   float64
	MeanMRR    float64
	MeanNDCG   float64
}

// Runner evaluates a ranker over a gold set. It owns a built index + token
// index so the same corpus can be reused across many gold queries.
type Runner struct {
	ix *index.Index
	ti *tokenindex.TokenIndex
	r  *rank.Ranker
}

// NewRunner builds a Runner around an already-populated index. It constructs the
// token index and a pure-lexical ranker (nil dense arm — no embedding server
// needed). Use BuildIndexFromCorpus or BuildIndexFromFiles to populate ix.
func NewRunner(ix *index.Index) *Runner {
	ti := tokenindex.Build(ix)
	return &Runner{
		ix: ix,
		ti: ti,
		// nil store + nil emb => pure-lexical ranking, the slice-1 baseline.
		r: rank.New(ix, ti, nil, nil, rank.Config{}),
	}
}

// EnableSymbols builds a multi-language symbol index (Go/C#/TypeScript, by file
// extension) over the runner's corpus and wires it into the ranker as the
// symbol-name RRF arm. Returns the number of blobs that carry symbols (0 when no
// file has a recognized extractor, where the arm is a no-op). Lets a caller
// measure ranking with vs. without the symbol arm by using two runners.
func (run *Runner) EnableSymbols() int {
	syms := symbol.BuildMulti(run.ix)
	run.r.SetSymbols(syms)
	return syms.NumBlobs()
}

// BuildIndexFromFiles indexes a slice of ingested files into a fresh index.
// Shared by the corpus path and tests so the AddFile call site lives in one
// place.
func BuildIndexFromFiles(files []ingest.File) *index.Index {
	ix := index.New()
	for _, f := range files {
		ix.AddFile(f.Repo, f.RelPath, f.AbsPath, f.SHA, f.Content)
	}
	return ix
}

// BuildIndexFromCorpus ingests a single repo directory (via ingest.Repo, which
// requires a git repo) and returns a populated index. It returns the file count
// so callers can skip gracefully when a corpus directory is empty or absent.
func BuildIndexFromCorpus(repoName, dir string) (*index.Index, int, error) {
	files, err := ingest.Repo(repoName, dir)
	if err != nil {
		return nil, 0, err
	}
	return BuildIndexFromFiles(files), len(files), nil
}

// rankedRelPaths runs one query through the real ranker and flattens the
// results into a ranked list of RelPaths (best first). A blob can back several
// files (same content, multiple paths); each result contributes its files in
// order, so a blob's first file takes the blob's rank. Metric functions
// de-duplicate by first occurrence, so a multi-file blob does not inflate
// scores.
func (run *Runner) rankedRelPaths(ctx context.Context, query string, topK int) ([]string, error) {
	results, err := run.r.Rank(ctx, query, topK)
	if err != nil {
		return nil, err
	}
	var ranked []string
	for _, res := range results {
		for _, f := range res.Files {
			ranked = append(ranked, f.RelPath)
		}
	}
	return ranked, nil
}

// Evaluate runs every gold query through the ranker at cutoff k and returns a
// Report. topK bounds how many results the ranker returns per query; pass a
// value >= k (a common choice is the same k, or a larger candidate pool).
func (run *Runner) Evaluate(ctx context.Context, gold []GoldQuery, k, topK int) (Report, error) {
	rep := Report{K: k}
	for _, g := range gold {
		ranked, err := run.rankedRelPaths(ctx, g.Query, topK)
		if err != nil {
			return Report{}, err
		}
		qr := QueryReport{
			Query:       g.Query,
			NumRelevant: numRelevant(g.Relevant),
			Ranked:      ranked,
			RecallAtK:   RecallAtK(ranked, g.Relevant, k),
			PrecAtK:     PrecisionAtK(ranked, g.Relevant, k),
			MRR:         MRR(ranked, g.Relevant),
			NDCGAtK:     NDCGAtK(ranked, g.Relevant, k),
		}
		rep.Queries = append(rep.Queries, qr)
	}
	rep.finalizeMeans()
	return rep, nil
}

// finalizeMeans fills the Mean* fields as the arithmetic mean across queries.
// With no queries the means stay 0.
func (rep *Report) finalizeMeans() {
	n := len(rep.Queries)
	if n == 0 {
		return
	}
	for _, q := range rep.Queries {
		rep.MeanRecall += q.RecallAtK
		rep.MeanPrec += q.PrecAtK
		rep.MeanMRR += q.MRR
		rep.MeanNDCG += q.NDCGAtK
	}
	rep.MeanRecall /= float64(n)
	rep.MeanPrec /= float64(n)
	rep.MeanMRR /= float64(n)
	rep.MeanNDCG /= float64(n)
}

// SortedQueries returns the per-query reports sorted by query string, giving a
// stable order for printing or golden comparison regardless of gold-set order.
func (rep Report) SortedQueries() []QueryReport {
	out := append([]QueryReport(nil), rep.Queries...)
	sort.Slice(out, func(i, j int) bool { return out[i].Query < out[j].Query })
	return out
}
