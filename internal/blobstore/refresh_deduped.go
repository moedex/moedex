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
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/parity"
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
}

// IsDedupedDir reports whether dir is an existing deduped served dir (it has a
// shared MOECONT1 content store, diskstore.ContentStoreName). A delta refresh is
// possible only over such a dir; an absent store means there is nothing to extend
// (the caller does a full ExportDedupedShardDir instead).
func IsDedupedDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, diskstore.ContentStoreName))
	return err == nil
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

	// --- 1. Classify repos by HEAD: served-manifest HEAD vs current CAS HEAD. -----
	// A repo present in the CAS but absent/different-HEAD in the served manifest is
	// changed/added; a repo in the served manifest absent from the CAS is removed.
	// This mirrors parity.DetectChanges, keyed on the freshness HEAD both manifests
	// record per repo.
	servedHead := map[string]string{}
	servedKnown := map[string]bool{}
	for _, h := range oldServed.Heads {
		servedHead[h.Dir] = h.Head
		servedKnown[h.Dir] = true
	}
	casByDir := map[string]RepoBlobs{}
	affected := map[string]bool{} // repo dir -> changed/added/removed
	for _, r := range cas.Repos {
		casByDir[r.Dir] = r
		old, known := servedHead[r.Dir], servedKnown[r.Dir]
		if !known {
			ds.AddedRepos = append(ds.AddedRepos, r.Dir)
			affected[r.Dir] = true
			continue
		}
		if r.Head != old {
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
	stamp := time.Now().Format("20060102-150405.000000000")
	tmpDir := outShardDir + ".dedup-refresh-" + stamp
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
		heads    []parity.RepoHead
		shardIdx int
	)
	nextShardPath := func() string {
		p := filepath.Join(tmpDir, fmt.Sprintf("shard-%04d.idx", shardIdx))
		shardIdx++
		return p
	}

	// Carried shards first (byte-for-byte copy), preserving their repo membership and
	// content-byte accounting.
	for _, cs := range carried {
		dst := nextShardPath()
		if err := copyFile(cs.oldPath, dst); err != nil {
			cleanupTmp()
			return nil, ds, fmt.Errorf("blobstore: carry deduped shard %s: %w", cs.oldPath, err)
		}
		shards = append(shards, parity.ShardManifest{Path: dst, Repos: cs.repos, ContentBytes: cs.bytes})
		ds.ShardsCarried++
		for _, r := range cs.repos {
			heads = append(heads, parity.RepoHead{Dir: r, Label: filepath.Base(r), Head: headForServed(servedHead, casByDir, r)})
		}
	}

	// Re-packed (rewritten) shards, written in MOEDEX05 against the SAME appender so
	// SaveDeduped's PutContent is a no-op for content already registered above.
	for _, st := range staged {
		dst := nextShardPath()
		if err := diskstore.SaveDedupedAppender(st.ix, dst, appender); err != nil {
			cleanupTmp()
			return nil, ds, fmt.Errorf("blobstore: save rewritten deduped shard %d: %w", shardIdx-1, err)
		}
		shards = append(shards, parity.ShardManifest{Path: dst, Repos: st.repos, ContentBytes: st.bytes})
		ds.ShardsRewritten++
		for _, r := range st.repos {
			heads = append(heads, parity.RepoHead{Dir: r, Label: filepath.Base(r), Head: casByDir[r].Head})
		}
	}

	// The extended content store was already written into tmpDir above, so the temp
	// dir now holds a complete, self-consistent deduped dir (shards + content store).

	out := &parity.Manifest{
		Version:  parity.ManifestVersion,
		Root:     cas.Root,
		BuiltAt:  time.Now(),
		ShardDir: outShardDir,
		Heads:    heads,
		Shards:   shards,
	}
	if err := parity.WriteManifest(filepath.Join(tmpDir, parity.ManifestName), out); err != nil {
		cleanupTmp()
		return nil, ds, fmt.Errorf("blobstore: write refreshed manifest: %w", err)
	}

	// --- 5. Atomic-ish swap: move live dir aside, move temp into place. -----------
	bak := outShardDir + ".dedup-bak-" + stamp
	if err := os.Rename(outShardDir, bak); err != nil {
		cleanupTmp()
		return nil, ds, fmt.Errorf("blobstore: move old deduped dir aside: %w", err)
	}
	if err := os.Rename(tmpDir, outShardDir); err != nil {
		_ = os.Rename(bak, outShardDir) // best-effort restore
		cleanupTmp()
		return nil, ds, fmt.Errorf("blobstore: swap refreshed deduped dir into place: %w", err)
	}
	_ = os.RemoveAll(bak)

	// Rebind manifest paths to the live dir (they were written under tmpDir).
	if err := rewriteDedupedManifestPaths(outShardDir); err != nil {
		return nil, ds, fmt.Errorf("blobstore: fix up refreshed manifest paths: %w", err)
	}
	out.ShardDir = outShardDir
	for i := range out.Shards {
		out.Shards[i].Path = filepath.Join(outShardDir, filepath.Base(out.Shards[i].Path))
	}
	return out, ds, nil
}

// headForServed returns the HEAD to record for a carried-forward repo: prefer the
// current CAS HEAD (the freshest known), else the prior served HEAD. A carried repo
// is by construction unchanged, so these agree; the fallback covers the (impossible
// for carried) case of a repo absent from the CAS.
func headForServed(servedHead map[string]string, casByDir map[string]RepoBlobs, dir string) string {
	if r, ok := casByDir[dir]; ok {
		return r.Head
	}
	return servedHead[dir]
}

// rewriteDedupedManifestPaths rebinds the manifest's ShardDir and every shard Path
// to dir after the refreshed dir has been swapped into place (mirrors
// cmd/moedex-index rewriteManifestPaths for the served format).
func rewriteDedupedManifestPaths(dir string) error {
	mp := filepath.Join(dir, parity.ManifestName)
	m, err := parity.LoadManifest(mp)
	if err != nil {
		return err
	}
	m.ShardDir = dir
	for i := range m.Shards {
		m.Shards[i].Path = filepath.Join(dir, filepath.Base(m.Shards[i].Path))
	}
	return parity.WriteManifest(mp, m)
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
