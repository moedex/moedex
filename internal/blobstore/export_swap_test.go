package blobstore

// Crash-safe-swap regression gate for F-13 (CODEBASE-REVIEW.md).
//
// Before the fix, ExportShardDir / ExportDedupedShardDir wrote shard files (and,
// for the deduped export, the manifest) directly into the LIVE outShardDir, with
// no staging dir and no atomic swap — unlike every other rewriting operation in
// this package. A failure partway through a full/forced re-export (the
// `cas-export [-deduped] -force` path) could overwrite part of an existing,
// complete export before erroring, leaving the live dir neither the old export
// nor a new one, with no RecoverInterruptedXXX counterpart to self-heal.
//
// These tests prove:
//   - a failing re-export never touches an existing live dir (it is staged under
//     a sibling temp dir first);
//   - RecoverInterruptedExport (the new plain-export counterpart to
//     RecoverInterruptedDedupedSwap) rolls an interrupted swap forward or back,
//     exactly like the existing deduped-swap recovery tests in
//     refresh_deduped_swap_test.go.

import (
	"os"
	"path/filepath"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/parity"
)

// corruptRepoFileSHA rewrites the CAS's BlobManifest so repoDir's FIRST recorded
// file resolves to badSHA (a SHA absent from the store), forcing exportShards to
// fail with a "not found" error the next time that repo is replayed — without
// touching any earlier-processed repo's data.
func corruptRepoFileSHA(t *testing.T, casDir, repoDir, badSHA string) {
	t.Helper()
	path := filepath.Join(casDir, BlobManifestName)
	m, err := LoadBlobManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range m.Repos {
		if m.Repos[i].Dir == repoDir {
			if len(m.Repos[i].Files) == 0 {
				t.Fatalf("repo %s has no files to corrupt", repoDir)
			}
			m.Repos[i].Files[0].SHA = badSHA
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("repo %s not found in blob manifest", repoDir)
	}
	if err := WriteBlobManifest(path, m); err != nil {
		t.Fatal(err)
	}
}

// TestExportShardDirFailureLeavesExistingLiveDirUntouched is the core plain-export
// regression test for F-13: a failing re-export must not touch an existing,
// complete live shard dir at all — not even the shards of repos processed (and
// flushed) before the failing repo was reached.
func TestExportShardDirFailureLeavesExistingLiveDirUntouched(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	commitGitRepo(t, repoA, map[string]string{"a.go": "package a\n" + largeBody("KeepAlphaMarker", 40)})
	commitGitRepo(t, repoB, map[string]string{"b.go": "package b\n" + largeBody("KeepBravoMarker", 40)})

	casDir := t.TempDir()
	if _, err := BuildCAS(corpus, casDir); err != nil {
		t.Fatalf("BuildCAS: %v", err)
	}

	liveDir := filepath.Join(t.TempDir(), "served")
	// Tiny shard budget: repoA alone exceeds it, so shard-0000.idx is flushed
	// (and, pre-fix, would already be sitting in the live dir) before repoB
	// (the repo we are about to corrupt) is ever reached.
	const shardBytes = 1 << 10
	if _, err := ExportShardDir(casDir, liveDir, shardBytes); err != nil {
		t.Fatalf("ExportShardDir baseline: %v", err)
	}

	beforeManifest, err := parity.LoadManifest(filepath.Join(liveDir, parity.ManifestName))
	if err != nil {
		t.Fatalf("load baseline manifest: %v", err)
	}
	if len(beforeManifest.Shards) < 2 {
		t.Fatalf("expected the baseline export to span >= 2 shards, got %d", len(beforeManifest.Shards))
	}
	beforeShard0, err := os.ReadFile(filepath.Join(liveDir, "shard-0000.idx"))
	if err != nil {
		t.Fatalf("read baseline shard-0000: %v", err)
	}

	// Corrupt repoB's manifest entry so the re-export fails partway through.
	corruptRepoFileSHA(t, casDir, repoB, "0000000000000000000000000000000000000000")

	if _, err := ExportShardDir(casDir, liveDir, shardBytes); err == nil {
		t.Fatalf("ExportShardDir over corrupt manifest: expected an error, got none")
	}

	// The live dir must be EXACTLY the untouched baseline.
	afterManifest, err := parity.LoadManifest(filepath.Join(liveDir, parity.ManifestName))
	if err != nil {
		t.Fatalf("live dir manifest unreadable after failed re-export: %v", err)
	}
	if len(afterManifest.Shards) != len(beforeManifest.Shards) {
		t.Errorf("live dir shard count changed after failed re-export: got %d, want %d",
			len(afterManifest.Shards), len(beforeManifest.Shards))
	}
	afterShard0, err := os.ReadFile(filepath.Join(liveDir, "shard-0000.idx"))
	if err != nil {
		t.Fatalf("live dir shard-0000.idx unreadable after failed re-export: %v", err)
	}
	if string(afterShard0) != string(beforeShard0) {
		t.Errorf("live dir shard-0000.idx was modified by the failed re-export (F-13 regression: " +
			"export wrote into the live dir before the new export was complete)")
	}
	assertServesConsistently(t, liveDir, "KeepAlphaMarker")
	assertServesConsistently(t, liveDir, "KeepBravoMarker")
	assertNoPlainExportSwapMarkers(t, liveDir)
}

// TestExportDedupedShardDirFailureLeavesExistingLiveDirUntouched is the deduped
// counterpart: ExportDedupedShardDir must not touch an existing live deduped dir
// (shards OR the shared blobs.dat) when a re-export fails partway through.
func TestExportDedupedShardDirFailureLeavesExistingLiveDirUntouched(t *testing.T) {
	requireGit(t)
	corpus := t.TempDir()
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	commitGitRepo(t, repoA, map[string]string{"a.go": "package a\n" + largeBody("DedupKeepAlpha", 40)})
	commitGitRepo(t, repoB, map[string]string{"b.go": "package b\n" + largeBody("DedupKeepBravo", 40)})

	casDir := t.TempDir()
	if _, err := BuildCAS(corpus, casDir); err != nil {
		t.Fatalf("BuildCAS: %v", err)
	}

	liveDir := filepath.Join(t.TempDir(), "served")
	const shardBytes = 1 << 10
	if _, _, err := ExportDedupedShardDir(casDir, liveDir, shardBytes); err != nil {
		t.Fatalf("ExportDedupedShardDir baseline: %v", err)
	}

	beforeManifest, err := parity.LoadManifest(filepath.Join(liveDir, parity.ManifestName))
	if err != nil {
		t.Fatalf("load baseline manifest: %v", err)
	}
	if len(beforeManifest.Shards) < 2 {
		t.Fatalf("expected the baseline export to span >= 2 shards, got %d", len(beforeManifest.Shards))
	}
	beforeContentStore, err := os.ReadFile(filepath.Join(liveDir, diskstore.ContentStoreName))
	if err != nil {
		t.Fatalf("read baseline blobs.dat: %v", err)
	}

	corruptRepoFileSHA(t, casDir, repoB, "0000000000000000000000000000000000000000")

	if _, _, err := ExportDedupedShardDir(casDir, liveDir, shardBytes); err == nil {
		t.Fatalf("ExportDedupedShardDir over corrupt manifest: expected an error, got none")
	}

	afterManifest, err := parity.LoadManifest(filepath.Join(liveDir, parity.ManifestName))
	if err != nil {
		t.Fatalf("live dir manifest unreadable after failed re-export: %v", err)
	}
	if len(afterManifest.Shards) != len(beforeManifest.Shards) {
		t.Errorf("live dir shard count changed after failed re-export: got %d, want %d",
			len(afterManifest.Shards), len(beforeManifest.Shards))
	}
	afterContentStore, err := os.ReadFile(filepath.Join(liveDir, diskstore.ContentStoreName))
	if err != nil {
		t.Fatalf("live dir blobs.dat unreadable after failed re-export: %v", err)
	}
	if string(afterContentStore) != string(beforeContentStore) {
		t.Errorf("live dir blobs.dat was modified by the failed re-export (F-13 regression)")
	}
	assertServesConsistently(t, liveDir, "DedupKeepAlpha")
	assertServesConsistently(t, liveDir, "DedupKeepBravo")
	assertNoSwapMarkers(t, liveDir) // reuses the dedup-swap marker check (refresh_deduped_swap_test.go)
}

// buildBaselineAndRefreshedPlainExport builds a 2-repo corpus, a baseline PLAIN
// export at liveDir, mutates one repo and rebuilds the CAS, mirroring
// buildBaselineAndStagedDelta (refresh_deduped_swap_test.go) but for the plain,
// non-delta ExportShardDir path (which always does a full re-export).
func buildBaselineAndRefreshedPlainExport(t *testing.T) (liveDir, casDir, newMarker, oldMarker string) {
	t.Helper()
	corpus := t.TempDir()
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	commitGitRepo(t, repoA, map[string]string{"a.go": "package a\n" + largeBody("PlainAlphaKeep", 40)})
	commitGitRepo(t, repoB, map[string]string{"b.go": "package b\nconst OldPlainBravoMarker = 1\n" + largeBody("PlainBravoBody", 40)})

	casDir = t.TempDir()
	if _, err := BuildCAS(corpus, casDir); err != nil {
		t.Fatalf("BuildCAS: %v", err)
	}
	liveDir = filepath.Join(t.TempDir(), "served")
	if _, err := ExportShardDir(casDir, liveDir, 1<<10); err != nil {
		t.Fatalf("ExportShardDir baseline: %v", err)
	}

	newMarker = "uniquely_new_plain_bravo_marker"
	writeFiles(t, repoB, map[string]string{"new.go": "package b\nfunc New() string { return \"" + newMarker + "\" }\n"})
	gitCommitAll(t, repoB, "add new.go")
	cm, err := LoadBlobManifest(filepath.Join(casDir, BlobManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := RefreshCAS(cm, corpus, casDir); err != nil {
		t.Fatalf("RefreshCAS: %v", err)
	}
	return liveDir, casDir, newMarker, "OldPlainBravoMarker"
}

// assertNoPlainExportSwapMarkers fails if any ".export-*" / ".export-bak-*"
// sibling of dir survives (debris must be cleaned), mirroring assertNoSwapMarkers
// (refresh_deduped_swap_test.go) but for the plain export's own markers.
func assertNoPlainExportSwapMarkers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(dir)
	for _, e := range entries {
		n := e.Name()
		if n == base {
			continue
		}
		if hasSwapPrefix(n, base+exportBakSuffix) || hasSwapPrefix(n, base+exportTmpSuffix) {
			t.Errorf("leftover swap marker after recovery: %s", n)
		}
	}
}

// TestRecoverInterruptedExport_WindowA_RollForward reproduces a crash BETWEEN the
// two swap renames with a COMPLETE new dir staged: recovery must roll FORWARD.
func TestRecoverInterruptedExport_WindowA_RollForward(t *testing.T) {
	requireGit(t)
	liveDir, casDir, newMarker, _ := buildBaselineAndRefreshedPlainExport(t)

	oldCopy := liveDir + ".oldsnapshot"
	if err := copyDir(t, liveDir, oldCopy); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportShardDir(casDir, liveDir, 1<<10); err != nil {
		t.Fatalf("ExportShardDir (re-export): %v", err)
	}
	// liveDir is now the COMPLETE new dir. Reproduce window A: move it to
	// .export-<stamp>, move the old snapshot to .export-bak-<stamp>, remove live.
	stamp := "20260101-000000.000000000"
	tmpSib := liveDir + exportTmpSuffix + stamp
	bakSib := liveDir + exportBakSuffix + stamp
	if err := os.Rename(liveDir, tmpSib); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldCopy, bakSib); err != nil {
		t.Fatal(err)
	}

	if err := RecoverInterruptedExport(liveDir); err != nil {
		t.Fatalf("RecoverInterruptedExport: %v", err)
	}
	assertServesConsistently(t, liveDir, newMarker)
	assertNoPlainExportSwapMarkers(t, liveDir)
}

// TestRecoverInterruptedExport_WindowB_RollBack reproduces a crash BETWEEN the two
// swap renames with the new dir NOT yet complete: recovery must roll BACK.
func TestRecoverInterruptedExport_WindowB_RollBack(t *testing.T) {
	requireGit(t)
	liveDir, casDir, newMarker, oldMarker := buildBaselineAndRefreshedPlainExport(t)

	stamp := "20260101-000000.000000000"
	bakSib := liveDir + exportBakSuffix + stamp
	tmpSib := liveDir + exportTmpSuffix + stamp

	oldSnap := liveDir + ".oldsnap"
	if err := copyDir(t, liveDir, oldSnap); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportShardDir(casDir, liveDir, 1<<10); err != nil {
		t.Fatalf("ExportShardDir (re-export, to build a complete new dir): %v", err)
	}
	// liveDir is now the complete NEW dir. Stage window B: .export-* = NEW minus
	// its manifest (incomplete), .export-bak-* = the OLD snapshot (complete),
	// live gone.
	if err := copyDir(t, liveDir, tmpSib); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(tmpSib, parity.ManifestName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldSnap, bakSib); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(liveDir); err != nil {
		t.Fatal(err)
	}

	if err := RecoverInterruptedExport(liveDir); err != nil {
		t.Fatalf("RecoverInterruptedExport: %v", err)
	}
	assertServesConsistently(t, liveDir, oldMarker)
	assertNoPlainExportSwapMarkers(t, liveDir)
	// And it genuinely rolled BACK: the new marker must NOT be present, because
	// the new content lived only in the INCOMPLETE .export-* dir we discarded.
	assertDoesNotServe(t, liveDir, newMarker)
}

// TestExportShardDirSurvivesPriorInterruptedSwap is the end-to-end guard: an
// interrupted swap (live absent, complete .export-* sibling, .export-bak-*
// sibling present) is healed by a SUBSEQUENT ExportShardDir call (which calls
// RecoverInterruptedExport on entry), and that call then proceeds to a clean,
// servable, manifest-consistent dir with no leftover markers.
func TestExportShardDirSurvivesPriorInterruptedSwap(t *testing.T) {
	requireGit(t)
	liveDir, casDir, newMarker, _ := buildBaselineAndRefreshedPlainExport(t)

	oldCopy := liveDir + ".oldsnapshot"
	if err := copyDir(t, liveDir, oldCopy); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportShardDir(casDir, liveDir, 1<<10); err != nil {
		t.Fatalf("first ExportShardDir (re-export): %v", err)
	}
	stamp := "20260101-000000.000000000"
	tmpSib := liveDir + exportTmpSuffix + stamp
	bakSib := liveDir + exportBakSuffix + stamp
	if err := os.Rename(liveDir, tmpSib); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldCopy, bakSib); err != nil {
		t.Fatal(err)
	}

	// A subsequent export must heal the interrupted swap on entry, then run
	// cleanly to a fresh, complete export (same content, since the CAS did not
	// change again in between).
	if _, err := ExportShardDir(casDir, liveDir, 1<<10); err != nil {
		t.Fatalf("recovery export: %v", err)
	}
	assertServesConsistently(t, liveDir, newMarker)
	assertNoPlainExportSwapMarkers(t, liveDir)
}
