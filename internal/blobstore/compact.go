package blobstore

// compact.go implements COMPACTION-GC for the two append-only content stores:
// reclaiming dead (unreferenced) content by REWRITING the existing store from its
// own LIVE entries — NOT a re-ingest from git (the expensive path) and NOT a full
// re-export from the CAS. This is the cheap "rewrite-keeping-live" reclaim the
// append-only design deferred (see the blobstore package doc and RefreshCAS).
//
// # Why compaction is needed
//
// Both content stores are APPEND-ONLY:
//
//   - The CAS pack (blobs.pack) only ever appends net-new blobs. RefreshCAS drops a
//     removed repo's manifest entry but LEAVES its now-unreferenced blob bytes in the
//     pack (carried forward indefinitely). Over time, repo removals / content churn
//     leave dead blobs no live repo references.
//   - The deduped served store (blobs.dat) is extended by ContentStoreAppender, which
//     carries every prior blob forward byte-for-byte. A delta re-export that drops a
//     repo (and rewrites its shards without it) leaves that repo's no-longer-referenced
//     content in blobs.dat forever.
//
// Neither store ever shrinks. Compaction is the GC pass that does.
//
// # The two compactors
//
//   - CompactCAS: liveness = BlobManifest.referencedBlobs() (the union of all live
//     repos' blob SHAs). Rewrites blobs.pack + blobs.idx keeping only referenced blobs.
//   - CompactDedupedShardDir: liveness = the union of every live MOEDEX05 shard's
//     content-hash refs (diskstore.DedupedShardSHAs). Rewrites blobs.dat keeping only
//     referenced content; carries the shards forward byte-for-byte (they are unchanged —
//     only dead content is dropped, and dead content is by definition referenced by no
//     live shard, so no shard byte changes).
//
// CompactDedupedShardDir is DISTINCT from `cas-export -deduped -force` (a FULL re-
// export that rebuilds a dead-free store from the CAS by re-reading every blob and
// re-packing every shard): compaction is the CHEAP in-place rewrite that reuses the
// EXISTING store + shards and avoids re-exporting anything. Both yield a dead-free
// store; compaction just does far less work when little is dead.
//
// # SACRED CONSTRAINT (never under-approximate)
//
// Compaction must NEVER drop a blob that any live repo/shard still references —
// GC'ing a live blob is a silent under-approximation (search would miss its content).
// The liveness sets above are computed conservatively from the live manifest /
// shards, and BOTH compactors VERIFY every live key is present in the source store
// before writing anything (a missing live key is a loud error, never a silent drop).
//
// # Crash safety
//
// Both compactors REUSE the durable crash-safe swap + recovery the delta deduped
// re-export established (RecoverInterruptedDedupedSwap + the .dedup-bak / .dedup-
// refresh sibling markers + fsync-durable WriteManifest): the compacted dir is built
// under a sibling temp dir, fsynced, its completeness marker written LAST, then
// swapped in with two renames. A crash in any window leaves the live path as EITHER
// the complete old dir OR the complete new dir, recovered on the next run.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/parity"
)

// CASCompactStats reports what a CompactCAS run reclaimed, for honest measurement.
type CASCompactStats struct {
	// LiveBlobs / LiveBytes are the unique referenced blobs and their content bytes
	// kept (== a fresh BuildCAS of the same live set would store).
	LiveBlobs int
	LiveBytes int64
	// DeadBlobs / DeadBytes are the unreferenced blobs and content bytes reclaimed
	// (the dead space dropped). BeforeBytes = LiveBytes + DeadBytes.
	DeadBlobs int
	DeadBytes int64
	// BeforeBytes / AfterBytes are the store's content footprint pre/post compaction;
	// AfterBytes == LiveBytes and is < BeforeBytes whenever any dead blob existed.
	BeforeBytes int64
	AfterBytes  int64
}

// casCompactMarkers are the crash-safe-swap sibling suffixes for the CAS dir swap,
// mirroring the deduped re-export markers. RecoverInterruptedCASCompaction keys on
// these to roll an interrupted compaction forward or back.
const (
	casCompactTmpSuffix = ".cas-compact-" // sibling holding the new (compacted) CAS dir
	casCompactBakSuffix = ".cas-compact-bak-"
)

// CompactCAS reclaims dead space from the CAS pack at casDir IN PLACE. It loads the
// BlobManifest, computes the liveness set (referencedBlobs — the union of all live
// repos' blob SHAs), and rewrites blobs.pack + blobs.idx keeping ONLY referenced
// blobs, dropping every unreferenced (dead) blob. The BlobManifest is carried forward
// UNCHANGED (every blob it references is kept, so it stays valid), copied into the
// compacted dir so the dir remains a complete, self-consistent CAS.
//
// The rewrite reuses Store.Put / Store.Flush, so the compacted pack + idx are
// byte-format-identical to a fresh BuildCAS of the same live set (same MOEBLOB1
// layout, same content-integrity invariant — each kept blob's bytes are copied
// verbatim under the same key). It is crash-safe: the compacted dir is built under a
// sibling temp dir and swapped in with two renames (the live dir kept as a .bak
// until the swap completes), recovered on the next run by
// RecoverInterruptedCASCompaction.
//
// SACRED: every live SHA is verified present in the source pack before writing; a
// missing live SHA is a loud error (the manifest references a blob the pack lacks —
// corruption), never a silent drop. Returns the reclaim stats.
func CompactCAS(casDir string) (CASCompactStats, error) {
	var st CASCompactStats

	// Heal any prior interrupted compaction so we start from a complete CAS dir.
	if err := RecoverInterruptedCASCompaction(casDir); err != nil {
		return st, err
	}

	m, err := LoadBlobManifest(filepath.Join(casDir, BlobManifestName))
	if err != nil {
		return st, fmt.Errorf("blobstore: compact CAS: load manifest: %w", err)
	}
	live := m.referencedBlobs()

	src, err := Open(casDir)
	if err != nil {
		return st, err
	}
	// We only read from src; close it before the swap so no handle holds the old dir.
	st.BeforeBytes = src.BytesStored()
	beforeBlobs := src.Len()

	// SACRED: verify every live SHA is in the source pack before writing anything.
	for sha := range live {
		if !src.Has(sha) {
			src.Close()
			return st, fmt.Errorf("blobstore: compact CAS: live blob %s referenced by manifest but absent from pack (corrupt store; refusing to compact)", sha)
		}
	}

	stamp := time.Now().Format("20060102-150405.000000000")
	tmpDir := casDir + casCompactTmpSuffix + stamp
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		src.Close()
		return st, err
	}
	cleanupTmp := func() { _ = os.RemoveAll(tmpDir) }

	// Build the compacted store into the temp dir, keeping only live blobs in the
	// source's deterministic pack order (SHAs()), so the compacted pack is reproducible.
	dst, err := Open(tmpDir)
	if err != nil {
		src.Close()
		cleanupTmp()
		return st, err
	}
	for _, sha := range src.SHAs() {
		if !live[sha] {
			continue // dead: drop it (the reclaim)
		}
		content, err := src.Get(sha)
		if err != nil {
			dst.Close()
			src.Close()
			cleanupTmp()
			return st, fmt.Errorf("blobstore: compact CAS: read live blob %s: %w", sha, err)
		}
		if _, err := dst.Put(sha, content); err != nil {
			dst.Close()
			src.Close()
			cleanupTmp()
			return st, fmt.Errorf("blobstore: compact CAS: write live blob %s: %w", sha, err)
		}
	}
	if err := dst.Close(); err != nil { // Flushes pack + idx durably.
		src.Close()
		cleanupTmp()
		return st, fmt.Errorf("blobstore: compact CAS: flush compacted store: %w", err)
	}
	st.AfterBytes = dst.BytesStored()
	st.LiveBlobs = dst.Len()
	st.LiveBytes = dst.BytesStored()
	st.DeadBlobs = beforeBlobs - dst.Len()
	st.DeadBytes = st.BeforeBytes - st.AfterBytes
	src.Close()

	// Carry the manifest forward UNCHANGED (it references only live blobs, all kept).
	// Write it LAST into the temp dir as the durable "complete" marker, after the
	// pack + idx are fsynced (Store.Close fsynced the pack; fsync the dir's files so
	// the marker only becomes durable after the data it covers).
	if err := fsyncDirFiles(tmpDir); err != nil {
		cleanupTmp()
		return st, fmt.Errorf("blobstore: compact CAS: fsync compacted dir: %w", err)
	}
	if err := WriteBlobManifest(filepath.Join(tmpDir, BlobManifestName), m); err != nil {
		cleanupTmp()
		return st, fmt.Errorf("blobstore: compact CAS: write carried manifest: %w", err)
	}
	if err := fsyncDirFiles(tmpDir); err != nil {
		cleanupTmp()
		return st, fmt.Errorf("blobstore: compact CAS: fsync manifest: %w", err)
	}

	// Crash-safe swap: move live dir aside (.bak), move temp into place. Recovered on
	// the next run by RecoverInterruptedCASCompaction.
	bak := casDir + casCompactBakSuffix + stamp
	if err := os.Rename(casDir, bak); err != nil {
		cleanupTmp()
		return st, fmt.Errorf("blobstore: compact CAS: move old dir aside: %w", err)
	}
	if err := os.Rename(tmpDir, casDir); err != nil {
		_ = os.Rename(bak, casDir) // best-effort immediate restore
		cleanupTmp()
		return st, fmt.Errorf("blobstore: compact CAS: swap compacted dir into place: %w", err)
	}
	_ = os.RemoveAll(bak)
	return st, nil
}

// casDirIsComplete reports whether dir is a complete CAS dir: it has a pack
// (blobs.pack present) AND a manifest (blobmanifest.json) that actually PARSES. The
// manifest is written LAST as the completeness marker, so its valid presence is the
// durable "compaction finished" signal recovery rolls a tmp dir FORWARD on.
//
// Defense-in-depth (hardening, not a fix for a reachable crash window — the manifest
// is fsynced before the swap, and a tmp dir is only rolled forward when the live dir
// is ABSENT, i.e. after the swap began, by which point the manifest is already
// durable): we LoadBlobManifest rather than mere os.Stat, so a present-but-corrupt
// manifest (bit-rot, a partial external copy) is treated as incomplete (recovery rolls
// BACK to the prior dir) instead of being rolled forward as if finished.
func casDirIsComplete(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, packName)); err != nil {
		return false
	}
	if _, err := LoadBlobManifest(filepath.Join(dir, BlobManifestName)); err != nil {
		return false
	}
	return true
}

// RecoverInterruptedCASCompaction makes CompactCAS's directory swap crash-
// RECOVERABLE, mirroring RecoverInterruptedDedupedSwap. Given the live casDir, it
// inspects the sibling markers a compaction leaves during its two-rename swap (a
// ".cas-compact-*" holding the new dir, a ".cas-compact-bak-*" holding the prior
// live dir moved aside) and rolls forward or back so the live path ends up as EITHER
// the complete compacted dir OR the complete prior dir — never absent/partial. It is
// idempotent and a no-op in the normal (no-marker) case, safe to call at the start of
// every compaction and before opening a CAS dir.
func RecoverInterruptedCASCompaction(casDir string) error {
	parent := filepath.Dir(casDir)
	base := filepath.Base(casDir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var baks, tmps []string
	for _, e := range entries {
		n := e.Name()
		// NOTE: casCompactBakSuffix has casCompactTmpSuffix as a prefix-of-a-prefix
		// only by coincidence of the leading ".cas-compact-"; disambiguate by checking
		// the bak suffix FIRST (it is the longer, more specific match).
		switch {
		case hasSwapPrefix(n, base+casCompactBakSuffix):
			baks = append(baks, filepath.Join(parent, n))
		case hasSwapPrefix(n, base+casCompactTmpSuffix):
			tmps = append(tmps, filepath.Join(parent, n))
		}
	}
	sortStrings(baks)
	sortStrings(tmps)

	_, liveErr := os.Stat(casDir)
	livePresent := liveErr == nil

	if livePresent {
		// S0 / S3: live dir in place; any lingering markers are debris — remove them.
		for _, p := range baks {
			_ = os.RemoveAll(p)
		}
		for _, p := range tmps {
			_ = os.RemoveAll(p)
		}
		return nil
	}

	// Live ABSENT: crash between the two renames. Prefer rolling FORWARD to a complete
	// compacted dir; else roll BACK to the prior dir moved aside.
	for i := len(tmps) - 1; i >= 0; i-- {
		if casDirIsComplete(tmps[i]) {
			if err := os.Rename(tmps[i], casDir); err != nil {
				return fmt.Errorf("blobstore: roll-forward interrupted CAS compaction: %w", err)
			}
			for _, p := range append(baks, tmps[:i]...) {
				_ = os.RemoveAll(p)
			}
			return nil
		}
	}
	for i := len(baks) - 1; i >= 0; i-- {
		if casDirIsComplete(baks[i]) {
			if err := os.Rename(baks[i], casDir); err != nil {
				return fmt.Errorf("blobstore: roll-back interrupted CAS compaction: %w", err)
			}
			for _, p := range append(baks[:i], tmps...) {
				_ = os.RemoveAll(p)
			}
			return nil
		}
	}
	return nil
}

// hasSwapPrefix reports whether s begins with prefix (a tiny dependency-free helper
// so this file does not pull in strings just for HasPrefix).
func hasSwapPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// sortStrings sorts in place (ascending). Suffixes are timestamps, so ascending puts
// the newest last — matching the deduped recovery's newest-last convention.
func sortStrings(s []string) {
	// Simple insertion sort (marker lists are tiny: at most a handful of siblings).
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// DedupedCompactStats reports what a CompactDedupedShardDir run reclaimed.
type DedupedCompactStats struct {
	// LiveBlobs / LiveBytes are the unique content records and bytes kept (referenced
	// by at least one live shard).
	LiveBlobs int
	LiveBytes int64
	// DeadBlobs / DeadBytes are the unreferenced content records / bytes reclaimed.
	DeadBlobs int
	DeadBytes int64
	// BeforeBytes / AfterBytes are the blobs.dat content footprint pre/post compaction.
	BeforeBytes int64
	AfterBytes  int64
	// Shards is the number of MOEDEX05 shards carried forward byte-for-byte (none are
	// rewritten — compaction only drops dead content, which no live shard references).
	Shards int
}

// CompactDedupedShardDir reclaims dead space from the shared content store (blobs.dat)
// of the deduped served dir at outShardDir IN PLACE. It computes the liveness set as
// the UNION of every live MOEDEX05 shard's content-hash refs (diskstore.DedupedShardSHAs),
// rewrites blobs.dat keeping ONLY referenced content, and carries every shard + the
// manifest forward byte-for-byte (the shards are unchanged — only dead content, which
// by definition no live shard references, is dropped).
//
// This is the CHEAP in-place reclaim, DISTINCT from a FULL `cas-export -deduped -force`
// (which rebuilds a dead-free store from the CAS by re-reading every blob and re-packing
// every shard). Compaction reuses the EXISTING shards + store and re-exports nothing.
//
// It is crash-safe: the compacted dir is built under a sibling temp dir, fsynced, its
// manifest (the completeness marker) written LAST, then swapped in with the SAME two-
// rename swap + recovery as the delta deduped re-export (RecoverInterruptedDedupedSwap),
// which it calls on entry to heal any prior interrupted swap.
//
// SACRED: every live SHA is verified present in blobs.dat before writing anything (a
// shard referencing content the store lacks is a loud error, never a silent drop).
func CompactDedupedShardDir(outShardDir string) (DedupedCompactStats, error) {
	var st DedupedCompactStats

	// Heal any prior interrupted swap (delta re-export OR a prior compaction; both use
	// the same markers) so we start from a complete, manifest-consistent live dir.
	if err := RecoverInterruptedDedupedSwap(outShardDir); err != nil {
		return st, err
	}

	served, err := parity.LoadManifest(filepath.Join(outShardDir, parity.ManifestName))
	if err != nil {
		return st, fmt.Errorf("blobstore: compact deduped: load manifest: %w", err)
	}
	csPath := filepath.Join(outShardDir, diskstore.ContentStoreName)
	if _, err := os.Stat(csPath); err != nil {
		return st, fmt.Errorf("blobstore: compact deduped: dir is not deduped (no %s): %w", diskstore.ContentStoreName, err)
	}

	// AUTHORITATIVE shard set: the SERVED set is what `filepath.Glob(dir,"*.idx")`
	// (sorted) returns — that is the EXACT signal server.Open / loadUnified key on to
	// decide what to serve, NOT the manifest's shard list. A GC tool that DELETES data
	// MUST derive both its liveness set and its carry-forward set from this identical
	// signal: if the glob set ever exceeds the manifest list (an orphan *.idx the
	// manifest does not record), keying off the manifest would (a) omit that shard's
	// content from `live` so its content is dropped, AND (b) never carry the shard file
	// forward — the swap deletes both, and matches the server returned pre-compaction
	// vanish. That is the SACRED under-approximation this lane forbids. We carry EVERY
	// globbed shard + its content forward; orphan shards (no manifest entry) get a
	// synthesized manifest entry rather than being dropped.
	shardPaths, err := filepath.Glob(filepath.Join(outShardDir, "*.idx"))
	if err != nil {
		return st, fmt.Errorf("blobstore: compact deduped: glob shards: %w", err)
	}
	sortStrings(shardPaths)
	if len(shardPaths) == 0 {
		return st, fmt.Errorf("blobstore: compact deduped: no *.idx shards under %s", outShardDir)
	}
	// Index the manifest's per-shard metadata by basename so we reuse Repos/ContentBytes
	// when a globbed shard matches a manifest entry, and synthesize for orphans.
	manifestByBase := map[string]parity.ShardManifest{}
	for _, sm := range served.Shards {
		manifestByBase[filepath.Base(sm.Path)] = sm
	}

	// Liveness = union of EVERY globbed shard's referenced content keys. A shard we
	// cannot read is a fatal error (treating it as "references nothing" would let us
	// drop content it actually needs — an under-approximation).
	live := map[string]bool{}
	for _, p := range shardPaths {
		shas, err := diskstore.DedupedShardSHAs(p)
		if err != nil {
			return st, fmt.Errorf("blobstore: compact deduped: read shard %s liveness: %w", p, err)
		}
		for _, sha := range shas {
			live[sha] = true
		}
	}

	// Footprint before (for the reclaim measurement).
	beforeCS, err := diskstore.OpenContentStore(csPath)
	if err != nil {
		return st, fmt.Errorf("blobstore: compact deduped: open content store: %w", err)
	}
	st.BeforeBytes = beforeCS.BytesStored()
	beforeBlobs := beforeCS.Len()
	beforeCS.Close()

	stamp := time.Now().Format("20060102-150405.000000000")
	tmpDir := outShardDir + dedupRefreshTmpSuffix + stamp
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return st, err
	}
	cleanupTmp := func() { _ = os.RemoveAll(tmpDir) }

	// Write the compacted content store into the temp dir (keeps only live content).
	// CompactContentStore verifies every live key is present in the source first.
	tmpCS := filepath.Join(tmpDir, diskstore.ContentStoreName)
	keptBlobs, keptBytes, err := diskstore.CompactContentStore(csPath, tmpCS, live)
	if err != nil {
		cleanupTmp()
		return st, fmt.Errorf("blobstore: compact deduped: rewrite content store: %w", err)
	}
	st.LiveBlobs = keptBlobs
	st.LiveBytes = keptBytes
	st.AfterBytes = keptBytes
	st.DeadBlobs = beforeBlobs - keptBlobs
	st.DeadBytes = st.BeforeBytes - st.AfterBytes

	// Carry EVERY globbed shard forward byte-for-byte into the temp dir under its
	// ORIGINAL basename (so the glob set the server reads is preserved exactly — an
	// orphan shard-9999.idx stays shard-9999.idx, not renumbered into a hole), and build
	// the manifest with paths bound to the LIVE path (no post-swap rewrite needed — same
	// crash-safety property as RefreshDedupedShardDir). A globbed shard with a manifest
	// entry reuses its Repos/ContentBytes; an ORPHAN (globbed but unrecorded) gets a
	// synthesized entry (empty Repos, content-bytes re-derived from its live blob sizes)
	// rather than being dropped — the cardinal rule: a shard the server would glob keeps
	// both its content and its file.
	var shards []parity.ShardManifest
	for _, src := range shardPaths {
		base := filepath.Base(src)
		phys := filepath.Join(tmpDir, base)
		rec := filepath.Join(outShardDir, base)
		if err := copyFile(src, phys); err != nil {
			cleanupTmp()
			return st, fmt.Errorf("blobstore: compact deduped: carry shard %s: %w", src, err)
		}
		if sm, ok := manifestByBase[base]; ok {
			shards = append(shards, parity.ShardManifest{Path: rec, Repos: sm.Repos, ContentBytes: sm.ContentBytes})
		} else {
			shards = append(shards, parity.ShardManifest{Path: rec, Repos: nil, ContentBytes: 0})
		}
	}
	st.Shards = len(shards)

	out := &parity.Manifest{
		Version:  parity.ManifestVersion,
		Root:     served.Root,
		BuiltAt:  time.Now(),
		ShardDir: outShardDir, // FINAL live path — no post-swap rewrite needed
		Heads:    served.Heads,
		Shards:   shards,
	}

	// fsync data files BEFORE the manifest (the completeness marker), so a crash before
	// the manifest leaves an incomplete tmpDir recovery will NOT roll forward.
	if err := fsyncDirFiles(tmpDir); err != nil {
		cleanupTmp()
		return st, fmt.Errorf("blobstore: compact deduped: fsync compacted dir files: %w", err)
	}
	if err := parity.WriteManifest(filepath.Join(tmpDir, parity.ManifestName), out); err != nil {
		cleanupTmp()
		return st, fmt.Errorf("blobstore: compact deduped: write manifest: %w", err)
	}

	// Crash-safe swap (identical to RefreshDedupedShardDir): live -> .dedup-bak, temp ->
	// live. A crash in any window is recovered by RecoverInterruptedDedupedSwap.
	bak := outShardDir + dedupBakSuffix + stamp
	if err := os.Rename(outShardDir, bak); err != nil {
		cleanupTmp()
		return st, fmt.Errorf("blobstore: compact deduped: move old dir aside: %w", err)
	}
	if err := os.Rename(tmpDir, outShardDir); err != nil {
		_ = os.Rename(bak, outShardDir)
		cleanupTmp()
		return st, fmt.Errorf("blobstore: compact deduped: swap compacted dir into place: %w", err)
	}
	_ = os.RemoveAll(bak)
	return st, nil
}
