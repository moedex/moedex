package blobstore

// ExportShardDir is the parity-preserving bridge: it materializes a
// today-compatible servable *.idx shard dir + parity.Manifest FROM the CAS, so
// the existing daemon (moedex-serve), server.Open/OpenRank, and the full-corpus
// parity harness consume it with ZERO changes. This is what keeps ripgrep parity
// and the warm serving spine 100% working while the new storage layer (global
// dedup + per-blob delta) is validated independently.
//
// SLICE-1 BOUNDARY (documented, accepted): the CAS itself stores each blob ONCE
// (full global cross-shard dedup), but the EXPORTED shard dir still inlines a
// blob's content into every shard that holds a repo carrying it — exactly as a
// direct parity build does today — because the export streams blobs back through
// the unchanged index.AddFile / diskstore.Save pipeline (which dedups only within
// a single shard). The retrieval path's switch to reference CAS blobs by SHA (so
// served shards also dedup) is the explicit NEXT slice. The dedup + delta win is
// fully realized at the storage layer; the export is a migration seam, not the
// final served format.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/parity"
)

// Crash-safe-swap markers for the plain (inlined MOEDEX03/04) export.
// ExportShardDir builds the new dir under a sibling temp dir and swaps it into
// place with two renames (the prior live dir, if any, kept as a .bak until the
// swap completes), mirroring the deduped export's
// RefreshDedupedShardDir/CompactCAS/CompactDedupedShardDir pattern — so a crash
// mid-export (e.g. `moedex-index cas-export -force` killed after the caller
// cleared an existing target dir but before the export finished) never leaves
// the live shard dir in a state that is neither the complete old export nor the
// complete new one (CODEBASE-REVIEW.md F-13).
const (
	exportTmpSuffix = ".export-"     // sibling holding the new dir being built
	exportBakSuffix = ".export-bak-" // sibling holding the prior live dir moved aside (if any)
)

// exportDirIsComplete reports whether dir is a fully-written plain export: its
// parity manifest is present and actually parses. Unlike the deduped export, a
// plain export inlines content into its shards, so there is no separate shared
// content store to also check — the manifest, written LAST, is the sole
// completeness marker (mirroring casDirIsComplete / dirIsCompleteDedupedExport).
func exportDirIsComplete(dir string) bool {
	_, err := parity.LoadManifest(filepath.Join(dir, parity.ManifestName))
	return err == nil
}

// RecoverInterruptedExport makes ExportShardDir's directory swap crash-
// RECOVERABLE, mirroring RecoverInterruptedDedupedSwap / RecoverInterruptedCASCompaction:
// given the live path outShardDir, it inspects the sibling markers a swap leaves
// (a ".export-*" holding the new dir, a ".export-bak-*" holding the prior live
// dir moved aside) and rolls forward or back so the live path ends up as EITHER
// the complete new dir OR the complete old dir — never absent or partial. It is
// idempotent and a no-op in the normal (no-marker) case; ExportShardDir calls it
// on entry, so a PRIOR interrupted export self-heals before a fresh one begins.
func RecoverInterruptedExport(outShardDir string) error {
	parent := filepath.Dir(outShardDir)
	base := filepath.Base(outShardDir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to recover (parent gone)
		}
		return err
	}
	var baks, tmps []string
	for _, e := range entries {
		n := e.Name()
		// exportBakSuffix has exportTmpSuffix as a prefix-of-a-prefix (both start
		// with ".export-"); check the longer, more specific bak suffix FIRST,
		// mirroring RecoverInterruptedCASCompaction's hasSwapPrefix disambiguation.
		switch {
		case hasSwapPrefix(n, base+exportBakSuffix):
			baks = append(baks, filepath.Join(parent, n))
		case hasSwapPrefix(n, base+exportTmpSuffix):
			tmps = append(tmps, filepath.Join(parent, n))
		}
	}
	sortStrings(baks)
	sortStrings(tmps)

	_, liveErr := os.Stat(outShardDir)
	livePresent := liveErr == nil

	if livePresent {
		// Live dir in place; any lingering bak/tmp is debris from a completed-but-
		// not-cleaned swap or an aborted pre-swap build — remove them.
		for _, p := range baks {
			_ = os.RemoveAll(p)
		}
		for _, p := range tmps {
			_ = os.RemoveAll(p)
		}
		return nil
	}

	// Live ABSENT: a crash landed between the two renames (or before the target
	// dir ever existed). Prefer rolling FORWARD to a complete new dir; else roll
	// BACK to the prior dir moved aside, if any.
	for i := len(tmps) - 1; i >= 0; i-- { // newest complete tmp first
		if exportDirIsComplete(tmps[i]) {
			if err := os.Rename(tmps[i], outShardDir); err != nil {
				return fmt.Errorf("blobstore: roll-forward interrupted export: %w", err)
			}
			for _, p := range append(baks, tmps[:i]...) {
				_ = os.RemoveAll(p)
			}
			return nil
		}
	}
	for i := len(baks) - 1; i >= 0; i-- {
		if exportDirIsComplete(baks[i]) {
			if err := os.Rename(baks[i], outShardDir); err != nil {
				return fmt.Errorf("blobstore: roll-back interrupted export: %w", err)
			}
			for _, p := range append(baks[:i], tmps...) {
				_ = os.RemoveAll(p)
			}
			return nil
		}
	}
	// Neither a complete tmp nor a complete bak exists: nothing safe to restore.
	// Leave the debris for an operator rather than guess.
	return nil
}

// ExportShardDir reads the CAS + BlobManifest at casDir and writes a servable
// shard dir to outShardDir: byte-bucketed *.idx shards (flushed at shardBytes of
// accumulated content, checked at repo boundaries so a repo is never split) plus
// a parity.Manifest. It replays each repo's recorded file entries (sha, rel) in
// manifest repo order through index.AddFile — the SAME (sha, content, repo, rel,
// abs) tuples a direct parity build would index — so within-shard dedup, content
// bytes, and FileRefs are byte-equivalent to a direct build. Pass shardBytes<=0
// for parity.DefaultShardBytes.
//
// The export is deterministic: repos are processed in manifest order (which is
// DiscoverRepos' sorted order at build/refresh time), and a repo's files in
// recorded order, so shard contents are reproducible across exports of the same
// manifest. Shard IDs remain opaque per-export (same caveat as parity.Manifest).
//
// CRASH SAFETY: the export is staged under a sibling temp dir and swapped into
// outShardDir atomically on success (see RecoverInterruptedExport above), so a
// failure or crash mid-export — whether from a bad manifest/CAS or the process
// being killed — never touches an existing outShardDir until the new export is
// fully built and durable. A prior interrupted export is healed on entry.
func ExportShardDir(casDir, outShardDir string, shardBytes int64) (*parity.Manifest, error) {
	if err := RecoverInterruptedExport(outShardDir); err != nil {
		return nil, err
	}

	m, err := LoadBlobManifest(filepath.Join(casDir, BlobManifestName))
	if err != nil {
		return nil, fmt.Errorf("blobstore: load blob manifest: %w", err)
	}
	store, err := Open(casDir)
	if err != nil {
		return nil, err
	}
	defer store.Close()

	stamp := time.Now().Format("20060102-150405.000000000")
	tmpDir := outShardDir + exportTmpSuffix + stamp
	cleanupTmp := func() { _ = os.RemoveAll(tmpDir) }

	out, err := exportShards(m, store, tmpDir, outShardDir, shardBytes, diskstore.Save)
	if err != nil {
		cleanupTmp()
		return nil, err
	}

	// fsync the staged shard files BEFORE writing the manifest (the completeness
	// marker), so a crash before the manifest leaves an incomplete tmpDir that
	// RecoverInterruptedExport will NOT roll forward (it rolls back instead).
	if err := fsyncDirFiles(tmpDir); err != nil {
		cleanupTmp()
		return nil, fmt.Errorf("blobstore: fsync exported dir files: %w", err)
	}
	if err := parity.WriteManifest(filepath.Join(tmpDir, parity.ManifestName), out); err != nil {
		cleanupTmp()
		return nil, fmt.Errorf("blobstore: write exported manifest: %w", err)
	}

	// Crash-safe swap: move any existing live dir aside (kept as .bak), move the
	// staged dir into place. A crash between the renames is recovered on the NEXT
	// run by RecoverInterruptedExport.
	var bak string
	if _, err := os.Stat(outShardDir); err == nil {
		bak = outShardDir + exportBakSuffix + stamp
		if err := os.Rename(outShardDir, bak); err != nil {
			cleanupTmp()
			return nil, fmt.Errorf("blobstore: move old exported dir aside: %w", err)
		}
	}
	if err := os.Rename(tmpDir, outShardDir); err != nil {
		if bak != "" {
			_ = os.Rename(bak, outShardDir) // best-effort immediate restore
		}
		cleanupTmp()
		return nil, fmt.Errorf("blobstore: swap exported dir into place: %w", err)
	}
	if bak != "" {
		_ = os.RemoveAll(bak)
	}
	return out, nil
}
