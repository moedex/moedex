package parity

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/query"
	"moedex/internal/trigram"
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
// invocation errors, and moedex==gold throughout.
func (r *Result) HardPass() bool {
	return len(r.UnderApprox) == 0 && len(r.OverApprox) == 0 && r.RGAvailable && len(r.RGErrors) == 0
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
				// A search error here means an unexpected engine failure; record by
				// leaving moedex empty — adjudication will flag it as under-approx.
				_ = err
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
		ScanWall: scanWall, RGWall: rgWall,
	}
	res.Results = make([]QueryResult, nq)
	for i := range bat.Queries {
		qr := adjudicate(bat.Queries[i], moeSets[i], rgSets[i], goldSets[i], rgAvail)
		res.Results[i] = qr
		switch qr.Verdict {
		case VUnderApprox:
			res.UnderApprox = append(res.UnderApprox, i)
		case VOverApprox:
			res.OverApprox = append(res.OverApprox, i)
		case VEngineQuirk:
			res.EngineQuirks = append(res.EngineQuirks, i)
		}
	}
	res.MoeLatency = latency(moeDur)
	res.MoeDur = moeDur
	res.MoeAttr = moeAttr

	// --- Zoekt differential (soft).
	if !cfg.SkipZoekt {
		res.Zoekt = runZoekt(cfg, built, bat, moeSets, rgSets, goldSets, rgAvail)
	}

	res.PeakRSS = maxRSS()
	res.TotalWall = time.Since(t0)
	return res, nil
}

type blobStats struct {
	bytes int64
	lines int64
}

type shardStats struct {
	blobs      []blobStats
	totalBytes int64
	totalLines int64
}

func newShardStats(ix *index.Index) shardStats {
	stats := shardStats{blobs: make([]blobStats, ix.NumBlobs())}
	for id := 0; id < ix.NumBlobs(); id++ {
		b := ix.Blob(uint64(id))
		if b == nil {
			continue
		}
		s := blobStats{bytes: int64(len(b.Content)), lines: countLines(b.Content)}
		stats.blobs[id] = s
		stats.totalBytes += s.bytes
		stats.totalLines += s.lines
	}
	return stats
}

func (s shardStats) totals(ids []uint64) (blobs, bytes, lines int64) {
	for _, id := range ids {
		if id >= uint64(len(s.blobs)) {
			continue
		}
		bs := s.blobs[id]
		blobs++
		bytes += bs.bytes
		lines += bs.lines
	}
	return blobs, bytes, lines
}

func countLines(content []byte) int64 {
	var n int64
	eachLine(content, func(int, []byte) { n++ })
	return n
}

func attributionForQuery(ix *index.Index, stats shardStats, q Query) QueryAttribution {
	if q.Literal && !q.IgnoreCase {
		return literalAttribution(ix, stats, q.Pattern)
	}
	return regexAttribution(ix, stats, q.goRegexSource())
}

func literalAttribution(ix *index.Index, stats shardStats, pattern string) QueryAttribution {
	if len(pattern) < trigram.N {
		return QueryAttribution{
			CandidateBlobs:        int64(ix.NumBlobs()),
			CandidateBytes:        stats.totalBytes,
			CandidateLines:        stats.totalLines,
			CandidateKind:         "literal-all",
			AllCandidates:         true,
			QueryAll:              false,
			LineFilterKind:        "literal",
			LinesEnteringRE2:      0,
			LinesEnteringRE2Known: true,
			observed:              true,
		}
	}

	ids, all := literalCandidateBlobs(ix, []byte(pattern))
	if all {
		// On a selective index, a deselected begin/end gram has UNKNOWN postings:
		// the search path force-scans every blob (IndexedGram widening). Attribute
		// that here so candidate counts are honest rather than under-counted.
		return QueryAttribution{
			CandidateBlobs:        int64(ix.NumBlobs()),
			CandidateBytes:        stats.totalBytes,
			CandidateLines:        stats.totalLines,
			CandidateKind:         "literal-all",
			AllCandidates:         true,
			QueryAll:              false,
			LineFilterKind:        "literal",
			LinesEnteringRE2:      0,
			LinesEnteringRE2Known: true,
			observed:              true,
		}
	}
	blobs, bytes, lines := stats.totals(ids)
	return QueryAttribution{
		CandidateBlobs:        blobs,
		CandidateBytes:        bytes,
		CandidateLines:        lines,
		CandidateKind:         "literal-positional",
		AllCandidates:         int(blobs) == ix.NumBlobs(),
		QueryAll:              false,
		LineFilterKind:        "literal",
		LinesEnteringRE2:      0,
		LinesEnteringRE2Known: true,
		observed:              true,
	}
}

// literalCandidateBlobs computes the begin/end-gram candidate-blob set for the
// parity harness's profiling ATTRIBUTION only (CandidateBlobs/Bytes/Lines), not
// for match retrieval — actual matches come from the search package, which is
// selective-index aware.
//
// It returns (ids, all). On the default all-trigram build every gram is indexed,
// so all is always false and ids is the exact begin/end candidate set. On a
// selective build, if either the begin or end gram is DESELECTED (IndexedGram
// false) its postings are UNKNOWN — reading them directly would under-count, so
// we report all=true (every blob is a candidate, matching the search path's
// force-scan widening). This keeps attribution honest under selective indexing;
// it is still not a match-correctness path.
func literalCandidateBlobs(ix *index.Index, qb []byte) (ids []uint64, all bool) {
	begin := trigram.Trigram{qb[0], qb[1], qb[2]}
	end := trigram.Trigram{qb[len(qb)-3], qb[len(qb)-2], qb[len(qb)-1]}
	if ix.Selective() && (!ix.IndexedGram(begin) || !ix.IndexedGram(end)) {
		return nil, true
	}
	off := len(qb) - trigram.N

	begins := ix.Postings(begin)
	ends := ix.Postings(end)
	j := 0
	var out []uint64
	for _, p := range begins {
		want := p.Offset + off
		for j < len(ends) && (ends[j].Blob < p.Blob || (ends[j].Blob == p.Blob && ends[j].Offset < want)) {
			j++
		}
		if j < len(ends) && ends[j].Blob == p.Blob && ends[j].Offset == want {
			if len(out) == 0 || out[len(out)-1] != p.Blob {
				out = append(out, p.Blob)
			}
		}
	}
	return out, false
}

func regexAttribution(ix *index.Index, stats shardStats, pattern string) QueryAttribution {
	q, err := query.FromRegexp(pattern)
	if err != nil {
		return QueryAttribution{CandidateKind: "regex-error", observed: true}
	}
	ids := q.Eval(ix)
	queryAll := q.String() == "ALL"
	blobs, bytes, lines := stats.totals(ids)
	kind := "regex-trigram"
	if queryAll {
		kind = "regex-all"
	}
	return QueryAttribution{
		CandidateBlobs: blobs,
		CandidateBytes: bytes,
		CandidateLines: lines,
		CandidateKind:  kind,
		AllCandidates:  int(blobs) == ix.NumBlobs(),
		QueryAll:       queryAll,
		observed:       true,
	}
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

func runZoekt(cfg RunConfig, built *Built, bat *Battery, moeSets, rgSets, goldSets []MatchSet, rgAvail bool) *ZoektReport {
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
		if rgAvail {
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
