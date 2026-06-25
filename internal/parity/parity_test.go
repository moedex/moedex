package parity

import (
	"crypto/sha1"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/index"
)

// --- AC-C1: persistence round-trip identity --------------------------------

// synthContent is a deterministic in-memory corpus exercising every matching
// path: common/rare tokens, case variants, metacharacters, multibyte runes, and
// byte-identical duplicate content (dedup-expand).
var synthFiles = []struct{ rel, content string }{
	{"a.cs", "namespace TC.SslApi;\npublic class SslService {\n  public string Token;\n  void get() {}\n}\n"},
	{"b.cs", "using System;\nusing System.Text;\nclass Other { get; set; }\nPUBLIC Public public\n"},
	{"u.txt", "let prix = café_au_lait;\nΣumма = Δ + ß\nplain ascii line\n你好 world → done\n"},
	{"m.txt", "arr[0] = obj.Method(x);\nif (a|b) i++;\n$value => { return null; }\n"},
	{"dup1.cs", "marker line\nZZUNIQUEDUPTOKEN appears here\n"},
	{"dup2.cs", "marker line\nZZUNIQUEDUPTOKEN appears here\n"}, // identical -> dedup
}

func buildSynthIndex(t *testing.T) (*index.Index, *FileTable, *TermPool) {
	t.Helper()
	ix := index.New()
	ft := newFileTable()
	pool := newTermPool(1)
	for _, f := range synthFiles {
		abs := filepath.Join("/synthetic", f.rel)
		sha := fmt.Sprintf("%x", sha1.Sum([]byte(f.content)))
		ft.add("synthetic", f.rel, abs)
		ix.AddFile("synthetic", f.rel, abs, sha, []byte(f.content))
		pool.observe([]byte(f.content))
	}
	return ix, ft, pool
}

func matchSet(t *testing.T, ix *index.Index, q Query, ft *FileTable) MatchSet {
	t.Helper()
	var a accum
	if _, err := moedexInto(&a, ix, q, ft); err != nil {
		t.Fatalf("moedex %s: %v", q, err)
	}
	return a.finalize()
}

func TestRoundTrip(t *testing.T) {
	ix, ft, pool := buildSynthIndex(t)
	bat := Generate(pool, 7)
	if len(bat.Queries) == 0 {
		t.Fatal("empty battery")
	}

	path := filepath.Join(t.TempDir(), "synth.idx")
	if err := diskstore.Save(ix, path); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := diskstore.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	mmapped, closer, err := diskstore.LoadMmap(path)
	if err != nil {
		t.Fatalf("mmap: %v", err)
	}
	defer closer.Close()

	for _, q := range bat.Queries {
		build := matchSet(t, ix, q, ft)
		ld := matchSet(t, loaded, q, ft)
		mm := matchSet(t, mmapped, q, ft)
		if !equalSets(build, ld) {
			t.Errorf("load-side differs from build-side for %s: build=%d load=%d", q, build.Len(), ld.Len())
		}
		if !equalSets(build, mm) {
			t.Errorf("mmap-side differs from build-side for %s: build=%d mmap=%d", q, build.Len(), mm.Len())
		}
	}
}

// --- AC-D2: battery determinism + bucket coverage --------------------------

func TestBatteryBucketsNonEmpty(t *testing.T) {
	_, _, pool := buildSynthIndex(t)
	bat := Generate(pool, 7)
	counts := bat.BucketCounts()
	for _, bk := range AllBuckets {
		if counts[bk] == 0 {
			t.Errorf("bucket %s is empty", bk)
		}
	}
	if err := bat.AssertBuckets(20); err != nil {
		t.Errorf("AssertBuckets: %v", err)
	}
}

func TestBatteryDeterministic(t *testing.T) {
	_, _, pool := buildSynthIndex(t)
	a := Generate(pool, 7)
	b := Generate(pool, 7)
	if len(a.Queries) != len(b.Queries) {
		t.Fatalf("nondeterministic size: %d vs %d", len(a.Queries), len(b.Queries))
	}
	for i := range a.Queries {
		if a.Queries[i] != b.Queries[i] {
			t.Fatalf("query %d differs: %v vs %v", i, a.Queries[i], b.Queries[i])
		}
	}
	c := Generate(pool, 8)
	if len(c.Queries) == len(a.Queries) {
		same := true
		for i := range a.Queries {
			if a.Queries[i] != c.Queries[i] {
				same = false
				break
			}
		}
		if same {
			t.Error("different seed produced identical battery (selection not seed-driven)")
		}
	}
}

// gold and moedex must agree on the synthetic index for every battery query —
// this is the trigram-reduction soundness invariant in miniature, and notably
// guards the case-insensitive prefilter fix.
func TestMoedexEqualsGoldSynthetic(t *testing.T) {
	ix, ft, pool := buildSynthIndex(t)
	bat := Generate(pool, 7)
	for _, q := range bat.Queries {
		moe := matchSet(t, ix, q, ft)
		var ga accum
		goldInto(&ga, ix, goldMatcher(q), ft)
		gold := ga.finalize()
		if !equalSets(moe, gold) {
			t.Errorf("moedex != gold for %s: |moe|=%d |gold|=%d missed=%d extra=%d",
				q, moe.Len(), gold.Len(), len(minus(gold, moe)), len(minus(moe, gold)))
		}
	}
}

// Explicit regression for the FoldCase prefilter bug: a case-insensitive search
// must find every case variant.
func TestCaseInsensitiveFindsAllVariants(t *testing.T) {
	ix, ft, _ := buildSynthIndex(t)
	q := Query{Bucket: BCaseInsensitive, Pattern: "public", Literal: true, IgnoreCase: true}
	moe := matchSet(t, ix, q, ft)
	var ga accum
	goldInto(&ga, ix, goldMatcher(q), ft)
	gold := ga.finalize()
	if !equalSets(moe, gold) {
		t.Fatalf("case-insensitive 'public' missed variants: |moe|=%d |gold|=%d", moe.Len(), gold.Len())
	}
	if moe.Len() < 3 {
		t.Fatalf("expected ≥3 case-variant lines for (?i)public, got %d", moe.Len())
	}
}

func TestLatencyAttributionSynthetic(t *testing.T) {
	ix, _, _ := buildSynthIndex(t)
	stats := newShardStats(ix)

	shortLiteral := attributionForQuery(ix, stats, Query{Pattern: "Q", Literal: true})
	if shortLiteral.CandidateKind != "literal-all" || !shortLiteral.AllCandidates || shortLiteral.QueryAll {
		t.Fatalf("short literal attribution = %+v, want literal all-candidates without query-All", shortLiteral)
	}
	if shortLiteral.CandidateBlobs != int64(ix.NumBlobs()) || shortLiteral.CandidateBytes == 0 || shortLiteral.CandidateLines == 0 {
		t.Fatalf("short literal candidate totals = %+v, want all shard blobs/bytes/lines", shortLiteral)
	}
	if !shortLiteral.LinesEnteringRE2Known || shortLiteral.LinesEnteringRE2 != 0 {
		t.Fatalf("literal RE2 accounting = known:%v lines:%d, want known zero", shortLiteral.LinesEnteringRE2Known, shortLiteral.LinesEnteringRE2)
	}

	positional := attributionForQuery(ix, stats, Query{Pattern: "ZZUNIQUEDUPTOKEN", Literal: true})
	if positional.CandidateKind != "literal-positional" {
		t.Fatalf("positional candidate kind = %q", positional.CandidateKind)
	}
	if positional.CandidateBlobs != 1 {
		t.Fatalf("positional candidate blobs = %d, want 1 deduped blob", positional.CandidateBlobs)
	}

	regexAll := attributionForQuery(ix, stats, Query{Pattern: ".", Literal: false})
	if regexAll.CandidateKind != "regex-all" || !regexAll.QueryAll || !regexAll.AllCandidates {
		t.Fatalf("regex all attribution = %+v, want regex-all/query-All/all-candidates", regexAll)
	}
}

// literalCandidateBlobs must apply the IndexedGram gate on a selective index:
// when a query's begin/end trigram is deselected its postings are UNKNOWN, so the
// helper must report all=true (force-scan widening) rather than under-count by
// reading the (empty) postings directly. On the all-trigram build it returns the
// exact candidate set with all=false.
func TestLiteralCandidateBlobsSelectiveGate(t *testing.T) {
	// "xyz" occurs in every blob (df=1.0) so df<=0.5 drops it; "rar" is rare.
	contents := []string{
		"xyz alpha rare token\n",
		"xyz beta\n",
		"xyz gamma\n",
		"xyz delta\n",
	}
	b := index.NewSelective(index.FrequencyThresholdSelector{MaxDocFraction: 0.5})
	for i, c := range contents {
		abs := filepath.Join("/synthetic", fmt.Sprintf("f%d.txt", i))
		sha := fmt.Sprintf("%x", sha1.Sum([]byte(c)))
		b.AddFile("synthetic", filepath.Base(abs), abs, sha, []byte(c))
	}
	ix := b.Finalize()
	if !ix.Selective() {
		t.Fatal("expected a selective index")
	}

	// A literal whose begin gram "xyz" was dropped must yield all-candidates.
	if ids, all := literalCandidateBlobs(ix, []byte("xyz alpha")); !all || ids != nil {
		t.Errorf("deselected-begin-gram literal: ids=%v all=%v, want nil/true (force-scan)", ids, all)
	}

	// A literal whose begin AND end grams are kept yields the exact candidate set
	// with all=false (here "rare" -> begin "rar"/end "are", both rare = kept).
	ids, all := literalCandidateBlobs(ix, []byte("rare"))
	if all {
		t.Errorf("kept-gram literal reported all-candidates; want exact set")
	}
	if len(ids) != 1 {
		t.Errorf("kept-gram literal candidate blobs = %d, want 1", len(ids))
	}
}

// --- AC-D3/AC-D4: full pipeline parity on a controlled git corpus ----------

// makeGitRepo creates a git repo with the given files staged (ls-files-visible).
func makeGitRepo(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	run("init", "-q")
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
}

func TestFullPipelineParitySmall(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed") // env-gated; ripgrep gate is exercised on the real corpus
	}

	corpus := t.TempDir()
	makeGitRepo(t, filepath.Join(corpus, "repoA"), map[string]string{
		"svc.cs":   "namespace TC.SslApi;\npublic class SslService {\n  public string Token;\n}\nPUBLIC public Public\n",
		"util.cs":  "using System;\nusing System.Text;\nvar x = obj.Method(arr[0]);\nif (a|b) i++;\n",
		"notes.md": "café_au_lait and Σumма plus 你好 → done\nreturn null; // TODO\n",
	})
	makeGitRepo(t, filepath.Join(corpus, "group/repoB"), map[string]string{
		"main.ts":  "export function handler(req) { return response; }\nconst v123 = 0x1A2B;\n",
		"dup1.txt": "ZZUNIQUEDUPTOKEN here\nshared line\n",
		"dup2.txt": "ZZUNIQUEDUPTOKEN here\nshared line\n",
	})

	// Run the same gate over the default all-trigram build AND the opt-in
	// selective build. AC-D3 (gold \ moedex empty — no under-approximation) MUST
	// hold for both: the selective path drops near-universal grams but forces a
	// full scan for any deselected gram via IndexedGram, so it can only ever
	// WIDEN the candidate set. gram-max-df=0.5 is small enough that several grams
	// in this tiny corpus are dropped, so the selective force-scan path is
	// genuinely exercised (not a degenerate all-kept build).
	cases := []struct {
		name     string
		selector index.GramSelector
	}{
		{"all-trigram", nil},
		{"selective-df0.5", index.FrequencyThresholdSelector{MaxDocFraction: 0.5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			res, err := Run(RunConfig{
				Build: Config{
					Root:       corpus,
					WorkDir:    work,
					Seed:       7,
					ShardBytes: 60, // below a single repo's content → forces multiple shards (union path)
					Selector:   tc.selector,
				},
				Floor:        20,
				SkipZoekt:    true,
				ScanParallel: 2,
				RGParallel:   2,
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			if res.Built.GitEntryCount != 2 {
				t.Errorf("expected 2 .git entries, got %d", res.Built.GitEntryCount)
			}
			if len(res.Built.Repos) != 2 {
				t.Errorf("expected 2 repos discovered, got %d", len(res.Built.Repos))
			}
			if len(res.Built.Shards) < 2 {
				t.Errorf("expected multiple shards with tiny ShardBytes, got %d", len(res.Built.Shards))
			}
			// The selective build must actually persist a selective shard (some gram
			// dropped), else this subtest would silently degrade to all-trigram.
			if tc.selector != nil {
				ix, closer, err := diskstore.LoadMmap(res.Built.Shards[0])
				if err != nil {
					t.Fatalf("load selective shard: %v", err)
				}
				selective := ix.Selective()
				_ = closer.Close()
				if !selective {
					t.Errorf("expected a selective shard (some gram dropped at df=0.5), got all-indexed")
				}
			}
			if !res.RGAvailable || len(res.RGErrors) > 0 {
				t.Errorf("ripgrep unavailable or errored: avail=%v errs=%d", res.RGAvailable, len(res.RGErrors))
			}
			if len(res.UnderApprox) > 0 {
				t.Errorf("AC-D3 under-approximations: %d", len(res.UnderApprox))
				for _, id := range res.UnderApprox {
					qr := res.Results[id]
					t.Logf("  under-approx %s missed=%d", qr.Q, len(qr.GoldMinusMoe))
				}
			}
			if len(res.OverApprox) > 0 {
				t.Errorf("AC-D4 over-approximations: %d", len(res.OverApprox))
			}
			if !res.HardPass() {
				t.Errorf("hard parity gate failed; engine quirks=%d", len(res.EngineQuirks))
				for _, id := range res.EngineQuirks {
					t.Logf("  quirk %s", res.Results[id].Q)
				}
			}
		})
	}
}
