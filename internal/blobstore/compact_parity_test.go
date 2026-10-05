package blobstore

// Compaction-GC PARITY gate (PROOF 1 of the compaction lane).
//
// Proves that compacting either content store (after creating dead blobs by removing
// a repo AND churning content so unreferenced blobs exist) returns byte-identical
// (file,line) matches to:
//   - the PRE-compaction LIVE results (the store before compaction), and
//   - a FRESH DIRECT parity build over the post-change corpus, and
//   - ripgrep over the same content scope F (for the literal buckets rg adjudicates).
// under=0, over=0, compacted!=pre=0, compacted!=direct=0. Reuses the parity-gate
// pattern (parity.Generate + server.Corpus + the rg-oracle / rg-error quirk-skip)
// from cas_export_parity_test.go / refresh_deduped_parity_test.go. Self-contained:
// builds a multi-repo git fixture so it runs in CI without ~/.moedex-managed.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/parity"
	server "moedex/internal/serve"
)

// mustNotFindLiteral fails if an ALREADY-OPEN corpus serves any match for marker —
// the live-side proof that dead (removed/churned-away) content no longer surfaces.
func mustNotFindLiteral(t *testing.T, c *server.Corpus, marker string) {
	t.Helper()
	ms, _, err := c.Literal(context.Background(), marker)
	if err != nil {
		t.Fatalf("Literal(%q): %v", marker, err)
	}
	if len(ms) != 0 {
		t.Errorf("compacted dir still serves dead marker %q (%d matches) — dead content was not reclaimed", marker, len(ms))
	}
}

// buildDeadBlobCorpus builds a 4-repo corpus, a CAS, and a deduped served dir, then
// creates DEAD (unreferenced) content two ways: (1) REMOVE a whole repo (its blobs go
// unreferenced), and (2) CHURN a file in another repo (the old content goes
// unreferenced) — then refreshes the CAS and delta-re-exports the deduped dir so both
// stores carry the dead content forward (the append-only behavior compaction fixes).
//
// It returns the corpus root, the casDir, the deduped served dir, and the marker
// strings: deadRepoMarker (only in the removed repo, must be GONE post-removal),
// deadContentMarker (the churned-away old line, must be GONE), liveNewMarker (the
// churned-in new line, must be PRESENT), and a survivor marker (an untouched repo).
func buildDeadBlobCorpus(t *testing.T) (corpus, casDir, dedupDir string,
	deadRepoMarker, deadContentMarker, liveNewMarker, survivorMarker string) {
	t.Helper()
	corpus = t.TempDir()
	// Cross-repo shared content so dedup is real; distinct identifiers for a non-trivial
	// seed battery.
	shared := "package shared\n" + largeBody("CrossRepoNeedle", 60) +
		"func SharedHelper(x int) int { return x * 2 }\n"
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	repoC := filepath.Join(corpus, "repoC") // this repo will be REMOVED
	repoD := filepath.Join(corpus, "repoD")
	deadRepoMarker = "doomed_repo_c_unique_marker"
	deadContentMarker = "stale_bravo_old_content_marker"
	liveNewMarker = "fresh_bravo_new_content_marker"
	survivorMarker = "delta_key"

	commitGitRepo(t, repoA, map[string]string{
		"shared.go": shared,
		"alpha.go":  "package a\nfunc AlphaCompute() string { return \"alpha_token_value\" }\n",
	})
	commitGitRepo(t, repoB, map[string]string{
		"shared.go": shared,
		"bravo.go":  "package b\nconst BravoConstant = \"" + deadContentMarker + "\"\n" + largeBody("BravoFiller", 30),
	})
	commitGitRepo(t, repoC, map[string]string{
		"charlie.go": "package c\nfunc CharlieHandler() { /* " + deadRepoMarker + " */ }\n" + largeBody("CharlieFiller", 50),
	})
	commitGitRepo(t, repoD, map[string]string{
		"delta.go": "package d\nvar DeltaRegistry = map[string]int{\"" + survivorMarker + "\": 7}\n",
	})

	const shardBytes = 1 << 11 // force multiple shards across repos

	casDir = t.TempDir()
	if _, err := BuildCAS(corpus, casDir); err != nil {
		t.Fatalf("BuildCAS: %v", err)
	}
	dedupDir = filepath.Join(t.TempDir(), "deduped")
	if _, _, err := ExportDedupedShardDir(casDir, dedupDir, shardBytes); err != nil {
		t.Fatalf("ExportDedupedShardDir baseline: %v", err)
	}

	// --- Create dead blobs. (1) Churn repoB's bravo.go (old content -> dead). -------
	writeFiles(t, repoB, map[string]string{
		"bravo.go": "package b\nconst BravoConstant = \"" + liveNewMarker + "\"\n" + largeBody("BravoFiller", 30),
	})
	gitCommitAll(t, repoB, "churn bravo.go")
	// (2) REMOVE repoC entirely (its blobs -> dead).
	if err := os.RemoveAll(repoC); err != nil {
		t.Fatalf("remove repoC: %v", err)
	}

	// Refresh the CAS (per-blob delta: adds the new bravo blob, drops repoC's manifest
	// entry but LEAVES its blobs + the old bravo blob in the pack — the dead content).
	cm, err := LoadBlobManifest(filepath.Join(casDir, BlobManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if _, ds, err := RefreshCAS(cm, corpus, casDir); err != nil {
		t.Fatalf("RefreshCAS: %v", err)
	} else if len(ds.RemovedRepos) != 1 {
		t.Fatalf("RefreshCAS removed %v, want exactly repoC", ds.RemovedRepos)
	}
	// Delta-re-export the deduped served dir so blobs.dat carries the dead content
	// forward (the append-only served behavior compaction reclaims).
	if _, _, err := RefreshDedupedShardDir(casDir, dedupDir, shardBytes); err != nil {
		t.Fatalf("RefreshDedupedShardDir: %v", err)
	}
	return corpus, casDir, dedupDir, deadRepoMarker, deadContentMarker, liveNewMarker, survivorMarker
}

// TestCompactionParity is the headline gate: dead blobs exist, compact both stores,
// and assert each compacted store is (file,line)-identical to its pre-compaction live
// state, to a fresh direct build, and to ripgrep.
func TestCompactionParity(t *testing.T) {
	requireGit(t)
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed")
	}
	const shardBytes = 1 << 11
	corpus, casDir, dedupDir, deadRepoMarker, deadContentMarker, liveNewMarker, survivorMarker := buildDeadBlobCorpus(t)

	// ---- Capture the PRE-compaction LIVE match sets from BOTH served formats. -------
	// (a) The deduped served dir (post-refresh, dead content present in blobs.dat).
	preDedup, err := server.Open(dedupDir)
	if err != nil {
		t.Fatalf("server.Open deduped (pre-compact): %v", err)
	}
	// (b) A CAS-exported deduped dir from the CURRENT (post-refresh) CAS — this is the
	// store CompactCAS will compact, exported so we can compare CAS pre/post by serving.
	preCASExport := filepath.Join(t.TempDir(), "pre-cas-export")
	if _, _, err := ExportDedupedShardDir(casDir, preCASExport, shardBytes); err != nil {
		t.Fatalf("ExportDedupedShardDir (pre-compact CAS view): %v", err)
	}
	preCAS, err := server.Open(preCASExport)
	if err != nil {
		t.Fatalf("server.Open CAS export (pre-compact): %v", err)
	}

	// ---- Record dead/live footprint BEFORE compaction (for the space assertion). ----
	casBefore := casStoredBytes(t, casDir)
	dedupBeforeBytes := storeContentBytes(t, filepath.Join(dedupDir, diskstore.ContentStoreName))

	// ---- COMPACT BOTH STORES. -------------------------------------------------------
	casSt, err := CompactCAS(casDir)
	if err != nil {
		t.Fatalf("CompactCAS: %v", err)
	}
	t.Logf("CAS compact: kept %d/%dB live, reclaimed %d/%dB dead (%dB -> %dB)",
		casSt.LiveBlobs, casSt.LiveBytes, casSt.DeadBlobs, casSt.DeadBytes, casSt.BeforeBytes, casSt.AfterBytes)
	dedupSt, err := CompactDedupedShardDir(dedupDir)
	if err != nil {
		t.Fatalf("CompactDedupedShardDir: %v", err)
	}
	t.Logf("deduped compact: kept %d/%dB live, reclaimed %d/%dB dead (%dB -> %dB), %d shards carried",
		dedupSt.LiveBlobs, dedupSt.LiveBytes, dedupSt.DeadBlobs, dedupSt.DeadBytes, dedupSt.BeforeBytes, dedupSt.AfterBytes, dedupSt.Shards)

	// ---- SPACE: dead bytes reclaimed; post == live; strictly less than before. ------
	if casSt.DeadBytes <= 0 {
		t.Errorf("CAS compaction reclaimed no dead bytes (%d) — the fixture should have dead blobs", casSt.DeadBytes)
	}
	if casSt.AfterBytes != casSt.LiveBytes {
		t.Errorf("CAS after %d != live %d", casSt.AfterBytes, casSt.LiveBytes)
	}
	if casSt.AfterBytes >= casBefore {
		t.Errorf("CAS not smaller after compaction: before %d, after %d", casBefore, casSt.AfterBytes)
	}
	if dedupSt.DeadBytes <= 0 {
		t.Errorf("deduped compaction reclaimed no dead bytes (%d)", dedupSt.DeadBytes)
	}
	if dedupSt.AfterBytes >= dedupBeforeBytes {
		t.Errorf("deduped not smaller after compaction: before %d, after %d", dedupBeforeBytes, dedupSt.AfterBytes)
	}
	// Post-compaction CAS footprint == post-compaction deduped footprint == the unique
	// LIVE content size (a fresh full build's size). Re-derive both and compare.
	if casSt.AfterBytes != dedupSt.AfterBytes {
		t.Errorf("compacted CAS content %d != compacted deduped content %d (both should equal live unique content)", casSt.AfterBytes, dedupSt.AfterBytes)
	}

	// ---- Open the POST-compaction served dirs. --------------------------------------
	postDedup, err := server.Open(dedupDir)
	if err != nil {
		t.Fatalf("server.Open deduped (post-compact): %v", err)
	}
	defer postDedup.Close()
	postCASExport := filepath.Join(t.TempDir(), "post-cas-export")
	if _, _, err := ExportDedupedShardDir(casDir, postCASExport, shardBytes); err != nil {
		t.Fatalf("ExportDedupedShardDir (post-compact CAS view): %v", err)
	}
	postCAS, err := server.Open(postCASExport)
	if err != nil {
		t.Fatalf("server.Open CAS export (post-compact): %v", err)
	}
	defer postCAS.Close()

	// A fresh DIRECT parity build over the post-change corpus (the independent oracle).
	work := t.TempDir()
	const seed = 13
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
	direct, err := server.Open(directDir)
	if err != nil {
		t.Fatalf("server.Open direct dir: %v", err)
	}
	defer direct.Close()

	// ---- Per-query: postCAS == preCAS == direct == rg; postDedup == preDedup == direct == rg.
	rg := newRG(t, built.MirrorDir)
	var (
		mismatchCASPre                           int // compacted CAS != pre-compaction CAS (compaction changed the match set)
		mismatchDedupPre                         int // compacted deduped != pre-compaction deduped
		mismatchCASDir                           int // compacted CAS != direct build
		mismatchDedupDir                         int // compacted deduped != direct build
		underCAS, overCAS, underDedup, overDedup int
		checkedVsRG, rgSkipped                   int
	)
	for _, q := range bat.Queries {
		preCASLocs := corpusQuery(t, preCAS, q)
		postCASLocs := corpusQuery(t, postCAS, q)
		preDedupLocs := corpusQuery(t, preDedup, q)
		postDedupLocs := corpusQuery(t, postDedup, q)
		directLocs := corpusQuery(t, direct, q)

		if miss, extra := diffLocs(preCASLocs, postCASLocs); len(miss) > 0 || len(extra) > 0 {
			mismatchCASPre++
			if mismatchCASPre <= 8 {
				t.Errorf("compacted CAS != pre-compaction CAS for %s:\n  pre has, post missing (%d): %s\n  post has, pre missing (%d): %s",
					q, len(miss), sampleLocs(miss), len(extra), sampleLocs(extra))
			}
		}
		if miss, extra := diffLocs(preDedupLocs, postDedupLocs); len(miss) > 0 || len(extra) > 0 {
			mismatchDedupPre++
			if mismatchDedupPre <= 8 {
				t.Errorf("compacted deduped != pre-compaction deduped for %s:\n  pre has, post missing (%d): %s\n  post has, pre missing (%d): %s",
					q, len(miss), sampleLocs(miss), len(extra), sampleLocs(extra))
			}
		}
		dedupOK := true
		if miss, extra := diffLocs(directLocs, postCASLocs); len(miss) > 0 || len(extra) > 0 {
			mismatchCASDir++
			if mismatchCASDir <= 8 {
				t.Errorf("compacted CAS != direct for %s: miss %d extra %d", q, len(miss), len(extra))
			}
		}
		if miss, extra := diffLocs(directLocs, postDedupLocs); len(miss) > 0 || len(extra) > 0 {
			dedupOK = false
			mismatchDedupDir++
			if mismatchDedupDir <= 8 {
				t.Errorf("compacted deduped != direct for %s: miss %d extra %d", q, len(miss), len(extra))
			}
		}
		if !dedupOK {
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
		if miss, extra := diffLocs(want, postCASLocs); len(miss) > 0 {
			underCAS++
		} else if len(extra) > 0 {
			overCAS++
		}
		if miss, extra := diffLocs(want, postDedupLocs); len(miss) > 0 {
			underDedup++
		} else if len(extra) > 0 {
			overDedup++
		}
	}

	t.Logf("compaction parity: %d queries; CAS!=pre=%d dedup!=pre=%d CAS!=direct=%d dedup!=direct=%d; vs-rg checked=%d under(cas=%d dedup=%d) over(cas=%d dedup=%d) rg-skipped=%d",
		len(bat.Queries), mismatchCASPre, mismatchDedupPre, mismatchCASDir, mismatchDedupDir, checkedVsRG, underCAS, underDedup, overCAS, overDedup, rgSkipped)
	if mismatchCASPre > 0 || mismatchDedupPre > 0 {
		t.Fatalf("FAIL: compaction CHANGED the live match set (CAS=%d dedup=%d) — a live blob was dropped (SACRED under-approximation) or content shifted", mismatchCASPre, mismatchDedupPre)
	}
	if mismatchCASDir > 0 || mismatchDedupDir > 0 {
		t.Fatalf("FAIL: compacted store diverged from a fresh direct build (CAS=%d dedup=%d)", mismatchCASDir, mismatchDedupDir)
	}
	if underCAS > 0 || underDedup > 0 {
		t.Fatalf("FAIL: compacted store UNDER-APPROXIMATED ripgrep (cas=%d dedup=%d) — SACRED parity violation", underCAS, underDedup)
	}
	if overCAS > 0 || overDedup > 0 {
		t.Fatalf("FAIL: compacted store OVER-APPROXIMATED ripgrep (cas=%d dedup=%d)", overCAS, overDedup)
	}
	preCAS.Close()
	preDedup.Close()

	// ---- Targeted dead/live checks on the post-compaction deduped dir. --------------
	// Every LIVE marker still surfaces (no live content dropped):
	mustFindLiteralCtx(t, postDedup, liveNewMarker, "bravo.go")      // churned-in new content
	mustFindLiteralCtx(t, postDedup, survivorMarker, "delta.go")     // untouched repo
	mustFindLiteralCtx(t, postDedup, "CrossRepoNeedle", "shared.go") // shared content
	// Every DEAD marker is GONE (the removed repo + churned-away content). The marker
	// being absent from the SEARCH RESULTS is the live-side proof; the blob bytes being
	// physically gone from the store is asserted in TestCompactionRemovesDeadBlobBytes.
	mustNotFindLiteral(t, postDedup, deadRepoMarker)
	mustNotFindLiteral(t, postDedup, deadContentMarker)
}

// storeContentBytes opens a MOECONT1 store and returns its stored content bytes.
func storeContentBytes(t *testing.T, path string) int64 {
	t.Helper()
	cs, err := diskstore.OpenContentStore(path)
	if err != nil {
		t.Fatalf("OpenContentStore %s: %v", path, err)
	}
	defer cs.Close()
	return cs.BytesStored()
}
