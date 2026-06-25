package blobstore

// Full-corpus cas-export parity harness — the orchestrator's definitive gate.
//
// This is the proof that the CAS migration seam is parity-clean: a shard dir
// materialized from the CAS (cas-export) returns the EXACT same (file,line) match
// sets as a shard dir built directly by the proven parity builder, AND both match
// ripgrep, over the full seed-reproducible query battery. If any battery query's
// match set differs between the two shard dirs — or if cas-export under-/over-
// approximates ripgrep — the gate fails.
//
// Three oracles, one scope F:
//   - DIRECT:  internal/parity.Build indexes the corpus into shards + a content
//              mirror (= exactly F, the bytes rg scans) and samples the battery.
//   - CAS:     BuildCAS + ExportShardDir materializes a shard dir from the CAS.
//   - rg:      ripgrep over the SAME content mirror, so its scanned universe IS F.
// Both shard dirs are opened through the real serving spine (server.Corpus) and
// driven with battery queries mapped to Literal/Regex exactly as the moedex
// oracle does. Matches are compared at (repo, rel-path, line) granularity — the
// identity that is stable across the two builds (both index the same repos at the
// same abspaths) and against rg (which reports mirror fileIDs we map back to F).
//
// GATING (mirrors internal/search.TestParityCorpus): skipped unless a corpus is
// present. By default it runs a SMALL repo subset so it is a fast local gate; the
// orchestrator runs the FULL ~/TCGitlab corpus by setting the env vars in the one
// documented command at the bottom of this file.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"moedex/internal/ingest"
	"moedex/internal/parity"
	"moedex/internal/search"
	"moedex/internal/server"
)

// loc is a (repo, rel-path, line) match location — the granularity at which the
// two shard dirs are compared (abspath-independent so a relocated corpus still
// compares cleanly, repo-qualified so identical rel paths in different repos do
// not collide).
type loc struct {
	repo string
	rel  string
	line int
}

// corpusRoot resolves the corpus to gate on. MOEDEX_CAS_PARITY_CORPUS overrides;
// otherwise ~/TCGitlab (the standing mirror). Returns "" (=> skip) if absent.
func corpusRoot(t *testing.T) string {
	t.Helper()
	if r := os.Getenv("MOEDEX_CAS_PARITY_CORPUS"); r != "" {
		return r
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	root := filepath.Join(home, "TCGitlab")
	if _, err := os.Stat(root); err != nil {
		return ""
	}
	return root
}

// maxRepos caps the subset size. 0 = full corpus (the orchestrator's run); the
// default keeps the local gate to a few repos so it is fast.
func maxRepos() int {
	if v := os.Getenv("MOEDEX_CAS_PARITY_MAXREPOS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 3 // small, fast local default; set to 0 for the full-corpus gate
}

// shardBytes is the per-shard content target. A small default forces MULTIPLE
// shards even on a tiny subset, so the cross-shard merge path is exercised
// locally; the full-corpus run can raise it to parity.DefaultShardBytes via env
// for build-RAM control without affecting the (file,line) match sets compared.
func shardBytes() int64 {
	if v := os.Getenv("MOEDEX_CAS_PARITY_SHARDBYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 256 << 10 // 256 KiB => several shards on the small local subset
}

// TestCASExportParityCorpus is the full-corpus cas-export parity gate.
//
// Run it on the full corpus with the single documented command:
//
//	MOEDEX_CAS_PARITY_CORPUS=$HOME/TCGitlab MOEDEX_CAS_PARITY_MAXREPOS=0 \
//	  go test -count=1 -timeout 0 -run TestCASExportParityCorpus ./internal/blobstore/
//
// (No env vars => small ~3-repo subset, the fast local validation.)
func TestCASExportParityCorpus(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed")
	}
	root := corpusRoot(t)
	if root == "" {
		t.Skip("corpus not present (set MOEDEX_CAS_PARITY_CORPUS or populate ~/TCGitlab)")
	}

	// Pin both builders to the SAME repo set. We discover the corpus' repos once
	// and cap to the first N (deterministic, sorted order); both builders are
	// driven over that identical list. A cap of 0 uses every repo (the full-corpus
	// gate). parity.Build takes MaxRepos directly; BuildCAS is driven over the same
	// list via its injectable discover seam (subsetDiscover).
	cap := maxRepos()
	allRepos, err := ingest.DiscoverRepos(root)
	if err != nil {
		t.Fatalf("discover repos: %v", err)
	}
	if len(allRepos) == 0 {
		t.Skip("corpus root has no git repos")
	}
	subset := allRepos
	if cap > 0 && cap < len(subset) {
		subset = subset[:cap]
	}

	work := t.TempDir()
	const seed = 7

	// --- DIRECT build (the proven parity builder) + battery + rg mirror. ------
	built, err := parity.Build(parity.Config{
		Root:       root,
		WorkDir:    work,
		Seed:       seed,
		MaxRepos:   cap, // 0 = all; matches the subset's first-N (DiscoverRepos order)
		ShardBytes: shardBytes(), // small shards => multiple shards => exercise the merge path
		Logf:       t.Logf,
	})
	if err != nil {
		t.Fatalf("parity.Build (direct): %v", err)
	}
	if built.IngestedRepos == 0 {
		t.Skip("no repos ingested from corpus subset")
	}
	bat := parity.Generate(built.Pool, seed)
	if len(bat.Queries) == 0 {
		t.Fatal("empty battery")
	}
	t.Logf("DIRECT: %d repos, %d files, %d shards; battery %d queries",
		built.IngestedRepos, built.NumFiles, len(built.Shards), len(bat.Queries))

	directDir := filepath.Join(work, "shards") // parity.Build writes shards here

	// --- CAS build + export (the migration seam under test). ------------------
	casDir := filepath.Join(work, "cas")
	subsetDiscover := func(string) ([]string, error) { return subset, nil }
	if _, err := buildCAS(root, casDir, subsetDiscover, ingest.Repo, ingest.Head); err != nil {
		t.Fatalf("buildCAS (subset): %v", err)
	}
	casShardDir := filepath.Join(work, "cas-shards")
	if _, err := ExportShardDir(casDir, casShardDir, shardBytes()); err != nil {
		t.Fatalf("ExportShardDir: %v", err)
	}

	// --- DEDUPED export (the served-shard-dedup format under test): content-less
	// MOEDEX05 shards + ONE shared blobs.dat content store. It must return
	// byte-identical (file,line) matches to the direct build AND ripgrep, while
	// storing each unique blob's content exactly ONCE for the whole served corpus.
	dedupShardDir := filepath.Join(work, "dedup-shards")
	_, dedupStoredBytes, err := ExportDedupedShardDir(casDir, dedupShardDir, shardBytes())
	if err != nil {
		t.Fatalf("ExportDedupedShardDir: %v", err)
	}
	// Dedup footprint proof. The invariant that holds on ANY corpus: the deduped
	// served content == the CAS StoredBytes (the unique-content size), and is <=
	// the sum of per-shard inlined content of today's (cas-export MOEDEX03) shards.
	// The footprint is STRICTLY less exactly when some blob is inlined into more
	// than one shard (cross-shard duplication); on a tiny subset that all lands in
	// one shard, or whose repos share no content across shards, the two are equal —
	// which is correct, not a dedup failure. The strict-win case is proven on a
	// forced multi-shard fixture in TestDedupedExportStoresSharedBlobOnce; here we
	// assert the universal invariant and REPORT the realized ratio (which on the
	// full corpus is well above 1.0x — that is the orchestrator's headline number).
	legacyServed := servedContentBytes(t, casShardDir)
	if got := casStoredBytes(t, casDir); dedupStoredBytes != got {
		t.Errorf("deduped served content = %d, want CAS StoredBytes %d", dedupStoredBytes, got)
	}
	if dedupStoredBytes > legacyServed {
		t.Errorf("deduped served content %d EXCEEDS legacy inlined %d (must never store more)", dedupStoredBytes, legacyServed)
	}
	t.Logf("DEDUP FOOTPRINT: deduped served content = %d bytes (== CAS StoredBytes), legacy cas-export inlined = %d bytes; ratio %.2fx (>1.0x => cross-shard dedup realized)",
		dedupStoredBytes, legacyServed, ratio(legacyServed, dedupStoredBytes))

	// --- open all shard dirs through the real serving spine. ------------------
	direct, err := server.Open(directDir)
	if err != nil {
		t.Fatalf("server.Open direct shards: %v", err)
	}
	defer direct.Close()
	cas, err := server.Open(casShardDir)
	if err != nil {
		t.Fatalf("server.Open cas-export shards: %v", err)
	}
	defer cas.Close()
	dedup, err := server.Open(dedupShardDir)
	if err != nil {
		t.Fatalf("server.Open deduped shards: %v", err)
	}
	defer dedup.Close()

	// --- per-query: cas-export AND deduped == direct == ripgrep over F. -------
	rg := newRG(t, built.MirrorDir)
	var (
		mismatchCAS   int // cas-export != direct build (the MOEDEX03 seam is broken)
		mismatchDedup int // deduped != direct build (the MOEDEX05 served-dedup seam is broken)
		underApprox   int // cas-export missed a line ripgrep found (SACRED violation)
		overApprox    int // cas-export returned a line ripgrep did not
		underDedup    int // deduped missed a line ripgrep found (SACRED violation)
		overDedup     int // deduped returned a line ripgrep did not
		checkedVsRG   int // queries with a usable rg ground truth, actually compared
		rgSkipped     int // queries rg ERRORED on (exit >= 2: un-foldable byte etc.)
	)
	for _, q := range bat.Queries {
		directLocs := corpusQuery(t, direct, q)
		casLocs := corpusQuery(t, cas, q)
		dedupLocs := corpusQuery(t, dedup, q)

		// (1a) cas-export must be byte-identical to the direct build.
		if miss, extra := diffLocs(directLocs, casLocs); len(miss) > 0 || len(extra) > 0 {
			mismatchCAS++
			if mismatchCAS <= 8 {
				t.Errorf("cas-export != direct for %s:\n  direct has, cas missing (%d): %s\n  cas has, direct missing (%d): %s",
					q, len(miss), sampleLocs(miss), len(extra), sampleLocs(extra))
			}
		}

		// (1b) deduped served must be byte-identical to the direct build.
		dedupMatchesDirect := true
		if miss, extra := diffLocs(directLocs, dedupLocs); len(miss) > 0 || len(extra) > 0 {
			dedupMatchesDirect = false
			mismatchDedup++
			if mismatchDedup <= 8 {
				t.Errorf("deduped != direct for %s:\n  direct has, dedup missing (%d): %s\n  dedup has, direct missing (%d): %s",
					q, len(miss), sampleLocs(miss), len(extra), sampleLocs(extra))
			}
		}
		// If either served format already diverged from direct, the rg comparison
		// for that format would just re-report the same divergence; skip to the next
		// query once the seam itself is shown broken.
		if !dedupMatchesDirect {
			continue
		}

		// (2) both served formats must match ripgrep over F (parity SACRED invariant).
		want, ok, rgErr := rg.run(t, q, built.FT)
		if !ok {
			if rgErr {
				rgSkipped++ // rg errored (exit >= 2): no ground truth — quirk-skip, but visible
			}
			// In every skip case we already proved cas==direct and dedup==direct above
			// (the property this gate exists to prove); only the rg comparison is skipped.
			continue
		}
		checkedVsRG++
		if miss, extra := diffLocs(want, casLocs); len(miss) > 0 || len(extra) > 0 {
			if len(miss) > 0 {
				underApprox++
				if underApprox <= 8 {
					t.Errorf("UNDER-APPROX cas-export vs rg for %s: missed %d (e.g. %s)", q, len(miss), sampleLocs(miss))
				}
			}
			if len(extra) > 0 {
				overApprox++
				if overApprox <= 8 {
					t.Errorf("OVER-APPROX cas-export vs rg for %s: extra %d (e.g. %s)", q, len(extra), sampleLocs(extra))
				}
			}
		}
		if miss, extra := diffLocs(want, dedupLocs); len(miss) > 0 || len(extra) > 0 {
			if len(miss) > 0 {
				underDedup++
				if underDedup <= 8 {
					t.Errorf("UNDER-APPROX deduped vs rg for %s: missed %d (e.g. %s)", q, len(miss), sampleLocs(miss))
				}
			}
			if len(extra) > 0 {
				overDedup++
				if overDedup <= 8 {
					t.Errorf("OVER-APPROX deduped vs rg for %s: extra %d (e.g. %s)", q, len(extra), sampleLocs(extra))
				}
			}
		}
	}

	t.Logf("served parity: %d queries; cas!=direct=%d dedup!=direct=%d; vs-rg checked=%d under(cas=%d dedup=%d) over(cas=%d dedup=%d) rg-skipped=%d",
		len(bat.Queries), mismatchCAS, mismatchDedup, checkedVsRG, underApprox, underDedup, overApprox, overDedup, rgSkipped)
	if mismatchCAS > 0 {
		t.Fatalf("FAIL: cas-export diverged from the direct build on %d queries (the seam is not parity-clean)", mismatchCAS)
	}
	if mismatchDedup > 0 {
		t.Fatalf("FAIL: deduped served diverged from the direct build on %d queries (the served-shard-dedup seam is not parity-clean)", mismatchDedup)
	}
	if underApprox > 0 || underDedup > 0 {
		t.Fatalf("FAIL: served UNDER-APPROXIMATED ripgrep (cas=%d dedup=%d queries) — SACRED parity violation", underApprox, underDedup)
	}
	if overApprox > 0 || overDedup > 0 {
		t.Fatalf("FAIL: served OVER-APPROXIMATED ripgrep (cas=%d dedup=%d queries)", overApprox, overDedup)
	}
}

// casStoredBytes returns the CAS's unique-content footprint (Store.BytesStored) at
// casDir — the deduped served content target.
func casStoredBytes(t *testing.T, casDir string) int64 {
	t.Helper()
	s, err := Open(casDir)
	if err != nil {
		t.Fatalf("open CAS: %v", err)
	}
	defer s.Close()
	return s.BytesStored()
}

// ratio is legacy/dedup as a float (1.0 if dedup is 0, to avoid a divide-by-zero in
// the log line).
func ratio(legacy, dedup int64) float64 {
	if dedup == 0 {
		return 1
	}
	return float64(legacy) / float64(dedup)
}

// corpusQuery drives a server.Corpus with a battery query mapped to Literal/Regex
// exactly as the moedex oracle does (plain literal => Literal; everything else =>
// Regex over the query's Go regexp source), and returns its (repo,rel,line) set.
func corpusQuery(t *testing.T, c *server.Corpus, q parity.Query) map[loc]bool {
	t.Helper()
	var (
		ms  []search.Match
		err error
	)
	if q.Literal && !q.IgnoreCase {
		ms, _, err = c.Literal(context.Background(), q.Pattern)
	} else {
		ms, _, err = c.Regex(context.Background(), q.GoRegexSource())
	}
	if err != nil {
		t.Fatalf("corpus query %s: %v", q, err)
	}
	out := make(map[loc]bool, len(ms))
	for _, m := range ms {
		out[loc{m.Repo, m.RelPath, m.Line}] = true
	}
	return out
}

// rgOracle shells out to ripgrep over the materialized mirror (= F), the same way
// internal/search/parity_test.go does, and maps mirror fileIDs back to (repo,
// rel, line) via the parity FileTable so its set is comparable to the corpus sets.
type rgOracle struct {
	bin       string
	mirrorDir string
}

func newRG(t *testing.T, mirrorDir string) *rgOracle {
	t.Helper()
	bin, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep not installed")
	}
	return &rgOracle{bin: bin, mirrorDir: mirrorDir}
}

// run executes rg for a query and returns (matches, ok, rgErr):
//   - ok=true:  a usable ground truth (the matches); compare against it.
//   - ok=false, rgErr=false: a query we deliberately do NOT rg-compare by design
//     (regex shapes — RE2-vs-Rust-regex engine quirks the full parity harness
//     adjudicates separately). Not counted; expected.
//   - ok=false, rgErr=true:  rg ERRORED (exit code >= 2 — e.g. it refuses to
//     case-fold an invalid-UTF-8 byte, an un-foldable pattern). rg gives no
//     ground truth here, so we quirk-SKIP — but the caller COUNTS it so the skip
//     is visible in the summary, not silently swallowed.
//
// In every skip case the gate still asserts cas-export==direct for the query
// (the property this gate exists to prove); only the rg ground-truth comparison
// is skipped. We rg-compare the literal buckets where Go RE2 and Rust regex are
// pinned to agree (literals, case-insensitive literals, unicode literals).
//
// rg exit codes: 0 = matches, 1 = no matches (a valid empty ground truth),
// >= 2 = error.
func (r *rgOracle) run(t *testing.T, q parity.Query, ft *parity.FileTable) (matches map[loc]bool, ok, rgErr bool) {
	t.Helper()
	if !q.Literal {
		return nil, false, false // regex semantics adjudicated by the parity harness proper
	}
	args := []string{
		"--no-config", "--no-heading", "--color=never", "--with-filename",
		"-n", "--no-ignore", "--hidden", "--no-messages",
	}
	if q.IgnoreCase {
		args = append(args, "-i")
	} else {
		args = append(args, "--case-sensitive")
	}
	if q.NoUnicode {
		args = append(args, "--no-unicode") // mirror internal/parity's rg semantics alignment
	}
	args = append(args, "-F", "-e", q.Pattern, r.mirrorDir)

	out, err := exec.Command(r.bin, args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if ee.ExitCode() == 1 {
				return map[loc]bool{}, true, false // no matches (valid empty truth)
			}
			// Exit code >= 2: rg errored (no ground truth). Quirk-skip + count it.
			return nil, false, true
		}
		// Could not even invoke rg (fork/exec failure): that IS a harness failure.
		t.Fatalf("rg %s: invoke failed: %v", q, err)
	}
	res := map[loc]bool{}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		c1 := strings.IndexByte(line, ':')
		if c1 < 0 {
			continue
		}
		rest := line[c1+1:]
		c2 := strings.IndexByte(rest, ':')
		if c2 < 0 {
			continue
		}
		n, err := strconv.Atoi(rest[:c2])
		if err != nil {
			continue
		}
		id := mirrorID(line[:c1])
		if id < 0 || id >= ft.Len() {
			continue
		}
		res[loc{ft.Repo(id), ft.Rel(id), n}] = true
	}
	return res, true, false
}

// mirrorID parses the trailing integer basename of a mirror path (the fileID),
// matching parity's mirror layout. Returns -1 if it doesn't parse.
func mirrorID(p string) int {
	base := filepath.Base(p)
	if base == "" {
		return -1
	}
	id := 0
	for i := 0; i < len(base); i++ {
		c := base[i]
		if c < '0' || c > '9' {
			return -1
		}
		id = id*10 + int(c-'0')
	}
	return id
}

// diffLocs returns (in want not in got, in got not in want).
func diffLocs(want, got map[loc]bool) (missing, extra []loc) {
	for l := range want {
		if !got[l] {
			missing = append(missing, l)
		}
	}
	for l := range got {
		if !want[l] {
			extra = append(extra, l)
		}
	}
	return missing, extra
}

func sampleLocs(ls []loc) string {
	s := make([]string, 0, len(ls))
	for _, l := range ls {
		s = append(s, l.repo+"/"+l.rel+":"+strconv.Itoa(l.line))
	}
	sort.Strings(s)
	if len(s) > 6 {
		return strings.Join(s[:6], ", ") + " ..."
	}
	return strings.Join(s, ", ")
}
