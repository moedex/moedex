package parity

import "testing"

// TestAdjudicateVerdicts locks in adjudicate's observable classification
// (Verdict, RGMinusMoe, MoeMinusRG) across its branches as a characterization
// test before removing the dead RGMissReal/RGMissQuirk attribution fields
// (F-048): RGMinusMoe is disjoint from gold whenever moe==gold, so any
// "ripgrep miss that's also a gold hit" can never occur in that branch — a
// real miss is already caught earlier as VUnderApprox.
func TestAdjudicateVerdicts(t *testing.T) {
	pack2 := func(file, line int) uint64 { return pack(file, line) }

	tests := []struct {
		name    string
		moe     MatchSet
		rg      MatchSet
		gold    MatchSet
		rgAvail bool
		want    Verdict
	}{
		{
			name:    "all agree",
			moe:     MatchSet{pack2(1, 1)},
			rg:      MatchSet{pack2(1, 1)},
			gold:    MatchSet{pack2(1, 1)},
			rgAvail: true,
			want:    VOK,
		},
		{
			name:    "rg unavailable but moe==gold",
			moe:     MatchSet{pack2(1, 1)},
			rg:      nil,
			gold:    MatchSet{pack2(1, 1)},
			rgAvail: false,
			want:    VOK,
		},
		{
			name:    "moe==gold, rg diverges (engine quirk)",
			moe:     MatchSet{pack2(1, 1)},
			rg:      MatchSet{pack2(1, 1), pack2(2, 2)},
			gold:    MatchSet{pack2(1, 1)},
			rgAvail: true,
			want:    VEngineQuirk,
		},
		{
			name:    "gold has a match moe missed (under-approx)",
			moe:     nil,
			rg:      MatchSet{pack2(1, 1)},
			gold:    MatchSet{pack2(1, 1)},
			rgAvail: true,
			want:    VUnderApprox,
		},
		{
			name:    "moe has a match gold lacks (over-approx)",
			moe:     MatchSet{pack2(1, 1)},
			rg:      nil,
			gold:    nil,
			rgAvail: true,
			want:    VOverApprox,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := adjudicate(Query{Pattern: "x"}, tc.moe, tc.rg, tc.gold, tc.rgAvail)
			if r.Verdict != tc.want {
				t.Fatalf("Verdict = %v, want %v", r.Verdict, tc.want)
			}
		})
	}
}

// TestAdjudicateEngineQuirkDivergence checks the RGMinusMoe/MoeMinusRG sets
// populated on the moe==gold-but-rg-diverges path, since those are the fields
// the report actually reads (writeQuirks in report.go) — unlike the dead
// RGMissReal/RGMissQuirk attribution this test deliberately does not assert.
func TestAdjudicateEngineQuirkDivergence(t *testing.T) {
	moe := MatchSet{pack(1, 1)}
	rg := MatchSet{pack(1, 1), pack(2, 2)}
	gold := MatchSet{pack(1, 1)}

	r := adjudicate(Query{Pattern: "x"}, moe, rg, gold, true)
	if r.Verdict != VEngineQuirk {
		t.Fatalf("Verdict = %v, want VEngineQuirk", r.Verdict)
	}
	if len(r.RGMinusMoe) != 1 || r.RGMinusMoe[0] != pack(2, 2) {
		t.Fatalf("RGMinusMoe = %v, want [pack(2,2)]", r.RGMinusMoe)
	}
	if len(r.MoeMinusRG) != 0 {
		t.Fatalf("MoeMinusRG = %v, want empty", r.MoeMinusRG)
	}
}
