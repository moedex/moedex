package blobstore

// Delta-deduped-reexport PARITY gate (PROOF 1).
//
// Proves that a DELTA deduped re-export (after changing exactly one repo over an
// existing deduped served dir) returns byte-identical (file,line) matches to:
//   - a FULL deduped re-export from the same (refreshed) CAS, and
//   - a DIRECT parity build over the post-change corpus, and
//   - ripgrep over the same content scope F (for the literal buckets rg adjudicates).
// under=0, over=0, dedup!=full=0, dedup!=direct=0. Reuses parity.Generate +
// server.Corpus + the rg-oracle pattern (and the rg-error quirk-skip) from
// cas_export_parity_test.go. Self-contained: builds a multi-repo git fixture so it
// runs in CI without ~/TCGitlab.

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/ingest"
	"moedex/internal/parity"
	server "moedex/internal/serve"
)

// TestDeltaDedupedReexportParity is PROOF (1): change one repo, delta-refresh the
// deduped served dir, and assert it is (file,line)-identical to a full re-export, a
// direct build, and ripgrep over the post-change corpus.
func TestDeltaDedupedReexportParity(t *testing.T) {
	requireGit(t)
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed")
	}

	// --- A multi-repo corpus with cross-repo shared content (so dedup is real) and
	// enough distinct identifiers that the seed-reproducible battery is non-trivial.
	corpus := t.TempDir()
	shared := "package shared\n" + largeBody("CrossRepoNeedle", 60) +
		"func SharedHelper(x int) int { return x * 2 }\n"
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	repoC := filepath.Join(corpus, "repoC")
	repoD := filepath.Join(corpus, "repoD")
	commitGitRepo(t, repoA, map[string]string{
		"shared.go": shared,
		"alpha.go":  "package a\nfunc AlphaCompute() string { return \"alpha_token_value\" }\n",
	})
	commitGitRepo(t, repoB, map[string]string{
		"shared.go": shared,
		"bravo.go":  "package b\nconst BravoConstant = \"bravo_token_value\"\n",
	})
	commitGitRepo(t, repoC, map[string]string{
		"charlie.go": "package c\nfunc CharlieHandler() { /* charlie_marker */ }\n" + largeBody("CharlieFiller", 30),
	})
	commitGitRepo(t, repoD, map[string]string{
		"delta.go": "package d\nvar DeltaRegistry = map[string]int{\"delta_key\": 7}\n",
	})

	// shardBytes small enough to force multiple shards across the four repos, so the
	// carry-forward (untouched shards) path is genuinely exercised.
	const shardBytes = 1 << 11

	// --- BASELINE: CAS state 1 + full deduped export -> the dir we will delta. -----
	casDir := t.TempDir()
	if _, err := BuildCAS(corpus, casDir); err != nil {
		t.Fatalf("BuildCAS (state 1): %v", err)
	}
	deltaDir := filepath.Join(t.TempDir(), "deduped-delta")
	if _, _, err := ExportDedupedShardDir(casDir, deltaDir, shardBytes); err != nil {
		t.Fatalf("ExportDedupedShardDir (baseline): %v", err)
	}

	// --- Mutate exactly ONE repo (repoB): change a file AND add a file, commit. ----
	writeFiles(t, repoB, map[string]string{
		"bravo.go": "package b\nconst BravoConstant = \"bravo_token_value\"\nfunc BravoExtra() string { return \"newly_added_bravo\" }\n",
		"echo.go":  "package b\nfunc EchoEmitted() { /* echo_marker_unique */ }\n",
	})
	gitCommitAll(t, repoB, "mutate repoB")

	// Refresh the CAS to state 2 (per-blob delta on the storage side).
	casManifest1, err := LoadBlobManifest(filepath.Join(casDir, BlobManifestName))
	if err != nil {
		t.Fatalf("load CAS manifest: %v", err)
	}
	if _, _, err := RefreshCAS(casManifest1, corpus, casDir); err != nil {
		t.Fatalf("RefreshCAS (state 2): %v", err)
	}

	// --- DELTA re-export over the baseline dir (the path under test). --------------
	_, ds, err := RefreshDedupedShardDir(casDir, deltaDir, shardBytes)
	if err != nil {
		t.Fatalf("RefreshDedupedShardDir: %v", err)
	}
	if len(ds.ChangedRepos) != 1 || ds.ChangedRepos[0] != repoB {
		t.Fatalf("delta ChangedRepos = %v, want [repoB]", ds.ChangedRepos)
	}
	if len(ds.AddedRepos) != 0 || len(ds.RemovedRepos) != 0 {
		t.Errorf("unexpected added/removed in 1-repo-change delta: %+v", ds)
	}
	if ds.ShardsRewritten < 1 {
		t.Errorf("delta rewrote %d shards, want >=1", ds.ShardsRewritten)
	}
	if ds.ShardsCarried < 1 {
		t.Errorf("delta carried %d shards, want >=1 (the untouched repos)", ds.ShardsCarried)
	}
	t.Logf("DELTA: changed=%v rewritten=%d carried=%d appended=%dblobs/%dB dedup-skipped=%d",
		ds.ChangedRepos, ds.ShardsRewritten, ds.ShardsCarried, ds.BlobsAppended, ds.BytesAppended, ds.PutsDeduped)

	// --- FULL deduped re-export from the SAME refreshed CAS (the equivalence target).
	fullDir := filepath.Join(t.TempDir(), "deduped-full")
	if _, _, err := ExportDedupedShardDir(casDir, fullDir, shardBytes); err != nil {
		t.Fatalf("ExportDedupedShardDir (full re-export): %v", err)
	}

	// --- DIRECT parity build over the post-change corpus + rg mirror + battery. ----
	work := t.TempDir()
	const seed = 11
	built, err := parity.Build(parity.Config{
		Root:       corpus,
		WorkDir:    work,
		Seed:       seed,
		ShardBytes: shardBytes,
		Logf:       t.Logf,
	})
	if err != nil {
		t.Fatalf("parity.Build (direct, post-change): %v", err)
	}
	if built.IngestedRepos == 0 {
		t.Skip("no repos ingested")
	}
	bat := parity.Generate(built.Pool, seed)
	if len(bat.Queries) == 0 {
		t.Fatal("empty battery")
	}
	directDir := filepath.Join(work, "shards")

	// --- Open all three served dirs through the real serving spine. ----------------
	delta, err := server.Open(deltaDir)
	if err != nil {
		t.Fatalf("server.Open delta dir: %v", err)
	}
	defer delta.Close()
	full, err := server.Open(fullDir)
	if err != nil {
		t.Fatalf("server.Open full dir: %v", err)
	}
	defer full.Close()
	direct, err := server.Open(directDir)
	if err != nil {
		t.Fatalf("server.Open direct dir: %v", err)
	}
	defer direct.Close()

	// --- Per-query: delta == full == direct == rg over F. --------------------------
	rg := newRG(t, built.MirrorDir)
	var (
		mismatchFull   int // delta != full re-export (the delta seam is broken)
		mismatchDirect int // delta != direct build
		underDelta     int // delta missed a line rg found (SACRED)
		overDelta      int // delta returned a line rg did not
		checkedVsRG    int
		rgSkipped      int
	)
	for _, q := range bat.Queries {
		deltaLocs := corpusQuery(t, delta, q)
		fullLocs := corpusQuery(t, full, q)
		directLocs := corpusQuery(t, direct, q)

		if miss, extra := diffLocs(fullLocs, deltaLocs); len(miss) > 0 || len(extra) > 0 {
			mismatchFull++
			if mismatchFull <= 8 {
				t.Errorf("delta != FULL re-export for %s:\n  full has, delta missing (%d): %s\n  delta has, full missing (%d): %s",
					q, len(miss), sampleLocs(miss), len(extra), sampleLocs(extra))
			}
		}
		if miss, extra := diffLocs(directLocs, deltaLocs); len(miss) > 0 || len(extra) > 0 {
			mismatchDirect++
			if mismatchDirect <= 8 {
				t.Errorf("delta != DIRECT build for %s:\n  direct has, delta missing (%d): %s\n  delta has, direct missing (%d): %s",
					q, len(miss), sampleLocs(miss), len(extra), sampleLocs(extra))
			}
			continue
		}

		want, ok, rgErr := rg.run(t, q, built.FT)
		if !ok {
			if rgErr {
				rgSkipped++
			}
			continue
		}
		checkedVsRG++
		if miss, extra := diffLocs(want, deltaLocs); len(miss) > 0 || len(extra) > 0 {
			if len(miss) > 0 {
				underDelta++
				if underDelta <= 8 {
					t.Errorf("UNDER-APPROX delta vs rg for %s: missed %d (e.g. %s)", q, len(miss), sampleLocs(miss))
				}
			}
			if len(extra) > 0 {
				overDelta++
				if overDelta <= 8 {
					t.Errorf("OVER-APPROX delta vs rg for %s: extra %d (e.g. %s)", q, len(extra), sampleLocs(extra))
				}
			}
		}
	}

	t.Logf("delta parity: %d queries; delta!=full=%d delta!=direct=%d; vs-rg checked=%d under=%d over=%d rg-skipped=%d",
		len(bat.Queries), mismatchFull, mismatchDirect, checkedVsRG, underDelta, overDelta, rgSkipped)
	if mismatchFull > 0 {
		t.Fatalf("FAIL: delta diverged from a FULL deduped re-export on %d queries (delta seam not parity-clean)", mismatchFull)
	}
	if mismatchDirect > 0 {
		t.Fatalf("FAIL: delta diverged from the direct build on %d queries", mismatchDirect)
	}
	if underDelta > 0 {
		t.Fatalf("FAIL: delta UNDER-APPROXIMATED ripgrep on %d queries — SACRED parity violation", underDelta)
	}
	if overDelta > 0 {
		t.Fatalf("FAIL: delta OVER-APPROXIMATED ripgrep on %d queries", overDelta)
	}

	// --- Spot-check the specific change is reflected (newly-added content findable,
	// and the now-absent prior content is NOT — a stale carry would fail this). -----
	mustFindLiteralCtx(t, delta, "newly_added_bravo", "bravo.go")
	mustFindLiteralCtx(t, delta, "echo_marker_unique", "echo.go")
	// Unchanged co-resident repos still findable through the delta dir.
	mustFindLiteralCtx(t, delta, "charlie_marker", "charlie.go")
	mustFindLiteralCtx(t, delta, "delta_key", "delta.go")
	mustFindLiteralCtx(t, delta, "CrossRepoNeedle", "shared.go")
}

// mustFindLiteralCtx asserts at least one match for q whose base RelPath == wantRel.
func mustFindLiteralCtx(t *testing.T, c *server.Corpus, q, wantRel string) {
	t.Helper()
	ms, _, err := c.Literal(context.Background(), q)
	if err != nil {
		t.Fatalf("Literal(%q): %v", q, err)
	}
	for _, m := range ms {
		if filepath.Base(m.RelPath) == wantRel {
			return
		}
	}
	got := make([]string, 0, len(ms))
	for _, m := range ms {
		got = append(got, m.RelPath)
	}
	t.Errorf("Literal(%q) found no match in %s (got %d matches: %s)", q, wantRel, len(ms), strings.Join(got, ", "))
}

// ensure ingest import is used even if the helper set changes.
var _ = ingest.DiscoverRepos
