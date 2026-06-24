package rank

import (
	"math/rand"
	"testing"
)

// TestTrainConverges asserts the SGD actually LEARNS on a linearly-separable
// synthetic set: the per-epoch loss must end well below where it started, and the
// trained model must classify the two clusters correctly. This is "real learning,"
// not "the code runs."
func TestTrainConverges(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var rows []TrainExample
	// Positives: high BM25 + high symbol coverage. Negatives: low both. Other
	// features are noise. Separable along a linear boundary.
	for i := 0; i < 60; i++ {
		pos := TrainExample{X: make([]float64, NumFeatures), Y: 1, Group: i}
		pos.X[0] = 4 + rng.NormFloat64()*0.3 // BM25
		pos.X[2] = 3 + rng.NormFloat64()*0.3 // SymbolCoverage
		pos.X[1] = rng.NormFloat64() * 0.1
		rows = append(rows, pos)

		neg := TrainExample{X: make([]float64, NumFeatures), Y: 0, Group: 1000 + i}
		neg.X[0] = 0.5 + rng.NormFloat64()*0.3
		neg.X[2] = 0.2 + rng.NormFloat64()*0.3
		neg.X[1] = rng.NormFloat64() * 0.1
		rows = append(rows, neg)
	}

	model, loss := Train(rows, TrainConfig{Epochs: 150})
	if len(loss) == 0 {
		t.Fatal("no per-epoch loss recorded")
	}
	first, last := loss[0], loss[len(loss)-1]
	if !(last < first*0.5) {
		t.Errorf("loss did not converge: first=%.4f last=%.4f (want last < first/2)", first, last)
	}
	// The loss curve should be (largely) monotonically decreasing; assert the tail
	// is below the head at several checkpoints rather than every step (SGD wobbles).
	if loss[len(loss)/2] >= loss[0] {
		t.Errorf("loss did not decrease by the halfway point: start=%.4f mid=%.4f", loss[0], loss[len(loss)/2])
	}
	if !model.Valid() {
		t.Fatalf("trained model invalid: weight len %d", len(model.Weights))
	}

	// The model must separate the clusters: a clear positive scores > a clear
	// negative. (Score is sigmoid(w·x_raw+b) on RAW features — the standardizer was
	// folded in, so we feed raw values here.)
	posF := FeatureVector{BM25: 4, SymbolCoverage: 3}
	negF := FeatureVector{BM25: 0.5, SymbolCoverage: 0.2}
	if model.Score(posF, 60) <= model.Score(negF, 60) {
		t.Errorf("trained model did not separate clusters: pos=%.4f neg=%.4f",
			model.Score(posF, 60), model.Score(negF, 60))
	}
}

// TestTrainDeterministic asserts a fixed seed yields identical weights across runs.
func TestTrainDeterministic(t *testing.T) {
	rows := syntheticRows(8)
	m1, _ := Train(rows, TrainConfig{Seed: 42, Epochs: 50})
	m2, _ := Train(rows, TrainConfig{Seed: 42, Epochs: 50})
	if m1.Bias != m2.Bias {
		t.Errorf("bias not reproducible: %v vs %v", m1.Bias, m2.Bias)
	}
	for i := range m1.Weights {
		if m1.Weights[i] != m2.Weights[i] {
			t.Errorf("weight[%d] not reproducible: %v vs %v", i, m1.Weights[i], m2.Weights[i])
		}
	}
}

// TestFoldIntoEquivalence checks the algebraic identity behind shipping raw-feature
// weights: scoring a raw vector with the folded model equals scoring its
// standardized form with the standardized weights. This guarantees the eval (which
// trains, gets a raw-feature model back, and scores raw FeatureVectors at query
// time) measures the same model the SGD optimized.
func TestFoldIntoEquivalence(t *testing.T) {
	rows := syntheticRows(10)
	s := fitStandardizer(rows)
	ws, bs, _ := trainStandardized(rows, s, TrainConfig{Seed: 3, Epochs: 40})
	wRaw, bRaw := s.foldInto(ws, bs)

	// For each row, sigmoid(bs + ws·z(x)) must equal sigmoid(bRaw + wRaw·x).
	for _, r := range rows {
		z := s.transform(r.X)
		var aStd = bs
		for j := range ws {
			aStd += ws[j] * z[j]
		}
		var aRaw = bRaw
		for j := range wRaw {
			aRaw += wRaw[j] * r.X[j]
		}
		if d := aStd - aRaw; d > 1e-9 || d < -1e-9 {
			t.Fatalf("folded raw-feature pre-activation diverges from standardized: std=%v raw=%v", aStd, aRaw)
		}
	}
}

// TestGroupKFoldDisjoint asserts the CV split is leakage-free: every row appears in
// exactly one fold, and no GROUP (query) is split across folds.
func TestGroupKFoldDisjoint(t *testing.T) {
	var rows []TrainExample
	// 10 groups, 3 rows each.
	for g := 0; g < 10; g++ {
		for r := 0; r < 3; r++ {
			rows = append(rows, TrainExample{X: make([]float64, NumFeatures), Group: g})
		}
	}
	const k = 5
	folds := GroupKFold(rows, k)
	if len(folds) != k {
		t.Fatalf("expected %d folds, got %d", k, len(folds))
	}

	// Every row index appears exactly once across all folds.
	seen := map[int]int{}
	for _, f := range folds {
		for _, idx := range f {
			seen[idx]++
		}
	}
	if len(seen) != len(rows) {
		t.Errorf("folds cover %d of %d rows", len(seen), len(rows))
	}
	for idx, n := range seen {
		if n != 1 {
			t.Errorf("row %d appears in %d folds (want 1)", idx, n)
		}
	}

	// No group is split: collect each group's fold assignment and check uniqueness.
	groupFold := map[int]int{}
	for fi, f := range folds {
		for _, idx := range f {
			g := rows[idx].Group
			if prev, ok := groupFold[g]; ok && prev != fi {
				t.Errorf("group %d split across folds %d and %d (leakage)", g, prev, fi)
			}
			groupFold[g] = fi
		}
	}
}

// syntheticRows builds n positive + n negative separable rows with stable content.
func syntheticRows(n int) []TrainExample {
	rng := rand.New(rand.NewSource(99))
	var rows []TrainExample
	for i := 0; i < n; i++ {
		pos := TrainExample{X: make([]float64, NumFeatures), Y: 1, Group: i}
		pos.X[0] = 3 + rng.NormFloat64()*0.2
		pos.X[3] = 2 + rng.NormFloat64()*0.2
		rows = append(rows, pos)
		neg := TrainExample{X: make([]float64, NumFeatures), Y: 0, Group: 1000 + i}
		neg.X[0] = 0.3 + rng.NormFloat64()*0.2
		neg.X[3] = 0.1 + rng.NormFloat64()*0.2
		rows = append(rows, neg)
	}
	return rows
}
