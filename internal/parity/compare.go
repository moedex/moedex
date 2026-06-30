package parity

import "sort"

// A match is packed as (fileID<<32 | line). fileID < 2^32 (|F| is small) and a
// 1-based line number fits in 32 bits.
func pack(fileID, line int) uint64 { return uint64(uint32(fileID))<<32 | uint64(uint32(line)) }
func unpack(v uint64) (fileID, line int) {
	return int(v >> 32), int(uint32(v))
}

// accum collects matches for one query (possibly across several shards) before
// being finalized into a sorted, de-duplicated MatchSet.
type accum struct{ vals []uint64 }

func (a *accum) add(fileID, line int) { a.vals = append(a.vals, pack(fileID, line)) }

func (a *accum) finalize() MatchSet {
	if len(a.vals) == 0 {
		return nil
	}
	sort.Slice(a.vals, func(i, j int) bool { return a.vals[i] < a.vals[j] })
	out := a.vals[:1]
	for _, v := range a.vals[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return MatchSet(out)
}

// MatchSet is a sorted, de-duplicated set of packed matches.
type MatchSet []uint64

func (s MatchSet) Len() int { return len(s) }

// minus returns the elements of s not in t (both sorted).
func minus(s, t MatchSet) []uint64 {
	var out []uint64
	i, j := 0, 0
	for i < len(s) {
		if j >= len(t) || s[i] < t[j] {
			out = append(out, s[i])
			i++
		} else if s[i] == t[j] {
			i++
			j++
		} else {
			j++
		}
	}
	return out
}

func equalSets(s, t MatchSet) bool {
	if len(s) != len(t) {
		return false
	}
	for i := range s {
		if s[i] != t[i] {
			return false
		}
	}
	return true
}

// Verdict classifies a query's outcome after adjudication.
type Verdict int

const (
	// VOK: moedex == ripgrep exactly.
	VOK Verdict = iota
	// VEngineQuirk: moedex != ripgrep, but moedex == gold (the independent Go
	// regexp/literal scan). The divergence is purely an RE2-vs-Rust-regex
	// semantics difference, not a retrieval error. Justified; reported.
	VEngineQuirk
	// VUnderApprox: gold has a match moedex missed — a REAL under-approximation
	// (the AC-D3 invariant violated). Fix the engine.
	VUnderApprox
	// VOverApprox: moedex has a match gold lacks — a REAL verifier false positive
	// (should be impossible: moedex's blobs are a subset of gold's and both apply
	// the same verification). If seen, a deep bug (e.g. line-number mismatch).
	VOverApprox
)

func (v Verdict) String() string {
	switch v {
	case VOK:
		return "OK"
	case VEngineQuirk:
		return "ENGINE-QUIRK"
	case VUnderApprox:
		return "UNDER-APPROX(BUG)"
	case VOverApprox:
		return "OVER-APPROX(BUG)"
	}
	return "?"
}

// QueryResult holds one query's match sets and adjudicated verdict.
type QueryResult struct {
	Q     Query
	NMoe  int // |moedex|
	NRG   int // |ripgrep|
	NGold int // |gold|

	// Diagnostics (sampled in the report, counts always exact).
	GoldMinusMoe []uint64 // gold \ moedex  — real under-approx (AC-D3)
	MoeMinusGold []uint64 // moedex \ gold  — real over-approx  (AC-D4)
	RGMinusMoe   []uint64 // ripgrep \ moedex
	MoeMinusRG   []uint64 // moedex \ ripgrep

	Verdict Verdict
	RGAvail bool
}

// adjudicate compares moedex against ripgrep, using gold to classify any
// divergence. rg may be nil (Zoekt/ripgrep unavailable for this query).
func adjudicate(q Query, moe, rg, gold MatchSet, rgAvail bool) QueryResult {
	r := QueryResult{Q: q, NMoe: moe.Len(), NRG: rg.Len(), NGold: gold.Len(), RGAvail: rgAvail}

	r.GoldMinusMoe = minus(gold, moe) // missed Go-true matches → under-approx
	r.MoeMinusGold = minus(moe, gold) // false positives vs Go truth

	switch {
	case len(r.GoldMinusMoe) > 0:
		r.Verdict = VUnderApprox
	case len(r.MoeMinusGold) > 0:
		r.Verdict = VOverApprox
	default:
		// moedex == gold. Now classify vs ripgrep.
		if rgAvail {
			r.RGMinusMoe = minus(rg, moe)
			r.MoeMinusRG = minus(moe, rg)
			if len(r.RGMinusMoe) == 0 && len(r.MoeMinusRG) == 0 {
				r.Verdict = VOK
			} else {
				r.Verdict = VEngineQuirk
			}
		} else {
			// rg unavailable: moedex==gold is the best truth available for this
			// query alone, with no ripgrep comparison. That's only safe because
			// Result.HardPass (run.go) requires RGAvailable==true run-wide, so a
			// run where this branch fires already fails the hard gate regardless
			// of this VOK. If rg availability ever became per-query, this verdict
			// would need its own gate rather than relying on the run-wide guard.
			r.Verdict = VOK
		}
	}
	return r
}
