package paritycmd

import (
	"strings"
	"testing"

	"moedex/internal/parity"
)

// TestResultOutcome_NoRGSkipIsNotAGateFailure is the regression for F-056:
// HardPass requires RGAvailable, but -no-rg (documented "latency-profiling
// only; NOT a parity gate") leaves RGAvailable false, so a -no-rg run always
// failed HardPass and the old inline logic always printed RESULT: FAIL and
// exited 1 — a hard failure exit code for a run that was never meant to gate
// anything.
func TestResultOutcome_NoRGSkipIsNotAGateFailure(t *testing.T) {
	res := &parity.Result{SkipRG: true, RGAvailable: false}
	line, code := resultOutcome(res)
	if code != 0 {
		t.Fatalf("resultOutcome(-no-rg run): exit code = %d, want 0 (not a parity-gate failure)", code)
	}
	if !strings.Contains(line, "SKIP") {
		t.Fatalf("resultOutcome(-no-rg run) = %q, want it to read as skipped, not FAIL", line)
	}
}

// TestResultOutcome_RealRGUnavailabilityStillFails guards the fix from
// over-firing: if ripgrep was NOT deliberately skipped (SkipRG false) but is
// still unavailable, that is a genuine hard-gate failure and must report FAIL
// with a non-zero exit code.
func TestResultOutcome_RealRGUnavailabilityStillFails(t *testing.T) {
	res := &parity.Result{SkipRG: false, RGAvailable: false}
	line, code := resultOutcome(res)
	if code == 0 {
		t.Fatalf("resultOutcome(rg unavailable, not skipped): exit code = 0, want non-zero")
	}
	if !strings.Contains(line, "FAIL") {
		t.Fatalf("resultOutcome(rg unavailable, not skipped) = %q, want it to report FAIL", line)
	}
}

// TestResultOutcome_CleanRunPasses guards the fix from over-firing on the
// ordinary success path.
func TestResultOutcome_CleanRunPasses(t *testing.T) {
	res := &parity.Result{SkipRG: false, RGAvailable: true}
	line, code := resultOutcome(res)
	if code != 0 {
		t.Fatalf("resultOutcome(clean run): exit code = %d, want 0", code)
	}
	if !strings.Contains(line, "PASS") {
		t.Fatalf("resultOutcome(clean run) = %q, want it to report PASS", line)
	}
}
