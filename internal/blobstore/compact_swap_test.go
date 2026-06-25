package blobstore

// Compaction-GC CRASH-SAFETY gate (PROOF 3 of the compaction lane).
//
// Both compactors swap the live dir into place with two renames (live -> .bak,
// temp -> live). A crash can land:
//   - WINDOW A: between the renames, new dir already complete   -> roll FORWARD.
//   - WINDOW B: between the renames, new dir NOT yet complete    -> roll BACK.
//   - WINDOW C: after the second rename, before .bak cleanup     -> keep new, clean .bak.
// In every window, recovery must leave the live path as EITHER the complete old dir
// OR the complete new dir — never absent/partial — with its completeness marker
// (manifest) consistent with whatever dir is live. These tests reconstruct each
// on-disk crash state from real, complete artifacts and exercise the pure recovery
// functions (RecoverInterruptedCASCompaction for the CAS dir;
// RecoverInterruptedDedupedSwap for the deduped served dir, which the deduped
// compactor reuses).

import (
	"os"
	"path/filepath"
	"testing"
)

// --- CAS pack compaction swap recovery --------------------------------------------

// buildCompactedCASBaseline builds a corpus with dead blobs, returns the casDir
// (already containing dead content, NOT yet compacted) and a function that runs a real
// CompactCAS into a SEPARATE scratch dir, returning the complete compacted dir path.
func buildCompactedCASBaseline(t *testing.T) (casDir string) {
	t.Helper()
	_, casDir, _, _, _, _, _ = buildDeadBlobCorpus(t)
	return casDir
}

func TestRecoverInterruptedCASCompaction_WindowA_RollForward(t *testing.T) {
	requireGit(t)
	casDir := buildCompactedCASBaseline(t)

	// Snapshot the pre-compaction (OLD) dir aside, then run a real compaction (it swaps
	// the complete NEW dir into casDir). Reproduce window A: live absent, complete
	// .cas-compact-* present, .cas-compact-bak-* present => roll FORWARD.
	oldCopy := casDir + ".oldsnapshot"
	if err := copyDirTest(t, casDir, oldCopy); err != nil {
		t.Fatal(err)
	}
	if _, err := CompactCAS(casDir); err != nil {
		t.Fatalf("CompactCAS: %v", err)
	}
	stamp := "20260101-000000.000000000"
	tmpSib := casDir + casCompactTmpSuffix + stamp
	bakSib := casDir + casCompactBakSuffix + stamp
	if err := os.Rename(casDir, tmpSib); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldCopy, bakSib); err != nil {
		t.Fatal(err)
	}

	if err := RecoverInterruptedCASCompaction(casDir); err != nil {
		t.Fatalf("RecoverInterruptedCASCompaction: %v", err)
	}
	assertCASComplete(t, casDir)
	assertNoCASMarkers(t, casDir)
	// The live dir is the COMPACTED (new) one: it opens and is dead-free (its blob count
	// equals its manifest's referenced set).
	assertCASDeadFree(t, casDir)
}

func TestRecoverInterruptedCASCompaction_WindowB_RollBack(t *testing.T) {
	requireGit(t)
	casDir := buildCompactedCASBaseline(t)

	// Record the OLD store's blob count (it still has the dead blobs).
	oldStore, err := Open(casDir)
	if err != nil {
		t.Fatal(err)
	}
	oldBlobs := oldStore.Len()
	oldStore.Close()

	stamp := "20260101-000000.000000000"
	tmpSib := casDir + casCompactTmpSuffix + stamp
	bakSib := casDir + casCompactBakSuffix + stamp

	// Snapshot the complete OLD dir to the .bak sibling. Run a real compaction to make a
	// complete NEW dir, then stage it into the tmp sibling MINUS its manifest (incomplete),
	// and remove the live path => roll BACK to .bak.
	oldSnap := casDir + ".oldsnap"
	if err := copyDirTest(t, casDir, oldSnap); err != nil {
		t.Fatal(err)
	}
	if _, err := CompactCAS(casDir); err != nil {
		t.Fatalf("CompactCAS: %v", err)
	}
	if err := copyDirTest(t, casDir, tmpSib); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(tmpSib, BlobManifestName)); err != nil {
		t.Fatal(err) // make the tmp INCOMPLETE
	}
	if err := os.Rename(oldSnap, bakSib); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(casDir); err != nil {
		t.Fatal(err)
	}

	if err := RecoverInterruptedCASCompaction(casDir); err != nil {
		t.Fatalf("RecoverInterruptedCASCompaction: %v", err)
	}
	assertCASComplete(t, casDir)
	assertNoCASMarkers(t, casDir)
	// It rolled BACK to the OLD (uncompacted) dir: the dead blobs are still present.
	post, err := Open(casDir)
	if err != nil {
		t.Fatal(err)
	}
	defer post.Close()
	if post.Len() != oldBlobs {
		t.Errorf("rolled-back CAS has %d blobs, want the OLD %d (recovery did not roll back)", post.Len(), oldBlobs)
	}
}

func TestRecoverInterruptedCASCompaction_WindowC_Cleanup(t *testing.T) {
	requireGit(t)
	casDir := buildCompactedCASBaseline(t)

	// A real compaction leaves casDir = the complete NEW dir (swap done). Reproduce
	// window C: a stray .bak lingered. Recovery keeps the live new dir, removes the .bak.
	if _, err := CompactCAS(casDir); err != nil {
		t.Fatalf("CompactCAS: %v", err)
	}
	stamp := "20260101-000000.000000000"
	bakSib := casDir + casCompactBakSuffix + stamp
	if err := copyDirTest(t, casDir, bakSib); err != nil {
		t.Fatal(err)
	}

	if err := RecoverInterruptedCASCompaction(casDir); err != nil {
		t.Fatalf("RecoverInterruptedCASCompaction: %v", err)
	}
	assertCASComplete(t, casDir)
	assertNoCASMarkers(t, casDir)
	assertCASDeadFree(t, casDir)
}

// TestCompactCASSurvivesPriorInterruptedSwap is the end-to-end guard: a window-A
// interrupted compaction is healed by a SUBSEQUENT CompactCAS (which calls recovery
// on entry), and that compaction then proceeds cleanly.
func TestCompactCASSurvivesPriorInterruptedSwap(t *testing.T) {
	requireGit(t)
	casDir := buildCompactedCASBaseline(t)

	oldCopy := casDir + ".oldsnapshot"
	if err := copyDirTest(t, casDir, oldCopy); err != nil {
		t.Fatal(err)
	}
	if _, err := CompactCAS(casDir); err != nil {
		t.Fatalf("first CompactCAS: %v", err)
	}
	stamp := "20260101-000000.000000000"
	tmpSib := casDir + casCompactTmpSuffix + stamp
	bakSib := casDir + casCompactBakSuffix + stamp
	if err := os.Rename(casDir, tmpSib); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldCopy, bakSib); err != nil {
		t.Fatal(err)
	}

	// A subsequent compaction heals the interrupted swap on entry, then runs cleanly.
	if _, err := CompactCAS(casDir); err != nil {
		t.Fatalf("recovery CompactCAS: %v", err)
	}
	assertCASComplete(t, casDir)
	assertNoCASMarkers(t, casDir)
	assertCASDeadFree(t, casDir)
}

// --- Deduped served-store compaction swap recovery (reuses the deduped swap) -------

// TestCompactDedupedSurvivesPriorInterruptedSwap proves the deduped compactor's swap
// is recoverable: stage a window-A interrupted state (live absent, complete .dedup-
// refresh, .dedup-bak present), then a SUBSEQUENT CompactDedupedShardDir heals it on
// entry (it calls RecoverInterruptedDedupedSwap) and proceeds to a clean dir.
func TestCompactDedupedSurvivesPriorInterruptedSwap(t *testing.T) {
	requireGit(t)
	_, _, dedupDir, _, deadContentMarker, liveNewMarker, _ := buildDeadBlobCorpus(t)

	// Snapshot the pre-compaction dir, run a real compaction (swaps complete new dir in),
	// then reposition: new -> .dedup-refresh, old -> .dedup-bak, live removed.
	oldCopy := dedupDir + ".oldsnapshot"
	if err := copyDirTest(t, dedupDir, oldCopy); err != nil {
		t.Fatal(err)
	}
	if _, err := CompactDedupedShardDir(dedupDir); err != nil {
		t.Fatalf("first CompactDedupedShardDir: %v", err)
	}
	stamp := "20260101-000000.000000000"
	refreshSib := dedupDir + dedupRefreshTmpSuffix + stamp
	bakSib := dedupDir + dedupBakSuffix + stamp
	if err := os.Rename(dedupDir, refreshSib); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldCopy, bakSib); err != nil {
		t.Fatal(err)
	}

	// A subsequent compaction heals on entry, then runs cleanly. (The recovery roll-
	// forward delivers the already-compacted dir; the second compaction is then a no-op
	// reclaim over an already-dead-free store, which is still correct.)
	if _, err := CompactDedupedShardDir(dedupDir); err != nil {
		t.Fatalf("recovery CompactDedupedShardDir: %v", err)
	}
	assertNoDedupMarkers(t, dedupDir)
	// Live content still served; dead content gone — the recovery delivered the
	// compacted (dead-free) dir.
	assertServesConsistently(t, dedupDir, liveNewMarker)
	assertDoesNotServe(t, dedupDir, deadContentMarker)
}

// --- helpers ----------------------------------------------------------------------

// assertCASComplete fails unless dir is a complete CAS dir (pack + manifest present
// and loadable, and the store opens).
func assertCASComplete(t *testing.T, dir string) {
	t.Helper()
	if !casDirIsComplete(dir) {
		t.Fatalf("CAS dir %s is not complete after recovery", dir)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open recovered CAS dir %s: %v", dir, err)
	}
	s.Close()
}

// assertCASDeadFree fails unless the CAS at dir holds EXACTLY its manifest's
// referenced (live) blob set — i.e. compaction reclaimed every dead blob.
func assertCASDeadFree(t *testing.T, dir string) {
	t.Helper()
	m, err := LoadBlobManifest(filepath.Join(dir, BlobManifestName))
	if err != nil {
		t.Fatal(err)
	}
	live := m.referencedBlobs()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Len() != len(live) {
		t.Errorf("compacted CAS holds %d blobs, want exactly the %d referenced (not dead-free)", s.Len(), len(live))
	}
	for sha := range live {
		if !s.Has(sha) {
			t.Errorf("live blob %s missing from recovered CAS", sha)
		}
	}
}

// assertNoCASMarkers fails if any .cas-compact-* / .cas-compact-bak-* sibling survives.
func assertNoCASMarkers(t *testing.T, dir string) {
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
		if hasSwapPrefix(n, base+casCompactTmpSuffix) || hasSwapPrefix(n, base+casCompactBakSuffix) {
			t.Errorf("leftover CAS compaction marker after recovery: %s", n)
		}
	}
}

// assertNoDedupMarkers fails if any .dedup-bak-* / .dedup-refresh-* sibling survives.
func assertNoDedupMarkers(t *testing.T, dir string) {
	t.Helper()
	assertNoSwapMarkers(t, dir) // reuse the deduped swap test's checker
}

// copyDirTest recursively copies src to dst (flat dirs are sufficient for both stores).
func copyDirTest(t *testing.T, src, dst string) error {
	t.Helper()
	return copyDir(t, src, dst) // reuse refresh_deduped_swap_test.go's copyDir
}
