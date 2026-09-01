package rank

import (
	"math"
	"sort"

	"moedex/internal/index"
)

// This file implements an OPTIONAL learned-reranker fusion mode that sits beside
// the default Reciprocal Rank Fusion (RRF) in ranker.go. The strongest named
// follow-up in research/learned-reranker.md is RRF vs a learned reranker for the
// lexical+symbol+dense hybrid. The reranker is a POST-FUSION re-scorer:
// it consumes per-arm features that already exist at fusion time (raw BM25, dense
// cosine, symbol coverage, path coverage, and each arm's per-arm RRF rank, plus the
// aggregate RRF score itself) and emits a fused score that REPLACES the RRF score
// before topK truncation. It never drops a candidate (recall-preserving) and never
// changes the frozen RankedResult/LineSpan contract — only Score and ordering.
//
// PURE GO, ZERO NEW DEPS: the model is a linear logistic-regression scorer
// (sigmoid(w·x + b)) over a fixed feature vector. research/learned-reranker.md
// flags the `leaves` GBDT inference lib as moedex's FIRST non-stdlib Go dependency;
// a linear model needs none. Training is a deterministic stdlib SGD (see train.go)
// driven by a go test over the gold set — no Python, no model-file loading
// dependency. A GBDT upgrade is DEFERRED to a future slice and, if ever pursued,
// must go behind a build tag mirroring -tags onnx.
//
// DEFAULT OFF: Fusion's zero value is FusionRRF, so the two production callers
// (internal/app/mcpcmd, internal/serve/rankcorpus.go) are behaviorally identical to
// today unless they explicitly opt into FusionLinear with an installed model.

// Fusion selects how the per-arm signals are combined into a result's final Score.
// The zero value is FusionRRF, which preserves the historical pure-RRF behavior
// exactly (a no-op relative to the pre-reranker code path).
type Fusion int

const (
	// FusionRRF is the default: Score is the summed Reciprocal Rank Fusion vote
	// across the active arms, exactly as before the learned reranker existed.
	FusionRRF Fusion = iota
	// FusionLinear re-scores each fused candidate with an installed LinearReranker
	// over its FeatureVector. Falls back to RRF when no reranker is installed (so a
	// caller cannot accidentally produce all-zero scores by selecting the mode
	// without a model).
	FusionLinear
)

// AbsentRank is the per-arm rank sentinel used in a FeatureVector when an arm did
// NOT surface the candidate blob. RRF ranks are 1-based (the top hit of an arm has
// rank 1), so 0 unambiguously means "this arm did not vote for this blob." The
// reranker reads it via the rankFeature helper, which maps an absent arm to a 0
// reciprocal contribution (the same thing RRF does — an absent arm adds nothing).
const AbsentRank = 0

// FeatureVector is the per-candidate input to a learned reranker. Every field is
// available at the RRF fusion loop in ranker.go; the learned mode simply stops
// discarding them. The named fields double as documentation of the feature order;
// Slice() flattens them into the dense vector the LinearReranker scores, and the
// order there is the FROZEN training/inference contract (a trained weight vector is
// position-indexed against it).
type FeatureVector struct {
	BM25           float64 // raw BM25 score from the lexical arm (0 if the arm did not surface the blob)
	DenseCosine    float64 // best dense cosine for the blob (0 if no dense arm / not surfaced)
	SymbolCoverage float64 // symbol-name coverage score (sum of qualifying-symbol hits; 0 if not surfaced)
	PathCoverage   float64 // filename/path coverage score (best path's distinct-term hits; 0 if not surfaced)
	RRFScore       float64 // the aggregate RRF vote — the current default Score, kept as a feature

	// Per-arm 1-based RRF rank, or AbsentRank (0) when the arm did not surface the
	// blob. These let the model learn arm-specific position priors (e.g. "trust the
	// symbol arm's rank-1 more than the lexical arm's rank-1").
	LexRank   int
	DenseRank int
	SymRank   int
	PathRank  int
}

// NumFeatures is the length of the flattened feature vector Slice() emits. It is
// the dimension a LinearReranker's Weights must have. Frozen alongside Slice().
const NumFeatures = 9

// Slice flattens the feature vector into the fixed-order dense representation the
// linear model scores. The per-arm ranks are mapped to their RECIPROCAL
// 1/(k+rank) form (with an absent arm contributing 0), mirroring how RRF consumes a
// rank: this keeps the feature bounded in [0, 1/(k+1)] and monotonic (a better/
// smaller rank yields a larger feature), which is far friendlier to a linear model
// than a raw rank integer (where "absent" would otherwise look like the best rank).
// The reciprocal constant rrfK matches the ranker's RRFk so the rank features are on
// the same scale the RRF score itself was built from.
//
// FROZEN ORDER (index -> meaning), the training/inference contract:
//
//	0 BM25            5 lexRankRecip
//	1 DenseCosine     6 denseRankRecip
//	2 SymbolCoverage  7 symRankRecip
//	3 PathCoverage    8 pathRankRecip
//	4 RRFScore
func (f FeatureVector) Slice(rrfK float64) []float64 {
	return []float64{
		f.BM25,
		f.DenseCosine,
		f.SymbolCoverage,
		f.PathCoverage,
		f.RRFScore,
		rankRecip(f.LexRank, rrfK),
		rankRecip(f.DenseRank, rrfK),
		rankRecip(f.SymRank, rrfK),
		rankRecip(f.PathRank, rrfK),
	}
}

// rankRecip maps a 1-based RRF rank to 1/(k+rank); an absent arm (AbsentRank/0)
// contributes 0, exactly as that arm contributes 0 to the RRF sum when it did not
// surface the blob.
func rankRecip(rank int, k float64) float64 {
	if rank <= AbsentRank {
		return 0
	}
	return 1.0 / (k + float64(rank))
}

// BlobFeatures is one fused candidate's features exposed for training/analysis: the
// blob id, the file paths it backs (so the trainer can map a candidate to gold
// RelPaths for labeling), and its FeatureVector. Returned by Ranker.Features.
type BlobFeatures struct {
	Blob  uint64
	Files []index.FileRef
	Feat  FeatureVector
}

// SortBlobFeaturesByRRF sorts candidates by descending RRF score with a blob-id
// tie-break, matching the ranker's default ordering. The trainer uses it to cap a
// query's candidate pool to the most plausible blobs (by RRF) before labeling.
func SortBlobFeaturesByRRF(bf []BlobFeatures) {
	sort.SliceStable(bf, func(i, j int) bool {
		if bf[i].Feat.RRFScore != bf[j].Feat.RRFScore {
			return bf[i].Feat.RRFScore > bf[j].Feat.RRFScore
		}
		return bf[i].Blob < bf[j].Blob
	})
}

// Reranker re-scores a fused candidate from its features. A nil Reranker means
// pure RRF (the ranker checks for nil before calling). Implementations must be
// pure functions of the FeatureVector (no per-query state) so a result's order is
// reproducible.
type Reranker interface {
	// Score returns the candidate's final fused score; higher is better. rrfK is
	// the ranker's RRF constant, passed so the implementation can build rank-based
	// features on the same scale as the RRF score.
	Score(f FeatureVector, rrfK float64) float64
}

// LinearReranker is a pointwise linear logistic-regression scorer:
// sigmoid(w·x + b) over FeatureVector.Slice(). Weights is position-indexed against
// the FROZEN Slice order and must have length NumFeatures. It is the pure-Go,
// zero-dependency model the slice ships; weights are trained offline by the
// deterministic SGD in train.go (driven by a go test over the gold set) and can be
// installed as a checked-in Go literal via a LinearReranker value.
//
// The sigmoid is monotonic, so the RANKING the model induces depends only on the
// linear pre-activation w·x + b; the sigmoid is applied for interpretability (a
// probability-of-relevance score in (0,1)) and to match the logistic training loss.
type LinearReranker struct {
	Weights []float64 // length NumFeatures; position-indexed against FeatureVector.Slice
	Bias    float64
}

// Score implements Reranker. A LinearReranker with a Weights length other than the
// feature dimension is a misconfiguration; rather than panicking mid-query it
// returns the RRF score (a safe identity fallback) so a bad model never crashes a
// production query. Callers that want to detect this should use Valid().
func (m *LinearReranker) Score(f FeatureVector, rrfK float64) float64 {
	x := f.Slice(rrfK)
	if len(m.Weights) != len(x) {
		return f.RRFScore // misconfigured model: fall back to RRF rather than crash
	}
	z := m.Bias
	for i, w := range m.Weights {
		z += w * x[i]
	}
	return sigmoid(z)
}

// Valid reports whether the model's weight vector matches the frozen feature
// dimension. A caller installing a model should check this so a dimension mismatch
// surfaces at install time rather than silently degrading to RRF per query.
func (m *LinearReranker) Valid() bool { return m != nil && len(m.Weights) == NumFeatures }

// sigmoid is the standard logistic function, numerically stable for large |z|.
func sigmoid(z float64) float64 {
	if z >= 0 {
		return 1.0 / (1.0 + math.Exp(-z))
	}
	ez := math.Exp(z)
	return ez / (1.0 + ez)
}
