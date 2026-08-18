package parity

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"moedex/internal/diskstore"
)

// RunConfig configures a full parity run.
type RunConfig struct {
	Build          Config
	Floor          int  // battery size floor (AC-D2 requires ≥1000)
	ScanParallel   int  // goroutines for moedex+gold scan per shard
	RGParallel     int  // concurrent ripgrep processes
	RGThreads      int  // rg --threads per process (0 = rg auto)
	SkipRG         bool // skip ripgrep entirely (latency profiling only; NOT a parity run)
	SkipZoekt      bool
	ZoektFileLimit int // zoekt-index -file_limit (0 = zoekt default 2MB)
	Logf           func(string, ...any)
}

func (c *RunConfig) logf(f string, a ...any) {
	if c.Logf != nil {
		c.Logf(f, a...)
	}
}

// Result is everything a parity run produces, enough to render the report and
// decide the hard-gate exit code.
type Result struct {
	Built   *Built
	Battery *Battery
	Results []QueryResult

	UnderApprox  []int // query IDs with a real under-approximation (AC-D3 fail)
	OverApprox   []int // query IDs with a real over-approximation  (AC-D4 fail)
	EngineQuirks []int // query IDs that diverge from rg but match gold (justified)

	RGAvailable bool
	RGErrors    []RGError
	// SkipRG records whether ripgrep was deliberately skipped (-no-rg), as
	// opposed to attempted and unavailable. A SkipRG run is latency-profiling
	// only and never meets HardPass (RGAvailable is false either way) — callers
	// must check SkipRG before treating a failed HardPass as a real gate FAIL.
	SkipRG bool

	MoeErrors []MoeError

	Zoekt *ZoektReport

	ScanWall   time.Duration
	RGWall     time.Duration
	TotalWall  time.Duration
	PeakRSS    int64
	MoeLatency LatencyStats
	MoeDur     []time.Duration // per-query moedex search time (indexed by query ID)
	MoeAttr    []QueryAttribution
}

// RGError records a ripgrep invocation failure for a query.
type RGError struct {
	QueryID int
	Err     string
}

// MoeError records a moedex search failure for a query during the scan phase
// (e.g. a regex compile error, a recovered panic, or a future
// error-before-results path). Any entry means moedex did not actually produce
// a verified result for that query — an empty match set in that case is not
// evidence of "no matches," so HardPass must not pass regardless of how the
// (empty) moedex set adjudicated against gold/rg.
type MoeError struct {
	QueryID int
	Err     string
}

// LatencyStats holds moedex per-query search-latency percentiles (soft).
type LatencyStats struct {
	P50, P95, Max time.Duration
}

// QueryAttribution records per-query work that can be observed without changing
// search behavior. Candidate counts come from the same index/query primitives the
// search package uses. Verifier-internal fields remain unset until search exposes
// them.
type QueryAttribution struct {
	CandidateBlobs int64
	CandidateBytes int64
	CandidateLines int64

	CandidateKind string
	AllCandidates bool
	QueryAll      bool

	LineFilterKind        string
	LinesEnteringRE2      int64
	LinesEnteringRE2Known bool
	VerifyWorkers         int

	observed bool
}

func (a *QueryAttribution) addShard(s QueryAttribution) {
	a.CandidateBlobs += s.CandidateBlobs
	a.CandidateBytes += s.CandidateBytes
	a.CandidateLines += s.CandidateLines
	a.LinesEnteringRE2 += s.LinesEnteringRE2
	if s.VerifyWorkers > a.VerifyWorkers {
		a.VerifyWorkers = s.VerifyWorkers
	}

	if !a.observed {
		a.CandidateKind = s.CandidateKind
		a.AllCandidates = s.AllCandidates
		a.QueryAll = s.QueryAll
		a.LineFilterKind = s.LineFilterKind
		a.LinesEnteringRE2Known = s.LinesEnteringRE2Known
		a.VerifyWorkers = s.VerifyWorkers
		a.observed = true
		return
	}

	a.AllCandidates = a.AllCandidates && s.AllCandidates
	a.QueryAll = a.QueryAll && s.QueryAll
	if a.CandidateKind != s.CandidateKind {
		a.CandidateKind = "mixed"
	}
	if a.LineFilterKind != s.LineFilterKind {
		a.LineFilterKind = ""
	}
	a.LinesEnteringRE2Known = a.LinesEnteringRE2Known && s.LinesEnteringRE2Known
}

// HardPass reports whether the run meets the hard parity gate: no real
// under-approximation, no real over-approximation, ripgrep available with no
// invocation errors, no moedex search errors, and moedex==gold throughout.
//
// The RGAvailable check here is load-bearing beyond its own name: adjudicate
// (compare.go) assigns VOK when rg is unavailable, based on moedex==gold
// alone, with no actual ripgrep comparison. That per-query VOK is only safe
// because RGAvailable is run-wide and checked here — it can't slip through as
// a hard pass with zero rg coverage.
func (r *Result) HardPass() bool {
	return len(r.UnderApprox) == 0 && len(r.OverApprox) == 0 && r.RGAvailable && len(r.RGErrors) == 0 &&
		len(r.MoeErrors) == 0
}

// Run executes the whole pipeline. It does not write the report; the caller does
// that (and decides the exit code from HardPass).
func Run(cfg RunConfig) (*Result, error) {
	if cfg.Floor == 0 {
		cfg.Floor = 1000
	}
	if cfg.ScanParallel <= 0 {
		cfg.ScanParallel = runtime.NumCPU()
	}
	if cfg.RGParallel <= 0 {
		cfg.RGParallel = runtime.NumCPU()
	}
	cfg.Build.Logf = cfg.Logf
	t0 := time.Now()

	built, err := Build(cfg.Build)
	if err != nil {
		return nil, err
	}
	bat := Generate(built.Pool, cfg.Build.Seed)
	if err := bat.AssertBuckets(cfg.Floor); err != nil {
		return nil, fmt.Errorf("battery invalid: %w", err) // fail fast (AC-D2)
	}
	cfg.logf("battery: %d queries across %d buckets", len(bat.Queries), len(AllBuckets))

	nq := len(bat.Queries)
	moe := make([]accum, nq)
	gold := make([]accum, nq)
	moeDur := make([]time.Duration, nq)
	moeAttr := make([]QueryAttribution, nq)
	matchers := make([]matcher, nq)
	for i := range bat.Queries {
		matchers[i] = goldMatcher(bat.Queries[i])
	}

	// --- scan phase: moedex + gold over each shard (shard-outer, query-parallel).
	var moeErrs []MoeError
	var moeErrMu sync.Mutex
	scanStart := time.Now()
	for si, path := range built.Shards {
		ix, closer, err := diskstore.LoadMmap(path)
		if err != nil {
			return nil, fmt.Errorf("load shard %s: %w", path, err)
		}
		parallelFor(nq, cfg.ScanParallel, func(i int) {
			st := time.Now()
			attr, err := moedexInto(&moe[i], ix, bat.Queries[i], built.FT)
			if err != nil {
				// An unexpected engine failure: an empty moedex set here is not
				// evidence of "no matches," so record it instead of letting
				// adjudication silently read it as agreement with an empty gold set.
				moeErrMu.Lock()
				moeErrs = append(moeErrs, MoeError{QueryID: i, Err: err.Error()})
				moeErrMu.Unlock()
				cfg.logf("  query %d (%s): moedex search error: %v", i, bat.Queries[i], err)
			}
			moeDur[i] += time.Since(st)
			goldInto(&gold[i], ix, matchers[i], built.FT)
			moeAttr[i].addShard(attr)
		})
		_ = closer.Close()
		ix = nil
		runtime.GC()
		cfg.logf("  scanned shard %d/%d", si+1, len(built.Shards))
	}
	moeSets := make([]MatchSet, nq)
	goldSets := make([]MatchSet, nq)
	for i := range moe {
		moeSets[i] = moe[i].finalize()
		goldSets[i] = gold[i].finalize()
	}
	moe = nil
	gold = nil
	runtime.GC()
	scanWall := time.Since(scanStart)

	// --- ripgrep phase over the materialized mirror (= exactly F).
	rgSets := make([]MatchSet, nq)
	// rgFailed[i] marks a query whose own rg.run() call errored (e.g.
	// exhausted its retries) even though ripgrep overall is available. It is
	// distinct from rgAvail (the run-wide "rg process could be spawned at
	// all" flag): without tracking it separately, a per-query rg.run()
	// failure would leave rgSets[i] at its nil zero value, which
	// adjudicate/runZoekt would then misread as "ripgrep found zero matches"
	// truth rather than "ripgrep could not be asked for this query" (F-30).
	rgFailed := make([]bool, nq)
	var rgErrs []RGError
	var rgMu sync.Mutex
	rgAvail := false
	rgStart := time.Now()
	if cfg.SkipRG {
		cfg.logf("ripgrep skipped (-no-rg): latency-profiling run, NOT a parity gate")
	} else if rg, err := newRipgrep(built.MirrorDir, cfg.RGThreads); err == nil {
		rgAvail = true
		parallelFor(nq, cfg.RGParallel, func(i int) {
			set, e := rg.run(bat.Queries[i], built.FT)
			if e != nil {
				rgMu.Lock()
				rgErrs = append(rgErrs, RGError{QueryID: i, Err: e.Error()})
				rgMu.Unlock()
				rgFailed[i] = true
				return
			}
			rgSets[i] = set
		})
	} else {
		cfg.logf("ripgrep unavailable: %v", err)
	}
	rgWall := time.Since(rgStart)

	// --- adjudicate.
	res := &Result{
		Built: built, Battery: bat, RGAvailable: rgAvail, RGErrors: rgErrs,
		MoeErrors: moeErrs, SkipRG: cfg.SkipRG,
		ScanWall: scanWall, RGWall: rgWall,
	}
	res.Results, res.UnderApprox, res.OverApprox, res.EngineQuirks =
		adjudicateAll(bat.Queries, moeSets, rgSets, goldSets, rgAvail, rgFailed)
	res.MoeLatency = latency(moeDur)
	res.MoeDur = moeDur
	res.MoeAttr = moeAttr

	// --- Zoekt differential (soft).
	if !cfg.SkipZoekt {
		res.Zoekt = runZoekt(cfg, built, bat, moeSets, rgSets, goldSets, rgAvail, rgFailed)
	}

	res.PeakRSS = maxRSS()
	res.TotalWall = time.Since(t0)
	return res, nil
}

// adjudicateAll adjudicates every query in the battery against moedex,
// ripgrep, and gold, and buckets the resulting verdicts into
// UnderApprox/OverApprox/EngineQuirks query-ID slices.
//
// rgAvail is the run-wide "the rg process itself could be spawned" flag;
// rgFailed marks the query IDs whose own rg.run() call errored (e.g.
// exhausted its retries) even though rg is available overall. Ripgrep is
// only treated as a real per-query comparison point when both hold —
// otherwise a query whose individual rg invocation failed would have its
// empty (zero-value) rgSets[i] misread as "ripgrep found zero matches,"
// which can misclassify a genuine tool-error query as a "Justified engine
// quirk" (F-30) instead of excluding it from that classification the way a
// run-wide rg outage already does.
func adjudicateAll(queries []Query, moeSets, rgSets, goldSets []MatchSet, rgAvail bool, rgFailed []bool) (results []QueryResult, under, over, quirks []int) {
	results = make([]QueryResult, len(queries))
	for i, q := range queries {
		qr := adjudicate(q, moeSets[i], rgSets[i], goldSets[i], rgAvail && !rgFailed[i])
		results[i] = qr
		switch qr.Verdict {
		case VUnderApprox:
			under = append(under, i)
		case VOverApprox:
			over = append(over, i)
		case VEngineQuirk:
			quirks = append(quirks, i)
		}
	}
	return results, under, over, quirks
}

// ZoektReport holds the competitive differential outcome.
type ZoektReport struct {
	Available    bool
	Notes        []string
	PerBucket    map[Bucket]*ZBucketStat
	MoedexBeaten []int // query IDs: moedex missed truth but Zoekt did not (HIGH)
	ZoektMissed  []int // query IDs: Zoekt missed truth but moedex did not (expected)
}

// ZBucketStat aggregates per-category coverage of ground truth.
type ZBucketStat struct {
	Queries           int
	MoedexCoversTruth int
	ZoektCoversTruth  int
	ZoektSkipped      int
}

func runZoekt(cfg RunConfig, built *Built, bat *Battery, moeSets, rgSets, goldSets []MatchSet, rgAvail bool, rgFailed []bool) *ZoektReport {
	idxDir := filepath.Join(cfg.Build.WorkDir, "zoekt-index")
	z := setupZoekt(built.MirrorDir, idxDir, cfg.ZoektFileLimit, cfg.Logf)
	rep := &ZoektReport{Available: z.available, Notes: z.notes, PerBucket: map[Bucket]*ZBucketStat{}}
	if !z.available {
		return rep
	}
	for _, bk := range AllBuckets {
		if zoektBuckets[bk] {
			rep.PerBucket[bk] = &ZBucketStat{}
		}
	}
	// Indices of the battery queries Zoekt is asked about.
	var ids []int
	for i, q := range bat.Queries {
		if zoektBuckets[q.Bucket] {
			ids = append(ids, i)
		}
	}
	par := cfg.RGParallel
	if par <= 0 {
		par = runtime.NumCPU()
	}
	var mu sync.Mutex
	parallelFor(len(ids), par, func(k int) {
		i := ids[k]
		q := bat.Queries[i]
		truth := goldSets[i]
		if rgAvail && !rgFailed[i] {
			truth = rgSets[i]
		}
		truthFiles := fileSetOf(truth)
		moedexFiles := fileSetOf(moeSets[i])
		moedexCovers := covers(moedexFiles, truthFiles)
		zoektFiles, ok := z.fileSet(q, built.FT)

		mu.Lock()
		defer mu.Unlock()
		stat := rep.PerBucket[q.Bucket]
		stat.Queries++
		// moedex's truth coverage is tallied for every query in the bucket,
		// independent of whether Zoekt could answer it.
		if moedexCovers {
			stat.MoedexCoversTruth++
		}
		if !ok {
			stat.ZoektSkipped++
			return
		}
		zoektCovers := coversMap(zoektFiles, truthFiles)
		if zoektCovers {
			stat.ZoektCoversTruth++
		}
		if moedexCovers && !zoektCovers {
			rep.ZoektMissed = append(rep.ZoektMissed, i)
		}
		if !moedexCovers && zoektCovers {
			rep.MoedexBeaten = append(rep.MoedexBeaten, i)
		}
	})
	sort.Ints(rep.ZoektMissed)
	sort.Ints(rep.MoedexBeaten)
	return rep
}

func fileSetOf(s MatchSet) map[int]bool {
	out := make(map[int]bool, len(s))
	for _, v := range s {
		fid, _ := unpack(v)
		out[fid] = true
	}
	return out
}

// covers reports whether every fileID in want is in have (both as sets).
func covers(have, want map[int]bool) bool {
	for f := range want {
		if !have[f] {
			return false
		}
	}
	return true
}
func coversMap(have, want map[int]bool) bool { return covers(have, want) }

// parallelFor runs fn(i) for i in [0,n) using at most workers goroutines.
func parallelFor(n, workers int, fn func(i int)) {
	if workers < 1 {
		workers = 1
	}
	if workers > n {
		workers = n
	}
	if n == 0 {
		return
	}
	var next int64 = 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				i := int(next)
				next++
				mu.Unlock()
				if i >= n {
					return
				}
				fn(i)
			}
		}()
	}
	wg.Wait()
}

func latency(d []time.Duration) LatencyStats {
	if len(d) == 0 {
		return LatencyStats{}
	}
	c := append([]time.Duration(nil), d...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	pick := func(p float64) time.Duration {
		idx := int(p * float64(len(c)))
		if idx >= len(c) {
			idx = len(c) - 1
		}
		return c[idx]
	}
	return LatencyStats{P50: pick(0.50), P95: pick(0.95), Max: c[len(c)-1]}
}
