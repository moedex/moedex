package eval

import (
	"context"
	"sort"

	"moedex/internal/embed"
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
	UDCGAtK     float64  // UDCG@K (graded gains minus distractor penalties)
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
	MeanUDCG   float64
}

// Runner evaluates a ranker over a gold set. It owns a built index + token
// index so the same corpus can be reused across many gold queries.
//
// The ranker is (re)built from the parts the Runner holds. EnableDense and
// EnableSymbols mutate those parts and rebuild, so either arm can be enabled in
// either order and both survive — a single Runner can be lexical-only, +dense,
// +symbol, or full hybrid depending on which Enable* methods the caller invokes.
type Runner struct {
	ix    *index.Index
	ti    *tokenindex.TokenIndex
	store *embed.Store         // dense arm; nil = lexical only
	emb   embed.Embedder       // dense arm; nil = lexical only
	syms  *symbol.Index        // symbol-name arm; nil = off
	rr    *rank.LinearReranker // learned reranker; nil = pure RRF even under FusionLinear
	cfg   rank.Config          // ranker config; zero value = all arms at defaults
	r     *rank.Ranker
}

// NewRunner builds a Runner around an already-populated index. It constructs the
// token index and a pure-lexical ranker (nil dense arm — no embedding server
// needed). Use BuildIndexFromCorpus or BuildIndexFromFiles to populate ix.
func NewRunner(ix *index.Index) *Runner {
	run := &Runner{
		ix: ix,
		ti: tokenindex.Build(ix),
	}
	run.rebuild()
	return run
}

// rebuild reconstructs the ranker from the Runner's current parts (store, emb,
// syms). A nil store+emb is the pure-lexical slice-1 baseline; the symbol arm is
// re-installed if present so it is not lost when the dense arm is toggled.
func (run *Runner) rebuild() {
	run.r = rank.New(run.ix, run.ti, run.store, run.emb, run.cfg)
	if run.syms != nil {
		run.r.SetSymbols(run.syms)
	}
	if run.rr != nil {
		run.r.SetReranker(run.rr)
	}
}

// SetFusion selects the fusion mode (RRF vs learned) and rebuilds. Mirrors the
// other Set* knobs so the eval can A/B RRF vs learned on one Runner over the same
// index. The default (rank.FusionRRF) leaves ranking exactly as the production gate
// measures it.
func (run *Runner) SetFusion(f rank.Fusion) {
	run.cfg.Fusion = f
	run.rebuild()
}

// SetReranker installs (or clears, when m is nil) the learned reranker used by
// FusionLinear and rebuilds. With no model installed, FusionLinear falls back to RRF
// per candidate, so a caller can set the mode before training and install the model
// after.
func (run *Runner) SetReranker(m *rank.LinearReranker) {
	run.rr = m
	run.rebuild()
}

// Features runs one query's four arms and returns the per-candidate features
// (without applying any fusion mode), the entry point the learned-reranker trainer
// uses to build labeled rows. Delegates to the underlying ranker so the features are
// identical to what Rank scores.
func (run *Runner) Features(ctx context.Context, query string) ([]rank.BlobFeatures, error) {
	return run.r.Features(ctx, query)
}

// RRFk exposes the ranker's RRF constant so a caller flattening features
// (FeatureVector.Slice) uses the same rank scale the ranker does.
func (run *Runner) RRFk() float64 { return run.r.RRFk() }

// DisablePathArm turns off the filename/path RRF arm (on by default) and rebuilds.
// Used by the eval to measure ranking with vs. without the path signal; production
// callers leave it on.
func (run *Runner) DisablePathArm() {
	run.cfg.PathMinCoverage = -1
	run.rebuild()
}

// SetPathCoverage sets the filename/path arm's coverage gate and rebuilds. Used by
// the eval to sweep the gate; production uses the default.
func (run *Runner) SetPathCoverage(c float64) {
	run.cfg.PathMinCoverage = c
	run.rebuild()
}

// SetSymbolCoverage sets the symbol arm's coverage gate and rebuilds. Used by the
// eval to sweep the gate; production uses the default.
func (run *Runner) SetSymbolCoverage(c float64) {
	run.cfg.SymbolMinCoverage = c
	run.rebuild()
}

// SetDenseMinScore sets the dense arm's cosine confidence gate and rebuilds. Used by
// the eval to sweep the gate; a negative value disables it (every dense chunk votes).
func (run *Runner) SetDenseMinScore(s float64) {
	run.cfg.DenseMinScore = s
	run.rebuild()
}

// SetDenseMinQueryTerms sets the dense arm's query-length gate and rebuilds. Used by
// the eval to sweep the gate; a negative value disables it (dense runs for every query).
func (run *Runner) SetDenseMinQueryTerms(n int) {
	run.cfg.DenseMinQueryTerms = n
	run.rebuild()
}

// EnableSymbols builds a multi-language symbol index (Go/C#/TypeScript/
// ColdFusion/SQL, by file extension) over the runner's corpus and wires it into
// the ranker as the symbol-name RRF arm. Returns the number of blobs that carry
// symbols (0 when no file has a recognized extractor, where the arm is a no-op).
// Lets a caller measure ranking with vs. without the symbol arm by using two
// runners.
func (run *Runner) EnableSymbols() int {
	run.syms = symbol.BuildMulti(run.ix)
	run.rebuild()
	return run.syms.NumBlobs()
}

// EnableDense builds a chunk-embedding Store over the runner's corpus using the
// supplied Embedder and wires it (plus the embedder) into the ranker as the
// dense (cosine) RRF arm. It mirrors EnableSymbols so a caller can measure
// ranking with vs. without the dense arm by toggling it on one or two runners.
//
// The embedder is a parameter rather than constructed internally because the
// production embedder (embed.NewHTTPEmbedder) needs a local embedding server,
// while hermetic tests pass a deterministic fake — keeping internal/eval free of
// a network dependency. Returns the number of embedded chunks (0 when the corpus
// produced no chunks, where the arm is a no-op).
//
// linesPerChunk/overlap mirror embed.BuildStore's chunking parameters.
func (run *Runner) EnableDense(ctx context.Context, e embed.Embedder, linesPerChunk, overlap int) (int, error) {
	store, err := embed.BuildStore(ctx, run.ix, e, linesPerChunk, overlap)
	if err != nil {
		return 0, err
	}
	run.store = store
	run.emb = e
	run.rebuild()
	return store.Len(), nil
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
			UDCGAtK:     UDCGAtK(ranked, g.Relevant, k),
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
		rep.MeanUDCG += q.UDCGAtK
	}
	rep.MeanRecall /= float64(n)
	rep.MeanPrec /= float64(n)
	rep.MeanMRR /= float64(n)
	rep.MeanNDCG /= float64(n)
	rep.MeanUDCG /= float64(n)
}

// SortedQueries returns the per-query reports sorted by query string, giving a
// stable order for printing or golden comparison regardless of gold-set order.
func (rep Report) SortedQueries() []QueryReport {
	out := append([]QueryReport(nil), rep.Queries...)
	sort.Slice(out, func(i, j int) bool { return out[i].Query < out[j].Query })
	return out
}
