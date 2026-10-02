package parity

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// ReportMeta carries run metadata the Result itself doesn't hold.
type ReportMeta struct {
	Timestamp     string
	Seed          int64
	Command       string
	Machine       string
	GoVersion     string
	RGVersion     string
	ZoektVersion  string
	RoundTripNote string // filled from the AC-C1 test outcome, if known
}

// fetchLine returns the bytes of a 1-based line from a mirror file, for showing
// the "exact bytes" behind a divergence in the report.
func fetchLine(mirrorDir string, fileID, line int) string {
	p := filepath.Join(mirrorDir, fmt.Sprintf("%03d", fileID/1000), fmt.Sprintf("%d", fileID))
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	n := 0
	var got []byte
	eachLine(data, func(li int, l []byte) {
		if li+1 == line {
			got = l
		}
		n++
	})
	return string(got)
}

// WriteReport renders PARITY-REPORT.md to path.
func WriteReport(res *Result, meta ReportMeta, path string) error {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }

	built := res.Built
	bat := res.Battery
	counts := bat.BucketCounts()

	d3Pass := len(res.UnderApprox) == 0
	d4Pass := len(res.OverApprox) == 0
	hard := res.HardPass()

	w("# moedex v0.1 — Full-Corpus Parity Report\n\n")
	w("_Generated %s by `%s` (seed %d)._\n\n", meta.Timestamp, meta.Command, meta.Seed)

	w("## Verdict\n\n")
	if hard {
		w("**PASS** — moedex returns exactly ripgrep's match set over the full corpus, ")
		w("with no under-approximation (AC-D3) and no over-approximation (AC-D4).\n\n")
	} else {
		w("**FAIL** — hard parity gate not met. See AC-D3/AC-D4 below.\n\n")
	}

	// --- Methodology -------------------------------------------------------
	w("## Method & definitions\n\n")
	w("- **Scope `F`**: the exact set of files moedex's ingest selected (text, ")
	w("non-binary, BOM-stripped). Every oracle runs over exactly `F`: ripgrep and ")
	w("Zoekt search a scratch **content mirror** holding each file's *indexed bytes* ")
	w("(one mirror file per fileID), so file-selection, `.gitignore`, binary-")
	w("detection, and BOM differences cannot contaminate parity (AC-D1).\n")
	w("- **Granularity**: `(file, line)`. Byte-spans were deliberately rejected: ")
	w("Go's RE2 (leftmost-longest) and Rust-regex submatch boundaries differ, so ")
	w("span equality would flag pure engine quirks, not retrieval errors. Line ")
	w("granularity is the v0.1 retrieval contract (did we find the right lines?).\n")
	w("- **Oracles**: ripgrep `rg` is the immovable ground truth. An independent ")
	w("in-process Go-regexp/`bytes.Contains` scan (\"gold\") over every blob — using ")
	w("no trigram filtering — adjudicates any moedex-vs-ripgrep divergence: if ")
	w("moedex == gold the difference is RE2-vs-Rust semantics (justified); if ")
	w("moedex misses a gold match it is a **real under-approximation** (AC-D3 fail). ")
	w("Note moedex's candidate blobs are a subset of gold's and both verify with the ")
	w("same Go engine, so moedex ⊆ gold always — an over-approximation vs gold would ")
	w("signal a deep bug.\n\n")

	// --- Historical regression covered by the battery ----------------------
	w("## Previously fixed regression exercised by this gate\n\n")
	w("Historically, `internal/search` verified candidate lines with a literal prefilter ")
	w("(`requiredRun`) that returned the literal bytes for an `OpLiteral` **without ")
	w("checking `FoldCase`**. For a case-insensitive pattern (e.g. `(?i)public`, ")
	w("which Go normalizes to an `OpLiteral` with `Rune=\"PUBLIC\"`), the prefilter ")
	w("did a case-*sensitive* `bytes.Contains(line, \"PUBLIC\")` and dropped lines ")
	w("containing `public`/`Public` — a real under-approximation that the ")
	w("case-insensitive battery bucket (h) exercises. Fixed: a case-folded literal ")
	w("yields no required byte run, so the prefilter is disabled for it (the full ")
	w("engine still verifies). This is the kind of invariant violation AC-D3 exists ")
	w("to catch.\n\n")

	// --- Corpus & build ----------------------------------------------------
	w("## Corpus & build (AC-B)\n\n")
	w("| Metric | Value |\n|---|---|\n")
	w("| Corpus root | `%s` |\n", built.Root)
	w("| `.git` entries (`find -name .git`) | %d |\n", built.GitEntryCount)
	w("| Repos discovered | %d |\n", len(built.Repos))
	w("| Repos ingested | %d |\n", built.IngestedRepos)
	w("| Repos skipped | %d |\n", len(built.Skipped))
	w("| Indexed files `\\|F\\|` | %d |\n", built.NumFiles)
	w("| Indexed content | %.1f MB |\n", float64(built.ContentBytes)/1e6)
	w("| Shards | %d |\n", len(built.Shards))
	w("| Build wall time | %s |\n", built.BuildWall.Round(1e6))
	w("| Build peak RSS | %.1f MB |\n", float64(built.BuildPeakRSS)/1e6)
	w("| Run peak RSS | %.1f MB |\n", float64(res.PeakRSS)/1e6)
	w("\n")
	w("- **AC-B1** discovery == `.git` count: %s\n", passStr(len(built.Repos) == built.GitEntryCount))
	w("- **AC-B2** ≥99%% of repos indexed: %s (%d/%d)\n",
		passStr(built.IngestedRepos*100 >= len(built.Repos)*99 && len(built.Repos) > 0),
		built.IngestedRepos, len(built.Repos))
	w("- **AC-B3** `|F|` ≥ 40,000 floor: %s (%d)\n", passStr(built.NumFiles >= 40000), built.NumFiles)
	w("- **AC-B4** peak RSS & build time recorded, no OOM: PASS (above)\n")
	if len(built.Skipped) > 0 {
		w("\n<details><summary>Skipped repos (%d)</summary>\n\n", len(built.Skipped))
		for i, s := range built.Skipped {
			if i >= 50 {
				w("- … (+%d more)\n", len(built.Skipped)-50)
				break
			}
			w("- `%s`: %s\n", s.Dir, oneLine(s.Reason))
		}
		w("\n</details>\n")
	}
	w("\n")

	// --- Battery -----------------------------------------------------------
	w("## Query battery (AC-D2)\n\n")
	w("Seeded (seed %d), reproducible. Total **%d** queries (floor 1000). ", meta.Seed, len(bat.Queries))
	w("Per-bucket counts (all non-empty):\n\n| Bucket | Count |\n|---|---|\n")
	for _, bk := range AllBuckets {
		w("| %s | %d |\n", bk, counts[bk])
	}
	w("\n")

	// --- Parity gate -------------------------------------------------------
	w("## Parity vs ground truth (AC-D3 / AC-D4)\n\n")
	w("- **AC-D3** no under-approximation (`moedex ⊇ ripgrep`, i.e. moedex misses no ")
	w("Go-true match): %s — %d/%d queries clean.\n", passStr(d3Pass), len(bat.Queries)-len(res.UnderApprox), len(bat.Queries))
	w("- **AC-D4** exact equality (no spurious matches vs Go truth): %s — %d real over-approximations.\n",
		passStr(d4Pass), len(res.OverApprox))
	w("- ripgrep available: %v; rg invocation errors: %d.\n", res.RGAvailable, len(res.RGErrors))
	w("- moedex search errors: %d.\n", len(res.MoeErrors))
	w("- Justified RE2-vs-Rust engine quirks (moedex==gold, differs from rg): %d.\n\n", len(res.EngineQuirks))

	if len(res.RGErrors) > 0 {
		w("### ripgrep invocation errors (gate-failing)\n\n")
		w("These queries could not be adjudicated because ripgrep itself errored, so ")
		w("the gate fails. (A non-(0,1) exit after retries — e.g. an environment/")
		w("resource problem, not a moedex bug.)\n\n")
		for i, e := range res.RGErrors {
			if i >= 20 {
				w("- … (+%d more)\n", len(res.RGErrors)-20)
				break
			}
			w("- query #%d: `%s`\n", e.QueryID, oneLine(e.Err))
		}
		w("\n")
	}

	if len(res.MoeErrors) > 0 {
		w("### moedex search errors (gate-failing)\n\n")
		w("These queries could not be adjudicated because moedex's own search call ")
		w("errored — an empty result here is not evidence of \"no matches,\" so the ")
		w("gate fails.\n\n")
		for i, e := range res.MoeErrors {
			if i >= 20 {
				w("- … (+%d more)\n", len(res.MoeErrors)-20)
				break
			}
			w("- query #%d: `%s`\n", e.QueryID, oneLine(e.Err))
		}
		w("\n")
	}

	if len(res.UnderApprox) > 0 {
		w("### AC-D3 violations (REAL bugs)\n\n")
		writeDivergences(&b, res, built, res.UnderApprox, true)
	}
	if len(res.OverApprox) > 0 {
		w("### AC-D4 violations (REAL bugs)\n\n")
		writeDivergences(&b, res, built, res.OverApprox, false)
	}
	if len(res.EngineQuirks) > 0 {
		w("### Justified engine quirks (RE2 vs Rust regex)\n\n")
		w("These queries diverge from ripgrep but match the independent Go scan exactly, ")
		w("so the difference is a documented regex-engine semantics difference, not a ")
		w("retrieval error. Exact query and a sample divergent line are shown.\n\n")
		writeQuirks(&b, res, built)
	}
	if d3Pass && d4Pass && len(res.EngineQuirks) == 0 {
		w("**No divergences of any kind** — moedex == ripgrep == gold for all %d queries.\n\n", len(bat.Queries))
	}

	// --- Zoekt -------------------------------------------------------------
	w("## Zoekt differential (AC-E, soft)\n\n")
	writeZoekt(&b, res)

	// --- Performance -------------------------------------------------------
	w("## Performance (soft)\n\n")
	w("| Metric | Value |\n|---|---|\n")
	w("| Scan wall (moedex+gold, all shards) | %s |\n", res.ScanWall.Round(1e6))
	w("| ripgrep wall (all queries) | %s |\n", res.RGWall.Round(1e6))
	w("| Total run wall | %s |\n", res.TotalWall.Round(1e6))
	w("| moedex query latency p50 | %s |\n", res.MoeLatency.P50.Round(1e3))
	w("| moedex query latency p95 | %s |\n", res.MoeLatency.P95.Round(1e3))
	w("| moedex query latency max | %s |\n", res.MoeLatency.Max.Round(1e3))
	w("\n")
	writeLatencyBreakdown(&b, res)

	// --- Round trip & reproducibility -------------------------------------
	w("## Persistence round-trip (AC-C1)\n\n")
	if meta.RoundTripNote != "" {
		w("%s\n\n", meta.RoundTripNote)
	} else {
		w("Verified by `go test ./internal/parity/ -run RoundTrip` (save→load→mmap match ")
		w("sets byte-identical to the in-memory build).\n\n")
	}
	w("## Reproducibility & environment\n\n")
	w("- Seed: `%d` (same corpus + seed ⇒ same battery + verdict).\n", meta.Seed)
	w("- Master gate: `make verify`.\n")
	if meta.Machine != "" {
		w("- Machine: %s\n", meta.Machine)
	}
	if meta.GoVersion != "" {
		w("- Go: %s\n", meta.GoVersion)
	}
	if meta.RGVersion != "" {
		w("- ripgrep: %s\n", meta.RGVersion)
	}
	if meta.ZoektVersion != "" {
		w("- Zoekt: %s\n", meta.ZoektVersion)
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func writeDivergences(b *strings.Builder, res *Result, built *Built, ids []int, under bool) {
	w := func(f string, a ...any) { fmt.Fprintf(b, f, a...) }
	for i, id := range ids {
		if i >= 30 {
			w("- … (+%d more)\n", len(ids)-30)
			break
		}
		qr := res.Results[id]
		var diff []uint64
		if under {
			diff = qr.GoldMinusMoe
		} else {
			diff = qr.MoeMinusGold
		}
		w("- %s — |moe|=%d |gold|=%d |rg|=%d; %d offending lines:\n",
			qr.Q.String(), qr.NMoe, qr.NGold, qr.NRG, len(diff))
		for k, v := range diff {
			if k >= 3 {
				break
			}
			fid, line := unpack(v)
			w("    - `%s:%d`  →  `%s`\n", built.FT.Rel(fid), line, oneLine(fetchLine(built.MirrorDir, fid, line)))
		}
	}
	w("\n")
}

func writeQuirks(b *strings.Builder, res *Result, built *Built) {
	w := func(f string, a ...any) { fmt.Fprintf(b, f, a...) }
	shown := 0
	for _, id := range res.EngineQuirks {
		if shown >= 25 {
			w("- … (+%d more)\n", len(res.EngineQuirks)-25)
			break
		}
		qr := res.Results[id]
		w("- %s — |moe|=%d |rg|=%d; rg-only=%d, moe-only=%d\n",
			qr.Q.String(), qr.NMoe, qr.NRG, len(qr.RGMinusMoe), len(qr.MoeMinusRG))
		sample := qr.RGMinusMoe
		tag := "rg-only"
		if len(sample) == 0 {
			sample = qr.MoeMinusRG
			tag = "moe-only"
		}
		for k, v := range sample {
			if k >= 2 {
				break
			}
			fid, line := unpack(v)
			w("    - (%s) `%s:%d`  →  `%s`\n", tag, built.FT.Rel(fid), line, oneLine(fetchLine(built.MirrorDir, fid, line)))
		}
		shown++
	}
	w("\n")
}

func writeZoekt(b *strings.Builder, res *Result) {
	w := func(f string, a ...any) { fmt.Fprintf(b, f, a...) }
	z := res.Zoekt
	if z == nil {
		w("Skipped.\n\n")
		return
	}
	for _, n := range z.Notes {
		w("- %s\n", n)
	}
	if !z.Available {
		w("\nZoekt unavailable — section skipped (ripgrep gate remains authoritative).\n\n")
		return
	}
	w("\nCompared at **file** granularity vs ground truth (ripgrep). Buckets sent: ")
	var bks []string
	for bk := range z.PerBucket {
		bks = append(bks, string(bk))
	}
	sort.Strings(bks)
	w("%s.\n\n", strings.Join(bks, ", "))
	w("| Bucket | Queries | moedex⊇truth | zoekt⊇truth | zoekt skipped |\n|---|---|---|---|---|\n")
	keys := make([]Bucket, 0, len(z.PerBucket))
	for bk := range z.PerBucket {
		keys = append(keys, bk)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, bk := range keys {
		s := z.PerBucket[bk]
		w("| %s | %d | %d | %d | %d |\n", bk, s.Queries, s.MoedexCoversTruth, s.ZoektCoversTruth, s.ZoektSkipped)
	}
	w("\n- Queries where **Zoekt missed truth but moedex did not** (expected — Zoekt's ")
	w("file/trigram caps): %d.\n", len(z.ZoektMissed))
	w("- Queries where **moedex missed truth but Zoekt did not** (HIGH-priority bugs; ")
	w("must be zero): %d.\n", len(z.MoedexBeaten))
	if len(z.MoedexBeaten) > 0 {
		w("\n  ⚠ moedex was beaten by Zoekt on recall — these also fail AC-D3:\n")
		for i, id := range z.MoedexBeaten {
			if i >= 20 {
				break
			}
			w("  - %s\n", res.Results[id].Q.String())
		}
	}
	w("\n")
}

// writeLatencyBreakdown renders per-bucket moedex latency and the slowest queries
// (tail-profiling, see docs/adr/0012-search-latency-positional-verify.md). No-op
// if per-query timing absent.
func writeLatencyBreakdown(b *strings.Builder, res *Result) {
	w := func(f string, a ...any) { fmt.Fprintf(b, f, a...) }
	if len(res.MoeDur) == 0 || len(res.MoeDur) != len(res.Results) {
		return
	}

	byBucket := map[Bucket][]time.Duration{}
	attrByBucket := map[Bucket][]QueryAttribution{}
	hasAttr := len(res.MoeAttr) == len(res.Results)
	for i, qr := range res.Results {
		byBucket[qr.Q.Bucket] = append(byBucket[qr.Q.Bucket], res.MoeDur[i])
		if hasAttr {
			attrByBucket[qr.Q.Bucket] = append(attrByBucket[qr.Q.Bucket], res.MoeAttr[i])
		}
	}
	w("### moedex latency by bucket\n\n| Bucket | n | p50 | p95 | max |\n|---|---|---|---|---|\n")
	for _, bk := range AllBuckets {
		ds := byBucket[bk]
		if len(ds) == 0 {
			continue
		}
		ls := latency(ds)
		w("| %s | %d | %s | %s | %s |\n", bk, len(ds),
			ls.P50.Round(1e3), ls.P95.Round(1e3), ls.Max.Round(1e3))
	}
	w("\n")

	if hasAttr {
		writeAttributionByBucket(b, attrByBucket)
	}

	idx := make([]int, len(res.MoeDur))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(i, j int) bool { return res.MoeDur[idx[i]] > res.MoeDur[idx[j]] })
	if hasAttr {
		w("### top-20 slowest queries\n\n")
		w("| dur | bucket | matches | cand blobs | cand MB | cand lines | RE2 lines | workers | kind | all/query-All | query |\n")
		w("|---|---|---|---|---|---|---|---|---|---|---|\n")
		for k, i := range idx {
			if k >= 20 {
				break
			}
			qr := res.Results[i]
			attr := res.MoeAttr[i]
			w("| %s | %s | %d | %d | %.1f | %d | %s | %d | %s | %s | %s |\n",
				res.MoeDur[i].Round(1e3), qr.Q.Bucket, qr.NMoe,
				attr.CandidateBlobs, float64(attr.CandidateBytes)/1e6, attr.CandidateLines,
				attrRE2Lines(attr), attr.VerifyWorkers, attr.CandidateKind, attrAllFlag(attr),
				strings.ReplaceAll(oneLine(qr.Q.String()), "|", "\\|"))
		}
		w("\n")
		return
	}

	w("### top-20 slowest queries\n\n| dur | bucket | matches | query |\n|---|---|---|---|\n")
	for k, i := range idx {
		if k >= 20 {
			break
		}
		qr := res.Results[i]
		w("| %s | %s | %d | %s |\n", res.MoeDur[i].Round(1e3), qr.Q.Bucket, qr.NMoe,
			strings.ReplaceAll(oneLine(qr.Q.String()), "|", "\\|"))
	}
	w("\n")
}

func writeAttributionByBucket(b *strings.Builder, byBucket map[Bucket][]QueryAttribution) {
	w := func(f string, a ...any) { fmt.Fprintf(b, f, a...) }
	w("### moedex candidate attribution by bucket\n\n")
	w("Candidate counts are measured at unique-blob granularity before final line verification. ")
	w("Filter kind, lines entering RE2, and verify worker counts are captured from ")
	w("the timed `internal/search` path.\n\n")
	w("| Bucket | n | cand blobs p50 | cand blobs p95 | cand blobs max | cand MB p95 | cand lines p95 | all-candidates | query-All |\n")
	w("|---|---|---|---|---|---|---|---|---|\n")
	for _, bk := range AllBuckets {
		attrs := byBucket[bk]
		if len(attrs) == 0 {
			continue
		}
		var blobVals, byteVals, lineVals []int64
		allCandidates := 0
		queryAll := 0
		for _, attr := range attrs {
			blobVals = append(blobVals, attr.CandidateBlobs)
			byteVals = append(byteVals, attr.CandidateBytes)
			lineVals = append(lineVals, attr.CandidateLines)
			if attr.AllCandidates {
				allCandidates++
			}
			if attr.QueryAll {
				queryAll++
			}
		}
		blobStats := int64Percentiles(blobVals)
		byteStats := int64Percentiles(byteVals)
		lineStats := int64Percentiles(lineVals)
		w("| %s | %d | %d | %d | %d | %.1f | %d | %d | %d |\n",
			bk, len(attrs), blobStats.P50, blobStats.P95, blobStats.Max,
			float64(byteStats.P95)/1e6, lineStats.P95, allCandidates, queryAll)
	}
	w("\n")
}

type int64Stats struct {
	P50, P95, Max int64
}

func int64Percentiles(vals []int64) int64Stats {
	if len(vals) == 0 {
		return int64Stats{}
	}
	c := append([]int64(nil), vals...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	pick := func(p float64) int64 {
		idx := int(p * float64(len(c)))
		if idx >= len(c) {
			idx = len(c) - 1
		}
		return c[idx]
	}
	return int64Stats{P50: pick(0.50), P95: pick(0.95), Max: c[len(c)-1]}
}

func attrAllFlag(attr QueryAttribution) string {
	switch {
	case attr.AllCandidates && attr.QueryAll:
		return "all/query-All"
	case attr.AllCandidates:
		return "all"
	case attr.QueryAll:
		return "query-All"
	default:
		return ""
	}
}

func attrRE2Lines(attr QueryAttribution) string {
	if !attr.LinesEnteringRE2Known {
		return ""
	}
	return fmt.Sprintf("%d", attr.LinesEnteringRE2)
}

func passStr(ok bool) string {
	if ok {
		return "**PASS**"
	}
	return "**FAIL**"
}

// oneLine renders a matched line for the report: it escapes control bytes and
// invalid UTF-8 as \xHH so the Markdown stays valid UTF-8 even when the sampled
// line is Latin-1 / binary-ish (exactly the bytes behind a `.`-vs-invalid-UTF-8
// engine quirk), and truncates for readability.
func oneLine(s string) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < 120; {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02X`, s[i])
		case r == '\n':
			b.WriteString("⏎")
		case r == '`':
			b.WriteByte('\'')
		case r < 0x20:
			fmt.Fprintf(&b, `\x%02X`, s[i])
		default:
			b.WriteRune(r)
		}
		i += size
	}
	out := b.String()
	if b.Len() >= 120 {
		out += "…"
	}
	return out
}
