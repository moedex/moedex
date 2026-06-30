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
