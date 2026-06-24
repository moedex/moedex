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

	// --- open both shard dirs through the real serving spine. -----------------
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

	// --- per-query: cas-export == direct == ripgrep over F. -------------------
	rg := newRG(t, built.MirrorDir)
	var (
		mismatchCAS  int // cas-export != direct build (the seam is broken)
		underApprox  int // cas-export missed a line ripgrep found (SACRED violation)
		overApprox   int // cas-export returned a line ripgrep did not
		checkedVsRG  int
	)
	for _, q := range bat.Queries {
		directLocs := corpusQuery(t, direct, q)
		casLocs := corpusQuery(t, cas, q)

		// (1) cas-export must be byte-identical to the direct build.
		if miss, extra := diffLocs(directLocs, casLocs); len(miss) > 0 || len(extra) > 0 {
			mismatchCAS++
			if mismatchCAS <= 8 {
				t.Errorf("cas-export != direct for %s:\n  direct has, cas missing (%d): %s\n  cas has, direct missing (%d): %s",
					q, len(miss), sampleLocs(miss), len(extra), sampleLocs(extra))
			}
			continue
		}

		// (2) cas-export must match ripgrep over F (parity SACRED invariant).
		want, ok := rg.run(t, q, built.FT)
		if !ok {
			continue // rg quirk-buckets (handled by the parity harness proper) skipped
		}
		checkedVsRG++
		miss, extra := diffLocs(want, casLocs)
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

	t.Logf("cas-export parity: %d queries; cas!=direct=%d; vs-rg checked=%d under=%d over=%d",
		len(bat.Queries), mismatchCAS, checkedVsRG, underApprox, overApprox)
	if mismatchCAS > 0 {
		t.Fatalf("FAIL: cas-export diverged from the direct build on %d queries (the seam is not parity-clean)", mismatchCAS)
	}
	if underApprox > 0 {
		t.Fatalf("FAIL: cas-export UNDER-APPROXIMATED ripgrep on %d queries (SACRED parity violation)", underApprox)
	}
	if overApprox > 0 {
		t.Fatalf("FAIL: cas-export OVER-APPROXIMATED ripgrep on %d queries", overApprox)
	}
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

// run executes rg for a query. ok=false means we deliberately skip the rg
// comparison for this query (the RE2-vs-Rust-regex engine quirks the full parity
// harness adjudicates separately); for those we still assert cas-export==direct,
// which is the property this gate exists to prove. We rg-compare the buckets
// where Go RE2 and Rust regex are pinned to agree (literals, case-insensitive
// literals, unicode literals) and short-circuit the regex shapes.
func (r *rgOracle) run(t *testing.T, q parity.Query, ft *parity.FileTable) (map[loc]bool, bool) {
	t.Helper()
	if !q.Literal {
		return nil, false // regex semantics adjudicated by the parity harness proper
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
	args = append(args, "-F", "-e", q.Pattern, r.mirrorDir)

	out, err := exec.Command(r.bin, args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return map[loc]bool{}, true // no matches
		}
		t.Fatalf("rg %s: %v", q, err)
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
	return res, true
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
