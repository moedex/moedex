package blobstore

// RefreshDedupedShardDir is the DELTA-AWARE deduped re-export: the incremental
// counterpart to ExportDedupedShardDir. Given an EXISTING deduped served dir
// (content-less MOEDEX05 shards + one shared MOECONT1 blobs.dat) and the current
// CAS, it re-exports only what changed instead of rebuilding every shard and the
// whole content store from scratch:
//
//   - APPEND only net-new blob content to the existing blobs.dat. The content store
//     is content-addressed, so a blob whose content hash is already present is a
//     dedup no-op (mirroring blobstore.Store.Put / diskstore.ContentStoreWriter):
//     co-resident repos' content is NOT re-appended, and every prior blob is carried
//     forward at its exact byte offset (diskstore.ContentStoreAppender).
//   - REWRITE only the MOEDEX05 shards whose repo set intersects the changed/added/
//     removed repos; carry UNAFFECTED shards forward byte-for-byte (a file copy,
//     exactly as parity.Rebuild does for the inlined format).
//   - UPDATE the parity.Manifest (shard→repo membership + per-repo HEAD).
//
// This is the served-side analogue of RefreshCAS's per-blob delta, targeting the
// served deduped format. The freshness key is git HEAD: a served shard is affected
// iff one of its repos' CURRENT CAS-manifest HEAD differs from the HEAD the served
// manifest recorded (changed), or a repo appeared (added) / vanished (removed) —
// the same classification parity.DetectChanges uses.
//
// PARITY (SACRED): a delta-re-exported dir returns byte-identical (file,line)
// matches to a FULL deduped re-export (and thus to a direct build and to ripgrep).
// It preserves it because: (1) affected repos are re-packed from the SAME CAS
// (sha,content,repo,rel,abs) tuples a full export would, through the same
// index.AddFile + SaveDeduped pipeline; (2) unaffected shards are copied byte-for-
// byte and their content SHAs remain resolvable (the append carries all prior
// content forward); (3) results merge per-shard by (repo,rel,line) with no cross-
// shard blob-ID space — so the shard PARTITION is irrelevant to the match set, only
// the union of (repo,file) refs and their content matters, and that union is
// identical to a full export's. The content-integrity verify (GitBlobSHA1 at open)
// and the content-true directory fingerprint are unchanged: the appended store is a
// valid MOECONT1 file the serving path opens and verifies exactly as a full export's.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/parity"
)

// Crash-safe-swap markers. The delta re-export builds the new dir under a sibling
// temp dir, then swaps with two renames (live -> bak, temp -> live). These fixed
// suffixes let a recovery pass on the NEXT run detect an interrupted swap by the
// leftover siblings and roll forward or back to a complete, manifest-consistent dir.
const (
	dedupRefreshTmpSuffix = ".dedup-refresh-" // sibling holding the new dir being built
	dedupBakSuffix        = ".dedup-bak-"     // sibling holding the prior live dir moved aside
	denseStoreName        = "corpus-embeddings.store"
	denseStoreMetaName    = denseStoreName + ".meta"
	graphFileName         = "corpus-graph.graph"
)

// DedupedDeltaStats reports what a RefreshDedupedShardDir actually did, for honest
// measurement of the served-side delta.
type DedupedDeltaStats struct {
	// ChangedRepos / AddedRepos / RemovedRepos classify the corpus delta vs the prior
	// served manifest (by git HEAD), exactly as parity.DetectChanges would.
	ChangedRepos []string
	AddedRepos   []string
	RemovedRepos []string
	// ShardsRewritten is the number of MOEDEX05 shards re-emitted because their repo
	// set intersected an affected repo. ShardsCarried is the number copied forward
	// byte-for-byte (untouched). Their sum is the new shard count.
	ShardsRewritten int
	ShardsCarried   int
	// BlobsAppended / BytesAppended are the net-new unique blobs and content bytes
	// physically appended to blobs.dat (co-resident/unchanged content NOT re-appended).
	BlobsAppended int
	BytesAppended int64
	// PutsDeduped is the number of PutContent calls during the rewrite that were
	// dedup no-ops (content already present in the carried-forward store) — the
	// cross-shard / co-resident saving the append realizes.
	PutsDeduped int
	// DenseSeedCarried reports that the prior dense store and its validation metadata
	// were hard-linked into the staged directory. The embedding refresh may safely use
	// the stale store as a content-keyed reuse seed before atomically replacing it.
	DenseSeedCarried bool
	GraphSeedCarried bool
}

// IsDedupedDir reports whether dir is an existing deduped served dir (it has a
// shared MOECONT1 content store, diskstore.ContentStoreName). A delta refresh is
// possible only over such a dir; an absent store means there is nothing to extend
// (the caller does a full ExportDedupedShardDir instead).
func IsDedupedDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, diskstore.ContentStoreName))
	return err == nil
}

// dirIsCompleteDedupedExport reports whether dir is a fully-written deduped served
// dir: it has BOTH a parity manifest (written LAST, atomically, in the build) AND a
// content store. The manifest's presence is the durable "complete" marker — the
// build writes shards + content store first, then the manifest via atomic temp+
// rename, so a dir with a manifest is provably finished.
func dirIsCompleteDedupedExport(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, parity.ManifestName)); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, diskstore.ContentStoreName)); err != nil {
		return false
	}
	return true
}

// RecoverInterruptedDedupedSwap makes the delta re-export's directory swap crash-
// RECOVERABLE: given the configured live path outShardDir, it inspects the sibling
// markers a delta refresh leaves during its two-rename swap (a ".dedup-refresh-*"
// holding the new dir, a ".dedup-bak-*" holding the prior live dir moved aside) and
// rolls forward or back so the live path ends up as EITHER the complete new dir OR
// the complete old dir — never absent or partial — with its manifest consistent with
// whatever dir is live. It is idempotent and a no-op in the normal (no-marker) case,
// safe to call at the start of every refresh and before opening a served dir.
//
// Recovery cases (live = outShardDir; bak/tmp = its sibling markers):
//
//	S0 live present, no bak           -> normal; nothing to do (also clears any
//	                                     stray complete tmp left by a prior aborted
//	                                     pre-swap build — see note below).
//	S3 live present, bak present      -> crash AFTER the second rename succeeded but
//	                                     before bak cleanup; the live dir is the new,
//	                                     complete dir. Roll FORWARD: remove bak (+ tmp).
//	S1 live ABSENT, complete tmp      -> crash BETWEEN the two renames; the new dir was
//	                                     fully built (it has a manifest). Roll FORWARD:
//	                                     rename tmp -> live, remove bak.
//	S2 live ABSENT, no complete tmp   -> crash between the renames with the new dir not
//	                                     yet complete (or already consumed). Roll BACK:
//	                                     rename bak -> live (the prior complete dir).
//
// Because the new dir's manifest is written with paths already bound to the LIVE
// path (not the temp path), a rolled-forward dir needs no post-swap manifest rewrite
// — closing the "crash after swap before manifest rewrite" window entirely.
func RecoverInterruptedDedupedSwap(outShardDir string) error {
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
		if strings.HasPrefix(n, base+dedupBakSuffix) {
			baks = append(baks, filepath.Join(parent, n))
		} else if strings.HasPrefix(n, base+dedupRefreshTmpSuffix) {
			tmps = append(tmps, filepath.Join(parent, n))
		}
	}
	// Deterministic order (the suffix is a timestamp; newest last).
	sort.Strings(baks)
	sort.Strings(tmps)

	_, liveErr := os.Stat(outShardDir)
	livePresent := liveErr == nil

	if livePresent {
		// S0 / S3: the live dir is in place. Any lingering bak/tmp is debris from a
		// completed-but-not-cleaned swap (S3) or an aborted pre-swap build; remove them.
		for _, p := range baks {
			_ = os.RemoveAll(p)
		}
		for _, p := range tmps {
			_ = os.RemoveAll(p)
		}
		return nil
	}

	// Live ABSENT: a crash landed between the two renames. Prefer rolling FORWARD to
	// a complete new dir; else roll BACK to the prior dir moved aside.
	for i := len(tmps) - 1; i >= 0; i-- { // newest complete tmp first
		if dirIsCompleteDedupedExport(tmps[i]) {
			if err := os.Rename(tmps[i], outShardDir); err != nil {
				return fmt.Errorf("blobstore: roll-forward interrupted swap: %w", err)
			}
			// Clean the remaining markers.
			for _, p := range append(baks, tmps[:i]...) {
				_ = os.RemoveAll(p)
			}
			return nil
		}
	}
	// No complete new dir: roll back the most-recent bak into place.
	for i := len(baks) - 1; i >= 0; i-- {
		if dirIsCompleteDedupedExport(baks[i]) {
			if err := os.Rename(baks[i], outShardDir); err != nil {
				return fmt.Errorf("blobstore: roll-back interrupted swap: %w", err)
			}
			for _, p := range append(baks[:i], tmps...) {
				_ = os.RemoveAll(p)
			}
			return nil
		}
	}
	// Neither a complete tmp nor a complete bak exists: nothing safe to restore (the
	// live path was never successfully written, or the markers are gone). Leave the
	// debris for an operator rather than guess; the caller will surface a missing-dir
	// error from its normal load path.
	return nil
}

// RefreshDedupedShardDir performs a delta-aware deduped re-export over an existing
// deduped served dir at outShardDir, reading current content from the CAS at casDir.
// It loads the prior served parity.Manifest, classifies each repo against the
// current CAS BlobManifest by git HEAD, rewrites only affected shards, carries the
// rest forward byte-for-byte, and appends only net-new content to blobs.dat. It
// rewrites the parity.Manifest in place and returns it plus the delta stats.
//
// shardBytes governs the re-packing of affected repos into fresh shards (pass <=0
// for parity.DefaultShardBytes); it does not affect carried-forward shards.
//
// PRECONDITION: outShardDir is an existing deduped dir (IsDedupedDir) written by a
// prior ExportDedupedShardDir / RefreshDedupedShardDir, and casDir holds the CURRENT
// CAS (run cas-refresh first so net-new blobs are present). If outShardDir is not a
// deduped dir, callers should fall back to a full ExportDedupedShardDir.
func RefreshDedupedShardDir(casDir, outShardDir string, shardBytes int64) (*parity.Manifest, DedupedDeltaStats, error) {
	var ds DedupedDeltaStats

	// Roll any prior interrupted swap forward/back so we start from a complete,
	// manifest-consistent live dir (idempotent no-op in the normal case).
	if err := RecoverInterruptedDedupedSwap(outShardDir); err != nil {
		return nil, ds, err
	}

	cas, err := LoadBlobManifest(filepath.Join(casDir, BlobManifestName))
	if err != nil {
		return nil, ds, fmt.Errorf("blobstore: load blob manifest: %w", err)
	}
	oldServed, err := parity.LoadManifest(filepath.Join(outShardDir, parity.ManifestName))
	if err != nil {
		return nil, ds, fmt.Errorf("blobstore: load prior served manifest: %w", err)
	}
	csPath := filepath.Join(outShardDir, diskstore.ContentStoreName)
	if _, err := os.Stat(csPath); err != nil {
		return nil, ds, fmt.Errorf("blobstore: prior dir is not deduped (no %s): %w", diskstore.ContentStoreName, err)
	}

	store, err := Open(casDir)
	if err != nil {
		return nil, ds, err
	}
	defer store.Close()

	if shardBytes <= 0 {
		shardBytes = parity.DefaultShardBytes
	}

	// --- 1. Classify repos by freshness identity: served manifest vs current CAS. -
	// A repo present in the CAS but absent or different by HEAD/privacy policy in
	// the served manifest is changed/added; a repo in the served manifest absent
	// from the CAS is removed.
	servedKnown := map[string]bool{}
	servedMeta := map[string]parity.RepoHead{}
	for _, h := range oldServed.Heads {
		servedKnown[h.Dir] = true
		servedMeta[h.Dir] = h
	}
	casByDir := map[string]RepoBlobs{}
	affected := map[string]bool{} // repo dir -> changed/added/removed
	for _, r := range cas.Repos {
		casByDir[r.Dir] = r
		old, known := servedMeta[r.Dir], servedKnown[r.Dir]
		if !known {
			ds.AddedRepos = append(ds.AddedRepos, r.Dir)
			affected[r.Dir] = true
			continue
		}
		if r.Head != old.Head || r.PrivacyFingerprint != old.PrivacyFingerprint {
			ds.ChangedRepos = append(ds.ChangedRepos, r.Dir)
			affected[r.Dir] = true
		}
	}
	for _, h := range oldServed.Heads {
		if _, inCAS := casByDir[h.Dir]; !inCAS {
			ds.RemovedRepos = append(ds.RemovedRepos, h.Dir)
			affected[h.Dir] = true
		}
	}
	// A steady-state daily refresh commonly has no corpus delta. Keep the live
	// directory byte-for-byte and inode-for-inode unchanged: swapping an equivalent
	// directory is wasted I/O and, historically, discarded the dense reuse seed.
	if len(affected) == 0 {
		return oldServed, ds, nil
	}

	// --- 2. Partition prior shards: carry-forward vs rewrite. ---------------------
	// A shard is affected iff any of its repos is affected. Carried shards are copied
	// byte-for-byte; their repos are NOT re-exported. Affected shards' repos are
	// re-packed from the CAS (skipping any removed repo, which simply drops out).
	type carriedShard struct {
		oldPath string
		repos   []string
		bytes   int64
	}
	var carried []carriedShard
	reexport := map[string]bool{} // repo dir -> must be re-packed
	var reexportOrder []string
	addReexport := func(dir string) {
		if !reexport[dir] {
			reexport[dir] = true
			reexportOrder = append(reexportOrder, dir)
		}
	}
	for _, sm := range oldServed.Shards {
		shardAffected := false
		for _, r := range sm.Repos {
			if affected[r] {
				shardAffected = true
				break
			}
		}
		if !shardAffected {
			carried = append(carried, carriedShard{oldPath: sm.Path, repos: sm.Repos, bytes: sm.ContentBytes})
			continue
		}
		for _, r := range sm.Repos {
			// A removed repo (gone from the CAS) is simply not re-packed.
			if _, inCAS := casByDir[r]; inCAS {
				addReexport(r)
			}
		}
	}
	// Newly-added repos (never in any prior shard) are always re-packed.
	for _, dir := range ds.AddedRepos {
		addReexport(dir)
	}
	// A changed repo may have had no prior shard because it was empty or globally
	// Restricted. Re-pack it explicitly so newly eligible content is not missed.
	for _, dir := range ds.ChangedRepos {
		addReexport(dir)
	}

	// --- 3. Append net-new content to blobs.dat (delta over the existing store). --
	// Seed the appender from the EXISTING store so prior content is carried forward
	// byte-for-byte and an already-present content hash is a dedup no-op. We register
	// the content of every blob that any re-exported repo references; co-resident /
	// unchanged content already in the store adds zero bytes.
	appender, err := diskstore.OpenContentStoreAppender(csPath)
	if err != nil {
		return nil, ds, fmt.Errorf("blobstore: open content store for append: %w", err)
	}

	// Stage the re-packed repos into fresh in-RAM indexes FIRST, byte-bucketing them
	// exactly as ExportDedupedShardDir does, recording each blob's content in the
	// appender as we go. We hold the staged indexes (postings only; content aliases
	// the CAS reads) and flush them to shard files after the appended store is
	// written, so a shard never references a content store that lacks its blobs.
	type stagedShard struct {
		ix    *index.Index
		repos []string
		bytes int64
	}
	var (
		staged   []stagedShard
		curIx    = index.New()
		curBytes int64
		curRepos []string
		curSeen  = map[string]bool{}
	)
	flushStaged := func() {
		if curIx.NumBlobs() == 0 {
			return
		}
		staged = append(staged, stagedShard{ix: curIx, repos: curRepos, bytes: curBytes})
		curIx = index.New()
		curBytes = 0
		curRepos = nil
		curSeen = map[string]bool{}
	}
	for _, dir := range reexportOrder {
		r := casByDir[dir]
		contributed := false
		seen := map[string]bool{}
		for _, f := range r.Files {
			abs := filepath.Join(r.Dir, f.RelPath)
			if seen[abs] {
				continue // same abspath already in this repo's batch (matches build/export)
			}
			seen[abs] = true
			content, err := store.Get(f.SHA)
			if err != nil {
				return nil, ds, fmt.Errorf("blobstore: refresh repo %s file %s: %w", r.Label, f.RelPath, err)
			}
			if _, present := appender.Ref(f.SHA); present {
				ds.PutsDeduped++
			}
			appender.PutContent(f.SHA, content)
			curIx.AddFile(r.Label, f.RelPath, abs, f.SHA, content)
			curBytes += int64(len(content))
			contributed = true
		}
		if contributed && !curSeen[r.Dir] {
			curSeen[r.Dir] = true
			curRepos = append(curRepos, r.Dir)
		}
		if curBytes >= shardBytes {
			flushStaged()
		}
	}
	flushStaged()

	ds.BlobsAppended = appender.BlobsAppended()
	ds.BytesAppended = appender.BytesAppended()

	// --- 4. Emit the new shard set into a TEMP dir, then atomically swap it in, so a
	// failure mid-rewrite never corrupts the live dir. The carried shards are copied
	// from their old paths (still present in the live dir until the swap), and the
	// extended content store is written straight into the temp dir (the live
	// blobs.dat is never mutated in place — the swap delivers store+shards atomically).
	//
	// CRASH SAFETY: the manifest is written into tmpDir with its ShardDir + shard
	// Paths already bound to the FINAL live path (outShardDir), not tmpDir — so once
	// tmpDir is renamed to outShardDir the manifest is already consistent and needs NO
	// post-swap rewrite (closing the "crash after swap, before manifest rewrite"
	// window). The shard FILES are physically written to tmpDir under their final
	// basenames; after the rename they sit at exactly the recorded paths. The manifest
	// is written LAST, so a tmpDir carrying a manifest is provably complete — which is
	// the durable marker RecoverInterruptedDedupedSwap keys on to roll an interrupted
	// swap forward. The old dir is kept as a .bak sibling until the swap completes, so
	// a roll-back target always exists between the two renames.
	stamp := time.Now().Format("20060102-150405.000000000")
	tmpDir := outShardDir + dedupRefreshTmpSuffix + stamp
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, ds, err
	}
	cleanupTmp := func() { _ = os.RemoveAll(tmpDir) }

	// Write the extended content store into the temp dir. It streams the prior
	// store's content forward byte-for-byte from the live csPath (the appender's
	// source) and appends only net-new content; the live store is read, not written.
	tmpCSPath := filepath.Join(tmpDir, diskstore.ContentStoreName)
	if err := appender.Write(tmpCSPath); err != nil {
		cleanupTmp()
		return nil, ds, fmt.Errorf("blobstore: append shared content store: %w", err)
	}

	var (
		shards   []parity.ShardManifest
		shardIdx int
	)
	// nextShard returns the PHYSICAL write path (in tmpDir) and the FINAL recorded
	// path (in outShardDir, where the file will live after the swap) for the next
	// shard, advancing the ordinal.
	nextShard := func() (physical, recorded string) {
		name := fmt.Sprintf("shard-%04d.idx", shardIdx)
		shardIdx++
		return filepath.Join(tmpDir, name), filepath.Join(outShardDir, name)
	}

	// Carried shards first (byte-for-byte copy), preserving their repo membership and
	// content-byte accounting.
	for _, cs := range carried {
		phys, rec := nextShard()
		if err := copyFile(cs.oldPath, phys); err != nil {
			cleanupTmp()
			return nil, ds, fmt.Errorf("blobstore: carry deduped shard %s: %w", cs.oldPath, err)
		}
		shards = append(shards, parity.ShardManifest{Path: rec, Repos: cs.repos, ContentBytes: cs.bytes})
		ds.ShardsCarried++
	}

	// Re-packed (rewritten) shards, written in MOEDEX05 against the SAME appender so
	// SaveDeduped's PutContent is a no-op for content already registered above.
	for _, st := range staged {
		phys, rec := nextShard()
		if err := diskstore.SaveDedupedAppender(st.ix, phys, appender); err != nil {
			cleanupTmp()
			return nil, ds, fmt.Errorf("blobstore: save rewritten deduped shard %d: %w", shardIdx-1, err)
		}
		shards = append(shards, parity.ShardManifest{Path: rec, Repos: st.repos, ContentBytes: st.bytes})
		ds.ShardsRewritten++
	}

	// Preserve the expensive dense store as an incremental reuse seed. tmpDir is a
	// sibling of outShardDir, so hard links are same-filesystem and O(1), even for a
	// multi-gigabyte store. Carry the pair only when both are regular files; a missing,
	// partial, or unsupported seed safely falls back to a full rebuild later.
	ds.DenseSeedCarried = carryDenseEmbeddingSeed(outShardDir, tmpDir)
	ds.GraphSeedCarried = carryGraphSeed(outShardDir, tmpDir)

	// Freshness identity is independent of shard membership. In particular, a
	// globally Restricted repo contributes zero blobs but must remain represented
	// so policy relaxation is detected and steady-state refreshes stay stable.
	heads := make([]parity.RepoHead, 0, len(cas.Repos))
	for _, repo := range cas.Repos {
		heads = append(heads, parity.RepoHead{
			Dir: repo.Dir, Label: repo.Label, Head: repo.Head,
			PrivacyFingerprint: repo.PrivacyFingerprint,
			ProjectID:          repo.ProjectID, Managed: repo.Managed,
		})
	}

	// The extended content store was already written into tmpDir above, so the temp
	// dir now holds a complete, self-consistent deduped dir (shards + content store)
	// minus the manifest, which is written LAST (the completeness marker).
	out := &parity.Manifest{
		Version:  parity.ManifestVersion,
		Root:     cas.Root,
		BuiltAt:  time.Now(),
		ShardDir: outShardDir, // FINAL live path — no post-swap rewrite needed
		Heads:    heads,
		Shards:   shards,
	}

	// fsync the temp dir's data files BEFORE writing the manifest, so the manifest
	// (the "complete" marker) only becomes durable after the shards + content store it
	// references are durable. A crash before the manifest leaves an incomplete tmpDir
	// that RecoverInterruptedDedupedSwap will NOT roll forward (it rolls back instead).
	if err := fsyncDirFiles(tmpDir); err != nil {
		cleanupTmp()
		return nil, ds, fmt.Errorf("blobstore: fsync refreshed dir files: %w", err)
	}
	if err := parity.WriteManifest(filepath.Join(tmpDir, parity.ManifestName), out); err != nil {
		cleanupTmp()
		return nil, ds, fmt.Errorf("blobstore: write refreshed manifest: %w", err)
	}

	// --- 5. Crash-safe swap: move live dir aside (kept as .bak), move temp into
	// place. A crash BETWEEN the renames is recovered on the NEXT run by
	// RecoverInterruptedDedupedSwap: the complete tmpDir rolls forward, else the .bak
	// rolls back. A crash AFTER the second rename (before .bak cleanup) leaves the new
	// complete dir live + a stray .bak, which recovery cleans up. Either way the live
	// path is always EITHER the complete old dir OR the complete new dir.
	bak := outShardDir + dedupBakSuffix + stamp
	if err := os.Rename(outShardDir, bak); err != nil {
		cleanupTmp()
		return nil, ds, fmt.Errorf("blobstore: move old deduped dir aside: %w", err)
	}
	if err := os.Rename(tmpDir, outShardDir); err != nil {
		_ = os.Rename(bak, outShardDir) // best-effort immediate restore
		cleanupTmp()
		return nil, ds, fmt.Errorf("blobstore: swap refreshed deduped dir into place: %w", err)
	}
	// Swap succeeded; the manifest already points at the live path. Remove the backup.
	_ = os.RemoveAll(bak)
	return out, ds, nil
}

// fsyncDirFiles fsyncs every regular file directly under dir (and the dir entry
// itself where the platform supports it), so a freshly-built export dir's data is
// durable before the manifest (its completeness marker) is written. Best-effort on
// the directory handle (some platforms reject fsync on a dir); file fsyncs are
// required.
func fsyncDirFiles(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync() // best-effort: many platforms don't support dir fsync
		_ = d.Close()
	}
	return nil
}

// copyFile copies src to dst byte-for-byte (mirrors parity.copyFile; duplicated to
// avoid exporting an internal helper across packages).
func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0o644)
}

// carryDenseEmbeddingSeed hard-links the generated dense store and its validation
// metadata into dstDir as an all-or-nothing pair. RefreshEmbeddings validates the
// model before reuse and writes the replacement store via atomic rename, so carrying
// a stale fingerprint is both intentional and safe. This function is best-effort:
// correctness never depends on the optimization.
func carryDenseEmbeddingSeed(srcDir, dstDir string) bool {
	return carrySeedFiles(srcDir, dstDir, denseStoreName, denseStoreMetaName)
}

func carryGraphSeed(srcDir, dstDir string) bool {
	return carrySeedFiles(srcDir, dstDir, graphFileName)
}

func carrySeedFiles(srcDir, dstDir string, names ...string) bool {
	for _, name := range names {
		info, err := os.Lstat(filepath.Join(srcDir, name))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}

	linked := make([]string, 0, len(names))
	for _, name := range names {
		dst := filepath.Join(dstDir, name)
		if err := os.Link(filepath.Join(srcDir, name), dst); err != nil {
			for _, path := range linked {
				_ = os.Remove(path)
			}
			return false
		}
		linked = append(linked, dst)
	}
	return true
}
