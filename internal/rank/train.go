package rank

import (
	"math"
	"math/rand"
)

// This file is the pure-Go, stdlib-only trainer for the LinearReranker. It fits
// the logistic-regression weights by deterministic mini-batchless SGD (one example
// at a time, fixed RNG seed) over a set of labeled feature rows extracted from the
// gold set. There is NO Python pipeline and NO new go.mod dependency: training runs
// in-process, driven by a go test (see internal/eval), and the resulting weights
// can be checked in as a Go literal LinearReranker. The k-fold cross-validation
// helper exists so the A/B eval reports an OUT-OF-FOLD score on the small (~82
// query) gold set rather than a memorized training fit — the central honesty
// mechanism for a learned model trained on so few labels.

// TrainExample is one labeled row: the feature slice (already flattened via
// FeatureVector.Slice with the ranker's RRFk) and a binary relevance label (1 =
// relevant per the gold grade, 0 = not). Group ties a row to the query it came
// from, so k-fold CV can split by QUERY (no leakage of a query's rows across the
// train/test boundary).
type TrainExample struct {
	X     []float64
	Y     float64 // 1 relevant, 0 not
	Group int     // query index, for grouped (leakage-free) CV splits
}

// TrainConfig tunes the SGD. Zero values get sensible defaults via withDefaults.
type TrainConfig struct {
	Epochs       int     // passes over the data (default 200)
	LearningRate float64 // SGD step size (default 0.1)
	L2           float64 // L2 regularization strength (default 1e-3; tames overfit on the tiny gold)
	Seed         int64   // RNG seed for the per-epoch shuffle (default 1; fixed => reproducible)
}

func (c TrainConfig) withDefaults() TrainConfig {
	if c.Epochs <= 0 {
		c.Epochs = 200
	}
	if c.LearningRate <= 0 {
		c.LearningRate = 0.1
	}
	if c.L2 <= 0 {
		c.L2 = 1e-3
	}
	if c.Seed == 0 {
		c.Seed = 1
	}
	return c
}

// standardizer holds per-feature mean/std used to z-score features before the
// linear model sees them. Raw features here span wildly different scales (BM25 can
// be tens, cosine is in [-1,1], rank reciprocals are ~0.016), and an un-normalized
// linear model would let the largest-scale feature dominate and train poorly.
// Standardizing makes the learned weights comparable and the SGD well-conditioned.
// The fitted mean/std are FOLDED INTO the final exported weights (see foldInto) so
// the shipped LinearReranker scores RAW feature vectors directly — no standardizer
// needs to ship or run at query time.
type standardizer struct {
	mean []float64
	std  []float64
}

func fitStandardizer(rows []TrainExample) standardizer {
	if len(rows) == 0 {
		return standardizer{}
	}
	d := len(rows[0].X)
	mean := make([]float64, d)
	for _, r := range rows {
		for j := 0; j < d; j++ {
			mean[j] += r.X[j]
		}
	}
	for j := range mean {
		mean[j] /= float64(len(rows))
	}
	std := make([]float64, d)
	for _, r := range rows {
		for j := 0; j < d; j++ {
			dv := r.X[j] - mean[j]
			std[j] += dv * dv
		}
	}
	for j := range std {
		std[j] = math.Sqrt(std[j] / float64(len(rows)))
		if std[j] < 1e-9 {
			std[j] = 1 // a constant feature: leave it unscaled (its weight will just be ~0)
		}
	}
	return standardizer{mean: mean, std: std}
}

func (s standardizer) transform(x []float64) []float64 {
	out := make([]float64, len(x))
	for j := range x {
		out[j] = (x[j] - s.mean[j]) / s.std[j]
	}
	return out
}

// trainStandardized fits weights/bias in STANDARDIZED feature space and returns
// them along with the per-epoch mean logistic loss (so a caller/test can assert the
// loss actually decreases — real learning, not just execution).
func trainStandardized(rows []TrainExample, s standardizer, cfg TrainConfig) (w []float64, b float64, lossPerEpoch []float64) {
	cfg = cfg.withDefaults()
	if len(rows) == 0 {
		return nil, 0, nil
	}
	d := len(rows[0].X)
	w = make([]float64, d)
	rng := rand.New(rand.NewSource(cfg.Seed))

	// Pre-standardize once.
	xs := make([][]float64, len(rows))
	ys := make([]float64, len(rows))
	for i, r := range rows {
		xs[i] = s.transform(r.X)
		ys[i] = r.Y
	}

	idx := make([]int, len(rows))
	for i := range idx {
		idx[i] = i
	}

	lossPerEpoch = make([]float64, 0, cfg.Epochs)
	for e := 0; e < cfg.Epochs; e++ {
		rng.Shuffle(len(idx), func(i, j int) { idx[i], idx[j] = idx[j], idx[i] })
		for _, i := range idx {
			x, y := xs[i], ys[i]
			z := b
			for j := 0; j < d; j++ {
				z += w[j] * x[j]
			}
			p := sigmoid(z)
			g := p - y // dLoss/dz for logistic loss
			for j := 0; j < d; j++ {
				w[j] -= cfg.LearningRate * (g*x[j] + cfg.L2*w[j])
			}
			b -= cfg.LearningRate * g
		}
		lossPerEpoch = append(lossPerEpoch, meanLogLoss(w, b, xs, ys, cfg.L2))
	}
	return w, b, lossPerEpoch
}

// meanLogLoss is the mean binary cross-entropy (+ L2 penalty) over the rows, used
// only for the convergence diagnostic the trainer test asserts on.
func meanLogLoss(w []float64, b float64, xs [][]float64, ys []float64, l2 float64) float64 {
	const eps = 1e-12
	var sum float64
	for i := range xs {
		z := b
		for j := range w {
			z += w[j] * xs[i][j]
		}
		p := sigmoid(z)
		p = math.Min(1-eps, math.Max(eps, p))
		sum += -(ys[i]*math.Log(p) + (1-ys[i])*math.Log(1-p))
	}
	loss := sum / float64(len(xs))
	var reg float64
	for _, wi := range w {
		reg += wi * wi
	}
	return loss + 0.5*l2*reg
}

// foldInto rewrites STANDARDIZED weights (w_s, b_s) into weights that act on RAW
// features, so the shipped LinearReranker needs no standardizer at query time.
// For a standardized linear model z = b_s + Σ w_s[j]*(x[j]-mean[j])/std[j], the
// equivalent raw-feature weights are w_raw[j] = w_s[j]/std[j] and the raw bias is
// b_raw = b_s - Σ w_s[j]*mean[j]/std[j]. This is an exact algebraic identity (the
// induced ranking is unchanged), not an approximation.
func (s standardizer) foldInto(ws []float64, bs float64) (wRaw []float64, bRaw float64) {
	wRaw = make([]float64, len(ws))
	bRaw = bs
	for j := range ws {
		wRaw[j] = ws[j] / s.std[j]
		bRaw -= ws[j] * s.mean[j] / s.std[j]
	}
	return wRaw, bRaw
}

// Train fits a LinearReranker over the labeled rows by standardized logistic SGD,
// then folds the standardizer into the exported (raw-feature) weights so the
// returned model scores raw FeatureVectors directly. It also returns the per-epoch
// loss for a convergence assertion. Deterministic for a fixed cfg.Seed.
func Train(rows []TrainExample, cfg TrainConfig) (*LinearReranker, []float64) {
	if len(rows) == 0 {
		return &LinearReranker{Weights: make([]float64, NumFeatures)}, nil
	}
	s := fitStandardizer(rows)
	ws, bs, loss := trainStandardized(rows, s, cfg)
	wRaw, bRaw := s.foldInto(ws, bs)
	return &LinearReranker{Weights: wRaw, Bias: bRaw}, loss
}

// GroupKFold partitions the distinct Group ids of rows into k folds (assigned
// round-robin over the SORTED distinct group ids, so the split is deterministic and
// reproducible). It returns, for each fold f, the row indices whose group is in fold
// f (the held-out TEST rows for that fold). Splitting by GROUP (query) — not by row
// — is what makes the eval leakage-free: all rows of one query land in exactly one
// fold, so no query is ever in both the train and test side of a fold.
func GroupKFold(rows []TrainExample, k int) [][]int {
	if k <= 1 {
		// One "fold" containing everything: degenerate, but well-defined.
		all := make([]int, len(rows))
		for i := range all {
			all[i] = i
		}
		return [][]int{all}
	}
	// Distinct groups, sorted ascending for determinism.
	seen := map[int]bool{}
	var groups []int
	for _, r := range rows {
		if !seen[r.Group] {
			seen[r.Group] = true
			groups = append(groups, r.Group)
		}
	}
	sortInts(groups)
	if k > len(groups) {
		k = len(groups)
	}
	if k <= 0 {
		return nil
	}
	foldOf := map[int]int{}
	for i, g := range groups {
		foldOf[g] = i % k
	}
	folds := make([][]int, k)
	for i, r := range rows {
		f := foldOf[r.Group]
		folds[f] = append(folds[f], i)
	}
	return folds
}

// sortInts is a tiny insertion sort; avoids pulling sort just for small int slices
// in this file (the rest of the package already imports sort, but keeping the
// trainer self-contained makes it easy to lift). Stable and deterministic.
func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
