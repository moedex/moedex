// Command moedex-parity is the full-corpus exact-match retrieval parity gate.
//
// It indexes the entire corpus under -corpus (sharded), generates a seeded query
// battery, runs every query through moedex, ripgrep (the ground-truth oracle,
// scope-pinned to a materialized mirror of exactly the indexed file set), an
// independent in-process Go scan (the adjudicator), and optionally Zoekt (a
// competitive differential), then writes PARITY-REPORT.md and exits non-zero if
// the hard parity gate (AC-D3/AC-D4) is not met.
//
// Usage:
//
//	moedex-parity [-corpus DIR] [-seed N] [-report PATH] [flags]
package paritycmd

import (
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"moedex/internal/corpus/catalog"
	"moedex/internal/index"
	"moedex/internal/parity"
	"moedex/internal/version"
)

func Main() {
	def := os.Getenv("MOEDEX_CORPUS")
	if def == "" {
		if home, err := os.UserHomeDir(); err == nil {
			def = filepath.Join(home, catalog.DefaultCorpusDirName)
		}
	}
	corpus := flag.String("corpus", def, "corpus root (every git repo beneath it is indexed)")
	work := flag.String("work", filepath.Join(os.TempDir(), "moedex-parity"), "scratch dir for shards + content mirror")
	seed := flag.Int64("seed", 20260622, "battery seed (reproducible)")
	report := flag.String("report", "PARITY-REPORT.md", "output report path")
	floor := flag.Int("floor", 1000, "minimum battery size")
	maxRepos := flag.Int("max-repos", 0, "cap number of repos (0 = all; for quick checks)")
	shardBytes := flag.Int64("shard-bytes", parity.DefaultShardBytes, "target indexed-content bytes per shard")
	scanPar := flag.Int("scan-parallel", 0, "moedex+gold scan goroutines (0 = NumCPU)")
	rgPar := flag.Int("rg-parallel", 0, "concurrent ripgrep processes (0 = NumCPU)")
	rgThreads := flag.Int("rg-threads", 1, "rg --threads per process")
	noZoekt := flag.Bool("no-zoekt", false, "skip the Zoekt differential")
	noRG := flag.Bool("no-rg", false, "skip ripgrep (latency-profiling only; NOT a parity gate)")
	latencyCSV := flag.String("latency-csv", "", "if set, write per-query latency CSV to this path")
	zoektFileLimit := flag.Int("zoekt-file-limit", 0, "zoekt-index -file_limit (0 = zoekt default 2MB)")
	keep := flag.Bool("keep", false, "keep scratch work dir after the run")
	selective := flag.Bool("selective", false, "build the opt-in FREE-style selective trigram index (drop near-universal grams; parity-safe via IndexedGram force-scan). Proves AC-D3 holds on the selective build too.")
	gramMaxDF := flag.Float64("gram-max-df", 0.9, "with -selective: keep a trigram only if it occurs in at most this fraction of blobs (0..1)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Line("moedex-parity", false))
		return
	}

	if *corpus == "" {
		fmt.Fprintln(os.Stderr, "no corpus root: set -corpus or MOEDEX_CORPUS")
		os.Exit(2)
	}
	if fi, err := os.Stat(*corpus); err != nil || !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "corpus root %q not a directory: %v\n", *corpus, err)
		os.Exit(2)
	}

	logf := func(f string, a ...any) {
		fmt.Fprintf(os.Stderr, "[parity] "+f+"\n", a...)
	}

	var selector index.GramSelector
	if *selective {
		selector = index.FrequencyThresholdSelector{MaxDocFraction: *gramMaxDF}
		logf("selective parity gate: %s", selector.Describe())
	}

	cfg := parity.RunConfig{
		Build: parity.Config{
			Root:       *corpus,
			WorkDir:    *work,
			Seed:       *seed,
			ShardBytes: *shardBytes,
			MaxRepos:   *maxRepos,
			Selector:   selector,
		},
		Floor:          *floor,
		ScanParallel:   *scanPar,
		RGParallel:     *rgPar,
		RGThreads:      *rgThreads,
		SkipRG:         *noRG,
		SkipZoekt:      *noZoekt,
		ZoektFileLimit: *zoektFileLimit,
		Logf:           logf,
	}

	start := time.Now()
	res, err := parity.Run(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parity run failed: %v\n", err)
		os.Exit(1)
	}

	meta := parity.ReportMeta{
		Timestamp:    time.Now().Format(time.RFC3339),
		Seed:         *seed,
		Command:      "make parity",
		Machine:      fmt.Sprintf("%s/%s, %d CPU", runtime.GOOS, runtime.GOARCH, runtime.NumCPU()),
		GoVersion:    runtime.Version(),
		RGVersion:    toolVersion("rg", "--version"),
		ZoektVersion: toolVersion("zoekt-index", "-version"),
	}
	if err := parity.WriteReport(res, meta, *report); err != nil {
		fmt.Fprintf(os.Stderr, "write report: %v\n", err)
		os.Exit(1)
	}
	if *latencyCSV != "" {
		if err := writeLatencyCSV(res, *latencyCSV); err != nil {
			fmt.Fprintf(os.Stderr, "write latency csv: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "latency csv: %s\n", *latencyCSV)
		}
	}

	// Console summary.
	fmt.Printf("\n=== moedex parity ===\n")
	fmt.Printf("corpus            : %s\n", *corpus)
	fmt.Printf("repos             : %d discovered (%d .git), %d ingested, %d skipped\n",
		len(res.Built.Repos), res.Built.GitEntryCount, res.Built.IngestedRepos, len(res.Built.Skipped))
	fmt.Printf("|F| / content     : %d files, %.1f MB, %d shards\n",
		res.Built.NumFiles, float64(res.Built.ContentBytes)/1e6, len(res.Built.Shards))
	fmt.Printf("battery           : %d queries\n", len(res.Battery.Queries))
	fmt.Printf("AC-D3 under-approx: %d (real bugs)\n", len(res.UnderApprox))
	fmt.Printf("AC-D4 over-approx : %d (real bugs)\n", len(res.OverApprox))
	fmt.Printf("engine quirks     : %d (justified RE2-vs-Rust)\n", len(res.EngineQuirks))
	fmt.Printf("ripgrep           : avail=%v errors=%d\n", res.RGAvailable, len(res.RGErrors))
	for i, e := range res.RGErrors {
		if i >= 5 {
			fmt.Printf("  ... (+%d more rg errors)\n", len(res.RGErrors)-5)
			break
		}
		fmt.Printf("  rg err q#%d: %s\n", e.QueryID, e.Err)
	}
	fmt.Printf("moedex errors     : %d\n", len(res.MoeErrors))
	for i, e := range res.MoeErrors {
		if i >= 5 {
			fmt.Printf("  ... (+%d more moedex errors)\n", len(res.MoeErrors)-5)
			break
		}
		fmt.Printf("  moedex err q#%d: %s\n", e.QueryID, e.Err)
	}
	if res.Zoekt != nil {
		fmt.Printf("zoekt             : avail=%v, zoekt-missed=%d, moedex-beaten=%d\n",
			res.Zoekt.Available, len(res.Zoekt.ZoektMissed), len(res.Zoekt.MoedexBeaten))
	}
	fmt.Printf("build peak RSS    : %.1f MB; run peak RSS: %.1f MB\n",
		float64(res.Built.BuildPeakRSS)/1e6, float64(res.PeakRSS)/1e6)
	fmt.Printf("wall              : build %s, scan %s, rg %s, total %s\n",
		res.Built.BuildWall.Round(time.Millisecond), res.ScanWall.Round(time.Millisecond),
		res.RGWall.Round(time.Millisecond), time.Since(start).Round(time.Millisecond))
	fmt.Printf("report            : %s\n", *report)

	if res.SkipRG {
		if !*keep {
			_ = os.RemoveAll(*work)
		}
	} else if !*keep && res.HardPass() {
		_ = os.RemoveAll(*work)
	} else if !res.HardPass() {
		fmt.Fprintf(os.Stderr, "scratch kept for debugging: %s\n", *work)
	}

	line, code := resultOutcome(res)
	fmt.Println(line)
	if code != 0 {
		os.Exit(code)
	}
}

// resultOutcome decides the final RESULT line and process exit code. HardPass
// requires RGAvailable, which is also false for a deliberate -no-rg run
// (latency-profiling only; see RunConfig.SkipRG) — so SkipRG must be checked
// first, or a -no-rg run would always read as a parity-gate FAIL with a
// non-zero exit code even though it never ran ripgrep on purpose.
func resultOutcome(res *parity.Result) (line string, exitCode int) {
	if res.SkipRG {
		return "RESULT: SKIPPED (latency-only, -no-rg: not a parity gate)", 0
	}
	if res.HardPass() {
		return "RESULT: PASS", 0
	}
	return "RESULT: FAIL (see PARITY-REPORT.md)", 1
}

// writeLatencyCSV dumps per-query moedex search latency and attribution, sorted
// slowest-first.
func writeLatencyCSV(res *parity.Result, path string) error {
	if len(res.MoeDur) != len(res.Results) {
		return fmt.Errorf("per-query timing unavailable (%d durs, %d results)", len(res.MoeDur), len(res.Results))
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	cw := csv.NewWriter(f)
	defer cw.Flush()
	_ = cw.Write([]string{
		"query_id", "bucket", "literal", "ignorecase", "nmoe", "nanos",
		"candidate_kind", "candidate_blobs", "candidate_bytes", "candidate_lines",
		"all_candidates", "query_all", "line_filter_kind", "lines_entering_re2", "verify_workers",
		"pattern",
	})

	idx := make([]int, len(res.MoeDur))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(i, j int) bool { return res.MoeDur[idx[i]] > res.MoeDur[idx[j]] })
	hasAttr := len(res.MoeAttr) == len(res.Results)
	for _, i := range idx {
		qr := res.Results[i]
		var attr parity.QueryAttribution
		linesEnteringRE2 := ""
		if hasAttr {
			attr = res.MoeAttr[i]
			if attr.LinesEnteringRE2Known {
				linesEnteringRE2 = strconv.FormatInt(attr.LinesEnteringRE2, 10)
			}
		}
		rec := []string{
			strconv.Itoa(qr.Q.ID),
			string(qr.Q.Bucket),
			strconv.FormatBool(qr.Q.Literal),
			strconv.FormatBool(qr.Q.IgnoreCase),
			strconv.Itoa(qr.NMoe),
			strconv.FormatInt(res.MoeDur[i].Nanoseconds(), 10),
			attr.CandidateKind,
			formatAttrInt(hasAttr, attr.CandidateBlobs),
			formatAttrInt(hasAttr, attr.CandidateBytes),
			formatAttrInt(hasAttr, attr.CandidateLines),
			formatAttrBool(hasAttr, attr.AllCandidates),
			formatAttrBool(hasAttr, attr.QueryAll),
			attr.LineFilterKind,
			linesEnteringRE2,
			formatAttrInt(hasAttr, int64(attr.VerifyWorkers)),
			qr.Q.Pattern,
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	return cw.Error()
}

func formatAttrInt(ok bool, n int64) string {
	if !ok {
		return ""
	}
	return strconv.FormatInt(n, 10)
}

func formatAttrBool(ok bool, v bool) string {
	if !ok {
		return ""
	}
	return strconv.FormatBool(v)
}

func toolVersion(bin string, arg string) string {
	out, err := exec.Command(bin, arg).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
}
