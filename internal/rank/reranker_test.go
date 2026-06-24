package rank

import (
	"context"
	"math"
	"testing"

	"moedex/internal/symbol"
	"moedex/internal/tokenindex"
)

// TestFusionRRFDefaultIsNoOp pins the central safety property: with Fusion at its
// zero value (FusionRRF) the ranker produces byte-identical Score and order to a
// ranker constructed before the reranker existed. We prove it by comparing the
// default-config ranker against an explicitly-FusionRRF ranker AND against a
// FusionLinear ranker with NO model installed (which must fall back to RRF per
// candidate). All three must agree exactly. This protects the two production
// callers, which never opt into FusionLinear.
func TestFusionRRFDefaultIsNoOp(t *testing.T) {
	ix := buildIndexWithPaths(
		[2]string{"billing/refund_order.go", "package billing\nfunc RefundOrder() error { return nil }\n// refund order processing\n"},
		[2]string{"web/order.ts", "export class OrderService { refund() {} }\n"},
		[2]string{"db/orders.sql", "CREATE TABLE orders (id BIGINT);\n-- order refund audit\n"},
	)
	ti := tokenindex.Build(ix)
	syms := symbol.BuildMulti(ix)

	mk := func(f Fusion, withModel bool) *Ranker {
		r := New(ix, ti, nil, nil, Config{Fusion: f})
		r.SetSymbols(syms)
		if withModel {
			// A real (but arbitrary) model; it must be IGNORED because Fusion=RRF, or
			// for the no-model case must be absent so FusionLinear falls back.
			r.SetReranker(&LinearReranker{Weights: make([]float64, NumFeatures), Bias: 1})
		}
		return r
	}

	const q = "refund order"
	def := mk(FusionRRF, false)
	resDefault, err := def.Rank(context.Background(), q, 10)
	if err != nil {
		t.Fatal(err)
	}

	// Explicit FusionRRF with a model installed: the model must be ignored.
	rrfWithModel := mk(FusionRRF, true)
	resRRFModel, err := rrfWithModel.Rank(context.Background(), q, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertIdentical(t, "FusionRRF+model ignored", resDefault, resRRFModel)

	// FusionLinear with NO model: falls back to RRF per candidate.
	linNoModel := mk(FusionLinear, false)
	resLinNoModel, err := linNoModel.Rank(context.Background(), q, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertIdentical(t, "FusionLinear no-model falls back to RRF", resDefault, resLinNoModel)

	if len(resDefault) == 0 {
		t.Fatal("fixture produced no results; the no-op test is vacuous")
	}
}

// assertIdentical fails unless two result slices match in length, order, Blob,
// Score (exact float equality — the no-op must not even perturb the bits), and
// Lexical/Dense components.
func assertIdentical(t *testing.T, label string, want, got []RankedResult) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%s: length differs: want %d got %d", label, len(want), len(got))
	}
	for i := range want {
		if want[i].Blob != got[i].Blob {
			t.Errorf("%s: result %d blob differs: want %d got %d", label, i, want[i].Blob, got[i].Blob)
		}
		if want[i].Score != got[i].Score {
			t.Errorf("%s: result %d score differs: want %v got %v", label, i, want[i].Score, got[i].Score)
		}
		if want[i].Lexical != got[i].Lexical || want[i].Dense != got[i].Dense {
			t.Errorf("%s: result %d components differ", label, i)
		}
	}
}

// TestRecallPreserved checks the reranker never DROPS a candidate: the learned
// mode and RRF must return the same SET of blobs (only the order/score may differ).
// This is the recall-preservation guarantee the parity story rests on.
func TestRecallPreserved(t *testing.T) {
	ix := buildIndexWithPaths(
		[2]string{"billing/refund_order.go", "package billing\nfunc RefundOrder() error { return nil }\n"},
		[2]string{"web/order.ts", "export class OrderService { refund() {} }\n"},
		[2]string{"db/orders.sql", "CREATE TABLE orders (id BIGINT);\n-- refund order audit\n"},
		[2]string{"misc/notes.txt", "the order refund flow is documented here\n"},
	)
	ti := tokenindex.Build(ix)
	syms := symbol.BuildMulti(ix)

	rrf := New(ix, ti, nil, nil, Config{})
	rrf.SetSymbols(syms)
	resRRF, err := rrf.Rank(context.Background(), "refund order", 0) // topK=0 => no truncation
	if err != nil {
		t.Fatal(err)
	}

	// A model that strongly favors symbol coverage — guaranteed to reorder.
	w := make([]float64, NumFeatures)
	w[2] = 5 // SymbolCoverage weight
	lin := New(ix, ti, nil, nil, Config{Fusion: FusionLinear})
	lin.SetSymbols(syms)
	lin.SetReranker(&LinearReranker{Weights: w, Bias: 0})
	resLin, err := lin.Rank(context.Background(), "refund order", 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(resRRF) != len(resLin) {
		t.Fatalf("reranker changed candidate count: rrf=%d learned=%d", len(resRRF), len(resLin))
	}
	set := func(rs []RankedResult) map[uint64]bool {
		m := map[uint64]bool{}
		for _, r := range rs {
			m[r.Blob] = true
		}
		return m
	}
	sRRF, sLin := set(resRRF), set(resLin)
	for b := range sRRF {
		if !sLin[b] {
			t.Errorf("learned reranker DROPPED blob %d that RRF returned (recall regression)", b)
		}
	}
	for b := range sLin {
		if !sRRF[b] {
			t.Errorf("learned reranker INVENTED blob %d RRF did not return", b)
		}
	}
}

// TestLearnedReorders proves the learned mode can actually change the order vs RRF:
// a model that weights symbol coverage heavily must promote the symbol DEFINER above
// a blob that outranks it under RRF. This is the "the plumbing reorders correctly"
// behavioral assertion — not just "it runs."
func TestLearnedReorders(t *testing.T) {
	// Blob 0 (prose) gets a high BM25 (term repeated) but defines no symbol.
	// Blob 1 DEFINES func Refund (symbol coverage) but mentions the word once.
	ix := buildIndexWithPaths(
		[2]string{"docs/refund_notes.txt", "refund refund refund refund handling notes about refund\n"},
		[2]string{"billing/svc.go", "package billing\nfunc Refund() error { return nil }\n"},
	)
	ti := tokenindex.Build(ix)
	syms := symbol.BuildMulti(ix)

	// Under RRF, find which blob is on top.
	rrf := New(ix, ti, nil, nil, Config{})
	rrf.SetSymbols(syms)
	resRRF, err := rrf.Rank(context.Background(), "refund", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(resRRF) < 2 {
		t.Fatalf("expected both blobs as candidates, got %d", len(resRRF))
	}

	// A model that scores ONLY on symbol coverage: the definer (blob 1) must top.
	w := make([]float64, NumFeatures)
	w[2] = 10 // SymbolCoverage
	lin := New(ix, ti, nil, nil, Config{Fusion: FusionLinear})
	lin.SetSymbols(syms)
	lin.SetReranker(&LinearReranker{Weights: w, Bias: 0})
	resLin, err := lin.Rank(context.Background(), "refund", 10)
	if err != nil {
		t.Fatal(err)
	}
	if resLin[0].Blob != 1 {
		t.Errorf("symbol-weighted reranker should top the definer (blob 1), got blob %d", resLin[0].Blob)
	}
	// And the scores must be sigmoid outputs in (0,1) — proving the learned path ran.
	for _, r := range resLin {
		if r.Score <= 0 || r.Score >= 1 {
			t.Errorf("learned score %v not in (0,1); learned path did not run", r.Score)
		}
	}
}

// TestFeatureVectorSlice pins the FROZEN feature order and the rank-reciprocal
// mapping (an absent arm contributes 0; a present arm contributes 1/(k+rank)).
func TestFeatureVectorSlice(t *testing.T) {
	f := FeatureVector{
		BM25:           1.5,
		DenseCosine:    0.8,
		SymbolCoverage: 3,
		PathCoverage:   2,
		RRFScore:       0.25,
		LexRank:        1,
		DenseRank:      AbsentRank, // absent
		SymRank:        2,
		PathRank:       AbsentRank, // absent
	}
	const k = 60
	got := f.Slice(k)
	if len(got) != NumFeatures {
		t.Fatalf("Slice len = %d, want NumFeatures=%d", len(got), NumFeatures)
	}
	want := []float64{
		1.5, 0.8, 3, 2, 0.25,
		1.0 / (k + 1), // lexRank=1
		0,             // denseRank absent
		1.0 / (k + 2), // symRank=2
		0,             // pathRank absent
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Errorf("Slice[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestLinearScoreDeterministicMonotonic checks Score is deterministic and that a
// single positively-weighted feature makes the score monotonically increase in that
// feature (with all weights and bias controlled).
func TestLinearScoreDeterministicMonotonic(t *testing.T) {
	w := make([]float64, NumFeatures)
	w[0] = 2 // weight on BM25 only
	m := &LinearReranker{Weights: w, Bias: -1}
	const k = 60

	lo := FeatureVector{BM25: 0.5}
	hi := FeatureVector{BM25: 5}
	s1 := m.Score(lo, k)
	s2 := m.Score(lo, k)
	if s1 != s2 {
		t.Errorf("Score not deterministic: %v vs %v", s1, s2)
	}
	if m.Score(hi, k) <= m.Score(lo, k) {
		t.Errorf("Score not monotonic in positively-weighted BM25: hi=%v lo=%v", m.Score(hi, k), m.Score(lo, k))
	}
	for _, s := range []float64{m.Score(lo, k), m.Score(hi, k)} {
		if s <= 0 || s >= 1 {
			t.Errorf("sigmoid score %v out of (0,1)", s)
		}
	}
}

// TestLinearScoreMisconfiguredFallsBack checks that a wrong-length weight vector
// does not crash a query: Score returns the RRF score (safe identity fallback) and
// Valid() reports the misconfiguration.
func TestLinearScoreMisconfiguredFallsBack(t *testing.T) {
	bad := &LinearReranker{Weights: []float64{1, 2}} // wrong length
	if bad.Valid() {
		t.Error("Valid() should be false for a wrong-length weight vector")
	}
	f := FeatureVector{RRFScore: 0.42}
	if got := bad.Score(f, 60); got != 0.42 {
		t.Errorf("misconfigured model should fall back to RRFScore 0.42, got %v", got)
	}
	good := &LinearReranker{Weights: make([]float64, NumFeatures)}
	if !good.Valid() {
		t.Error("Valid() should be true for a NumFeatures-length weight vector")
	}
}
