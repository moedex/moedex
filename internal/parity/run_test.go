package parity

import "testing"

// TestMoedexIntoReturnsErrorOnInvalidPattern confirms the scan-phase error path
// run.go discards is real, not hypothetical: a Query whose Go regexp source
// fails to compile makes moedexInto return a non-nil error (independent of
// goldMatcher, which would panic via regexp.MustCompile on the same source —
// so this can only be exercised directly, not through Run()'s shared
// moedex+gold loop).
func TestMoedexIntoReturnsErrorOnInvalidPattern(t *testing.T) {
	ix, ft, _ := buildSynthIndex(t)
	var a accum
	q := Query{Pattern: "("} // invalid regexp: unterminated group
	if _, err := moedexInto(&a, ix, q, ft); err == nil {
		t.Fatal("moedexInto: want error for invalid regex pattern, got nil")
	}
}

// TestHardPassFailsOnMoedexSearchError is the false-PASS regression this run.go
// bug allows: a query with no true matches and a moedex search error that
// returns empty results adjudicates equal to an empty gold set (VOK), so
// without recording the error separately, HardPass sees no UnderApprox,
// no OverApprox, rg available and clean — and wrongly reports a hard pass.
// HardPass must fail whenever a moedex search error was recorded, regardless
// of what adjudication concluded.
func TestHardPassFailsOnMoedexSearchError(t *testing.T) {
	r := &Result{
		RGAvailable: true,
		MoeErrors:   []MoeError{{QueryID: 0, Err: "boom"}},
	}
	if r.HardPass() {
		t.Fatal("HardPass: want false when a moedex search error was recorded")
	}
}

// TestHardPassOKWithNoErrors guards against the fix over-firing: a clean run
// (no under/over-approx, rg available and clean, no moedex errors) must still
// pass.
func TestHardPassOKWithNoErrors(t *testing.T) {
	r := &Result{RGAvailable: true}
	if !r.HardPass() {
		t.Fatal("HardPass: want true for a clean run with no recorded errors")
	}
}

// TestAdjudicateAllExcludesRGFailedQueriesFromEngineQuirk pins F-30: a query
// whose own rg.run() call errored must not be classified as a "Justified
// engine quirk" merely because its ripgrep match set is nil — rg.run()'s
// zero value on error — while moedex agrees with gold. Before the fix, the
// caller threaded the run-wide rgAvail flag into every query's adjudicate()
// call regardless of whether that specific query's rg.run() had failed, so
// this nil set was misread as "ripgrep genuinely found zero matches" — a
// real divergence from a non-empty moe/gold set — filing the query under
// EngineQuirks even though the true cause was a ripgrep tool error, not an
// RE2-vs-Rust semantics quirk. Query 1 is a control: a genuine rg divergence
// (rgFailed=false) must still classify as VEngineQuirk, so the fix only
// excludes queries actually marked failed rather than blanket-suppressing
// the classification.
func TestAdjudicateAllExcludesRGFailedQueriesFromEngineQuirk(t *testing.T) {
	queries := []Query{{Pattern: "a"}, {Pattern: "b"}}
	moeSets := []MatchSet{{pack(1, 1)}, {pack(1, 1)}}
	goldSets := []MatchSet{{pack(1, 1)}, {pack(1, 1)}}
	// Both queries' rg match sets are nil: query 0 because rg.run() actually
	// errored for it (rgFailed[0]=true); query 1 stands in for a genuine rg
	// divergence (rgFailed[1]=false).
	rgSets := []MatchSet{nil, nil}
	rgFailed := []bool{true, false}

	results, _, _, quirks := adjudicateAll(queries, moeSets, rgSets, goldSets, true, rgFailed)

	if results[0].Verdict != VOK {
		t.Fatalf("query 0 (rg.run() errored): Verdict = %v, want VOK (moe==gold, rg not actually comparable)", results[0].Verdict)
	}
	for _, id := range quirks {
		if id == 0 {
			t.Fatalf("query 0 (rg.run() errored) must not appear in EngineQuirks, got quirks=%v", quirks)
		}
	}

	if results[1].Verdict != VEngineQuirk {
		t.Fatalf("query 1 (genuine rg divergence): Verdict = %v, want VEngineQuirk", results[1].Verdict)
	}
	found := false
	for _, id := range quirks {
		if id == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("query 1 (genuine rg divergence) should still appear in EngineQuirks, got quirks=%v", quirks)
	}
}

// TestHardPassFailsWhenRGUnavailable locks the invariant F-055 relies on:
// adjudicate's rgAvail==false branch sets VOK on moedex==gold alone (no
// ripgrep comparison happened), and that is only safe because HardPass
// requires RGAvailable==true regardless of what adjudication concluded. If
// this guard were ever dropped, a run with ripgrep unavailable (or a
// deliberate -no-rg skip) would report a hard PASS with no ripgrep
// comparison at all.
func TestHardPassFailsWhenRGUnavailable(t *testing.T) {
	r := &Result{RGAvailable: false}
	if r.HardPass() {
		t.Fatal("HardPass: want false when RGAvailable is false, even with zero under/over-approx and no errors")
	}
}
