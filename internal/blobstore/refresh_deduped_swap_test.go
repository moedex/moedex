package blobstore

// Crash-safe-swap recovery gate (SHOULD-FIX 2).
//
// The delta re-export swaps the live deduped dir into place with two renames
// (live -> .dedup-bak-*, .dedup-refresh-* -> live). A crash can land:
//   - WINDOW A: between the two renames, with the new dir already complete  -> roll FORWARD.
//   - WINDOW B: between the two renames, with the new dir NOT yet complete   -> roll BACK.
//   - WINDOW C: after the second rename, before the .bak cleanup            -> keep new, clean .bak.
// In every window, RecoverInterruptedDedupedSwap must leave the live path as EITHER
// the complete old dir OR the complete new dir — never absent/partial — with the
// manifest consistent with whatever dir is live. These tests reconstruct each
// crash-window on-disk state from real exports and assert recovery + servability.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/parity"
	"moedex/internal/server"
)

// buildBaselineAndStagedDelta builds a 3-repo corpus, a baseline deduped export at
// liveDir, mutates one repo + refreshes the CAS, and produces a COMPLETE staged
// "new" dir (what a delta would swap in) WITHOUT swapping it — by running a delta
// into a separate scratch dir we can position as the .dedup-refresh-* sibling. It
// returns liveDir, the staged-new dir, the bak path, and the marker for "new content".
func buildBaselineAndStagedDelta(t *testing.T) (liveDir, casDir, newMarker, oldGoneMarker string) {
	t.Helper()
	corpus := t.TempDir()
	repoA := filepath.Join(corpus, "repoA")
	repoB := filepath.Join(corpus, "repoB")
	repoC := filepath.Join(corpus, "repoC")
	commitGitRepo(t, repoA, map[string]string{"a.go": "package a\n" + largeBody("AlphaKeep", 40)})
	commitGitRepo(t, repoB, map[string]string{"b.go": "package b\nconst OldBravoMarker = 1\n" + largeBody("BravoBody", 40)})
	commitGitRepo(t, repoC, map[string]string{"c.go": "package c\n" + largeBody("CharlieKeep", 40)})

	casDir = t.TempDir()
	if _, err := BuildCAS(corpus, casDir); err != nil {
		t.Fatalf("BuildCAS: %v", err)
	}
	liveDir = filepath.Join(t.TempDir(), "served")
	if _, _, err := ExportDedupedShardDir(casDir, liveDir, 1<<10); err != nil {
		t.Fatalf("ExportDedupedShardDir baseline: %v", err)
	}

	// Mutate repoB: add a uniquely-findable new file, refresh CAS.
	newMarker = "uniquely_new_bravo_marker"
	writeFiles(t, repoB, map[string]string{"new.go": "package b\nfunc New() string { return \"" + newMarker + "\" }\n"})
	gitCommitAll(t, repoB, "add new.go")
	cm, err := LoadBlobManifest(filepath.Join(casDir, BlobManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := RefreshCAS(cm, corpus, casDir); err != nil {
		t.Fatalf("RefreshCAS: %v", err)
	}
	return liveDir, casDir, newMarker, "OldBravoMarker"
}

// assertServesConsistently opens dir through the serving spine (default-on content
// verify), checks every manifest shard path resolves under dir, and runs a literal.
func assertServesConsistently(t *testing.T, dir string, wantMarker string) {
	t.Helper()
	// Manifest must be present and its shard paths must all live under dir.
	m, err := parity.LoadManifest(filepath.Join(dir, parity.ManifestName))
	if err != nil {
		t.Fatalf("manifest not loadable under live dir %s: %v", dir, err)
	}
	if m.ShardDir != dir {
		t.Errorf("manifest ShardDir = %q, want live dir %q (manifest not consistent with live path)", m.ShardDir, dir)
	}
	for _, sm := range m.Shards {
		if filepath.Dir(sm.Path) != dir {
			t.Errorf("manifest shard path %q not under live dir %q", sm.Path, dir)
		}
		if _, err := os.Stat(sm.Path); err != nil {
			t.Errorf("manifest shard path %q does not exist: %v", sm.Path, err)
		}
	}
	// Serving spine opens it (content verify default-on => corruption would fail here).
	c, err := server.Open(dir)
	if err != nil {
		t.Fatalf("server.Open(%s) after recovery: %v", dir, err)
	}
	defer c.Close()
	ms, _, err := c.Literal(context.Background(), wantMarker)
	if err != nil {
		t.Fatalf("Literal(%q): %v", wantMarker, err)
	}
	if len(ms) == 0 {
		t.Errorf("recovered dir does not serve expected marker %q", wantMarker)
	}
}

// stageNewDirAsSibling runs a real delta into liveDir (which swaps it), then MOVES
// the now-live new dir into a .dedup-refresh-<stamp> sibling and the prior content
// into a .dedup-bak-<stamp> sibling, reproducing the exact on-disk state of a crash
// at the requested window. It returns the live base path (now absent or present per
// window) and the sibling paths.
//
// Rather than racing a real crash, we deterministically reconstruct each window's
// filesystem state from real, complete artifacts, then exercise the pure recovery
// function — the unit under test.
func TestRecoverInterruptedSwap_WindowA_RollForward(t *testing.T) {
	requireGit(t)
	liveDir, casDir, newMarker, _ := buildBaselineAndStagedDelta(t)

	// Produce a COMPLETE new dir by running a real delta (it swaps into liveDir), then
	// reposition it as the .dedup-refresh sibling and move the OLD dir to .dedup-bak,
	// and DELETE the live path — exactly the state of a crash BETWEEN the two renames
	// with a complete new dir staged.
	// First capture the OLD (pre-delta) dir by copying it aside.
	oldCopy := liveDir + ".oldsnapshot"
	if err := copyDir(t, liveDir, oldCopy); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RefreshDedupedShardDir(casDir, liveDir, 1<<10); err != nil {
		t.Fatalf("RefreshDedupedShardDir: %v", err)
	}
	// liveDir is now the COMPLETE new dir. Reproduce window A: move it to .refresh,
	// move the old snapshot to .bak, remove the live path.
	stamp := "20260101-000000.000000000"
	refreshSib := liveDir + dedupRefreshTmpSuffix + stamp
	bakSib := liveDir + dedupBakSuffix + stamp
	if err := os.Rename(liveDir, refreshSib); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldCopy, bakSib); err != nil {
		t.Fatal(err)
	}
	// live path now ABSENT, complete .refresh present, .bak present => roll FORWARD.

	if err := RecoverInterruptedDedupedSwap(liveDir); err != nil {
		t.Fatalf("RecoverInterruptedDedupedSwap: %v", err)
	}
	// Live dir restored to the NEW (complete) dir; markers cleaned.
	assertServesConsistently(t, liveDir, newMarker)
	assertNoSwapMarkers(t, liveDir)
}

func TestRecoverInterruptedSwap_WindowB_RollBack(t *testing.T) {
	requireGit(t)
	liveDir, casDir, newMarker, oldMarker := buildBaselineAndStagedDelta(t)

	// Reproduce window B faithfully: live ABSENT, .bak present (complete OLD dir),
	// .refresh present but INCOMPLETE — and crucially the incomplete .refresh is a
	// (manifest-stripped) copy of the NEW dir, so a buggy roll-FORWARD would surface
	// the new marker. Recovery must instead roll BACK to the complete old dir.
	stamp := "20260101-000000.000000000"
	bakSib := liveDir + dedupBakSuffix + stamp
	refreshSib := liveDir + dedupRefreshTmpSuffix + stamp

	// Snapshot the complete OLD dir to a NEUTRAL location (not a swap-marker sibling,
	// so the delta's own entry-recovery won't sweep it). Then run a real delta to
	// materialize a complete NEW dir.
	oldSnap := liveDir + ".oldsnap"
	if err := copyDir(t, liveDir, oldSnap); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RefreshDedupedShardDir(casDir, liveDir, 1<<10); err != nil {
		t.Fatalf("RefreshDedupedShardDir (to build a complete new dir): %v", err)
	}
	// liveDir is now the complete NEW dir. Stage the window-B state: .refresh = NEW
	// minus its manifest (incomplete), .bak = the OLD snapshot (complete), live gone.
	if err := copyDir(t, liveDir, refreshSib); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(refreshSib, parity.ManifestName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldSnap, bakSib); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(liveDir); err != nil {
		t.Fatal(err)
	}
	// live ABSENT, .bak complete (OLD), .refresh INCOMPLETE (NEW minus manifest)
	// => roll BACK to .bak.

	if err := RecoverInterruptedDedupedSwap(liveDir); err != nil {
		t.Fatalf("RecoverInterruptedDedupedSwap: %v", err)
	}
	// Live dir restored to the OLD (complete) dir; the old marker is findable.
	assertServesConsistently(t, liveDir, oldMarker)
	assertNoSwapMarkers(t, liveDir)
	// And it genuinely rolled BACK (not forward): the new marker must NOT be present,
	// because the new content lived only in the INCOMPLETE .refresh we discarded.
	assertDoesNotServe(t, liveDir, newMarker)
}

// assertDoesNotServe fails if dir serves any match for marker (the roll-back proof).
func assertDoesNotServe(t *testing.T, dir, marker string) {
	t.Helper()
	c, err := server.Open(dir)
	if err != nil {
		t.Fatalf("server.Open(%s): %v", dir, err)
	}
	defer c.Close()
	ms, _, err := c.Literal(context.Background(), marker)
	if err != nil {
		t.Fatalf("Literal(%q): %v", marker, err)
	}
	if len(ms) != 0 {
		t.Errorf("rolled-back dir unexpectedly serves %q (%d matches) — recovery did not actually roll back", marker, len(ms))
	}
}

func TestRecoverInterruptedSwap_WindowC_CleanupAfterSwap(t *testing.T) {
	requireGit(t)
	liveDir, casDir, newMarker, _ := buildBaselineAndStagedDelta(t)

	// A real delta leaves liveDir = the complete NEW dir (swap fully done). Reproduce
	// window C: a stray .bak sibling lingered (crash after the 2nd rename, before bak
	// cleanup). Recovery must keep the live new dir and just remove the stray .bak.
	if _, _, err := RefreshDedupedShardDir(casDir, liveDir, 1<<10); err != nil {
		t.Fatalf("RefreshDedupedShardDir: %v", err)
	}
	stamp := "20260101-000000.000000000"
	bakSib := liveDir + dedupBakSuffix + stamp
	if err := copyDir(t, liveDir, bakSib); err != nil { // a stray leftover bak
		t.Fatal(err)
	}

	if err := RecoverInterruptedDedupedSwap(liveDir); err != nil {
		t.Fatalf("RecoverInterruptedDedupedSwap: %v", err)
	}
	assertServesConsistently(t, liveDir, newMarker)
	assertNoSwapMarkers(t, liveDir)
}

// TestRefreshSurvivesPriorInterruptedSwap is the end-to-end guard: an interrupted
// swap (live absent, complete .refresh, .bak present) is healed by a SUBSEQUENT
// RefreshDedupedShardDir (which calls recovery on entry), and that refresh then
// proceeds to a clean, servable, manifest-consistent dir with NO leftover markers.
func TestRefreshSurvivesPriorInterruptedSwap(t *testing.T) {
	requireGit(t)
	liveDir, casDir, newMarker, _ := buildBaselineAndStagedDelta(t)

	// Stage a window-A interrupted state (as in WindowA above).
	oldCopy := liveDir + ".oldsnapshot"
	if err := copyDir(t, liveDir, oldCopy); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RefreshDedupedShardDir(casDir, liveDir, 1<<10); err != nil {
		t.Fatalf("first RefreshDedupedShardDir: %v", err)
	}
	stamp := "20260101-000000.000000000"
	refreshSib := liveDir + dedupRefreshTmpSuffix + stamp
	bakSib := liveDir + dedupBakSuffix + stamp
	if err := os.Rename(liveDir, refreshSib); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldCopy, bakSib); err != nil {
		t.Fatal(err)
	}

	// A subsequent refresh must heal the interrupted swap on entry, then run cleanly.
	if _, _, err := RefreshDedupedShardDir(casDir, liveDir, 1<<10); err != nil {
		t.Fatalf("recovery refresh: %v", err)
	}
	assertServesConsistently(t, liveDir, newMarker)
	assertNoSwapMarkers(t, liveDir)
}

// assertNoSwapMarkers fails if any .dedup-bak-* / .dedup-refresh-* sibling of dir
// survives recovery (debris must be cleaned).
func assertNoSwapMarkers(t *testing.T, dir string) {
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
		if hasPrefix(n, base+dedupBakSuffix) || hasPrefix(n, base+dedupRefreshTmpSuffix) {
			t.Errorf("leftover swap marker after recovery: %s", n)
		}
	}
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

// copyDir recursively copies src to dst (regular files + dirs only; the deduped dir
// is flat, so this is sufficient).
func copyDir(t *testing.T, src, dst string) error {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(t, s, d); err != nil {
				return err
			}
			continue
		}
		b, err := os.ReadFile(s)
		if err != nil {
			return err
		}
		if err := os.WriteFile(d, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

var _ = diskstore.ContentStoreName