package blobstore

// BuildCAS / RefreshCAS are the offline-indexer entry points that drive the CAS.
// They reuse ingest.DiscoverRepos / ingest.Repo / ingest.Head VERBATIM, so the
// CAS ingests the exact same text-only, BOM-stripped, binary-skipped blob
// universe as a parity build — preserving the parity scope F (see export.go).

import (
	"fmt"
	"path/filepath"

	"moedex/internal/ingest"
)

// DeltaStats reports what a RefreshCAS actually did, for honest measurement of
// the per-blob delta: BlobsAdded is the number of net-new unique blobs physically
// appended to the pack, BytesAdded the content bytes those added (pack growth),
// and PutsSkipped the number of Put attempts that were dedup no-ops (an already
// present SHA — the cross-shard / co-resident saving).
type DeltaStats struct {
	ChangedRepos []string
	AddedRepos   []string
	RemovedRepos []string
	BlobsAdded   int
	BytesAdded   int64
	PutsSkipped  int
}

// ingestFn / headFn / discoverFn are injected in tests; production passes the
// real ingest functions. They mirror the seams parity.DetectChanges already uses.
type (
	ingestFn   = func(repoName, dir string) ([]ingest.File, error)
	headFn     = func(dir string) (string, error)
	discoverFn = func(root string) ([]string, error)
)

// BuildCAS discovers every repo under root, ingests each, and Puts every text
// file's (SHA, content) into a CAS at casDir. Because Put is idempotent on SHA,
// a blob shared by N repos is stored ONCE regardless of which shard it would have
// landed in — this is the global cross-shard dedup the per-shard model cannot do.
// It writes the BlobManifest (repo->blobset + global dedup Stats) under casDir and
// returns the manifest. The CAS is created fresh: an existing pack at casDir is
// reused (Open is append-only), so call on an empty dir for a clean build.
func BuildCAS(root, casDir string) (*BlobManifest, error) {
	return buildCAS(root, casDir, ingest.DiscoverRepos, ingest.Repo, ingest.Head)
}

func buildCAS(root, casDir string, discover discoverFn, ingestRepo ingestFn, head headFn) (*BlobManifest, error) {
	repos, err := discover(root)
	if err != nil {
		return nil, fmt.Errorf("blobstore: discover repos: %w", err)
	}
	store, err := Open(casDir)
	if err != nil {
		return nil, err
	}
	defer store.Close()

	m := &BlobManifest{Version: BlobManifestVersion, Root: root, CASDir: casDir}
	for _, dir := range repos {
		rb, raw, refs, err := ingestRepoBlobs(store, dir, ingestRepo, head)
		if err != nil {
			// A repo that fails to ingest is skipped (not fatal), matching parity.Build.
			continue
		}
		m.Repos = append(m.Repos, rb)
		m.Stats.RawBytes += raw
		m.Stats.FileRefs += refs
	}
	m.Stats.UniqueBlobs = store.Len()
	m.Stats.StoredBytes = store.BytesStored()

	if err := store.Flush(); err != nil {
		return nil, err
	}
	if err := WriteBlobManifest(filepath.Join(casDir, BlobManifestName), m); err != nil {
		return nil, err
	}
	return m, nil
}

// ingestRepoBlobs ingests one repo, Puts each unique-per-batch file's content
// into the store, and returns the repo's RepoBlobs record plus the raw bytes seen
// and the file-ref count. It dedups by AbsPath within the repo's batch exactly as
// parity.Build/buildShards do (nested-repo overlap guard).
func ingestRepoBlobs(store *Store, dir string, ingestRepo ingestFn, head headFn) (RepoBlobs, int64, int, error) {
	files, err := ingestRepo(filepath.Base(dir), dir)
	if err != nil {
		return RepoBlobs{}, 0, 0, err
	}
	h, _ := head(dir) // "" if unreadable; recorded as-is (commitless-repo stable)
	rb := RepoBlobs{Dir: dir, Label: filepath.Base(dir), Head: h}

	var raw int64
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f.AbsPath] {
			continue // same abspath already in this repo's batch
		}
		seen[f.AbsPath] = true
		if _, err := store.Put(f.SHA, f.Content); err != nil {
			return RepoBlobs{}, 0, 0, err
		}
		rb.Files = append(rb.Files, FileEntry{SHA: f.SHA, RelPath: f.RelPath})
		raw += int64(len(f.Content))
	}
	return rb, raw, len(rb.Files), nil
}

// RefreshCAS performs a per-blob delta refresh against an existing CAS. Given the
// prior manifest, it re-discovers repos under root and:
//
//   - for a CHANGED repo (HEAD differs from the recorded one) or an ADDED repo
//     (absent from the manifest), it re-ingests ONLY that repo and Puts its files;
//     idempotent Put means only the repo's net-new blob SHAs are physically added,
//     and — crucially — its co-resident repos are NOT re-ingested (the win over
//     parity.Rebuild, which re-ingests every repo sharing an affected shard);
//   - for a REMOVED repo (gone from disk), it drops the repo's manifest entry; its
//     now-unreferenced blobs are LEFT in the append-only pack (compaction/GC is
//     deferred — see package doc), so a future compactor has the manifest's
//     referenced-set as a correct liveness signal;
//   - an UNCHANGED repo is carried forward verbatim with zero Puts.
//
// HEAD comparison matches parity.DetectChanges: an unreadable current HEAD is
// normalized to "" and compared against the recorded "", so a commitless repo is
// stable across refreshes (not a perpetual change trigger).
//
// It rewrites the manifest and returns the new manifest plus DeltaStats proving
// the delta was minimal. root defaults to old.Root when empty.
func RefreshCAS(old *BlobManifest, root, casDir string) (*BlobManifest, DeltaStats, error) {
	return refreshCAS(old, root, casDir, ingest.DiscoverRepos, ingest.Repo, ingest.Head)
}

func refreshCAS(old *BlobManifest, root, casDir string, discover discoverFn, ingestRepo ingestFn, head headFn) (*BlobManifest, DeltaStats, error) {
	if root == "" {
		root = old.Root
	}
	current, err := discover(root)
	if err != nil {
		return nil, DeltaStats{}, fmt.Errorf("blobstore: discover repos: %w", err)
	}
	currentSet := map[string]bool{}
	for _, d := range current {
		currentSet[d] = true
	}

	store, err := Open(casDir)
	if err != nil {
		return nil, DeltaStats{}, err
	}
	defer store.Close()

	blobsBefore := store.Len()
	bytesBefore := store.BytesStored()

	var ds DeltaStats
	m := &BlobManifest{Version: BlobManifestVersion, Root: root, CASDir: casDir}

	for _, dir := range current {
		oldRepo, known := old.RepoOf(dir)
		if !known {
			// Added repo: ingest fully.
			ds.AddedRepos = append(ds.AddedRepos, dir)
			rb, raw, refs, err := ingestRepoBlobs(store, dir, ingestRepo, head)
			if err != nil {
				continue
			}
			m.Repos = append(m.Repos, rb)
			m.Stats.RawBytes += raw
			m.Stats.FileRefs += refs
			continue
		}
		now, err := head(dir)
		if err != nil {
			now = "" // normalize unreadable HEAD (commitless-repo stable)
		}
		if now == oldRepo.Head {
			// Unchanged: carry the recorded blob set forward verbatim; zero Puts.
			m.Repos = append(m.Repos, oldRepo)
			m.Stats.RawBytes += rawBytesOf(store, oldRepo)
			m.Stats.FileRefs += len(oldRepo.Files)
			continue
		}
		// Changed: re-ingest ONLY this repo; only net-new blobs are physically added.
		ds.ChangedRepos = append(ds.ChangedRepos, dir)
		ds.PutsSkipped += countPresent(store, ingestRepo, dir) // pre-count of dedup hits
		rb, raw, refs, err := ingestRepoBlobs(store, dir, ingestRepo, head)
		if err != nil {
			// Re-ingest failed (repo vanished mid-refresh): treat as removed by omission.
			ds.RemovedRepos = append(ds.RemovedRepos, dir)
			continue
		}
		m.Repos = append(m.Repos, rb)
		m.Stats.RawBytes += raw
		m.Stats.FileRefs += refs
	}
	// Removed repos: in old manifest, gone from disk now. Dropped by omission.
	for _, r := range old.Repos {
		if !currentSet[r.Dir] {
			ds.RemovedRepos = append(ds.RemovedRepos, r.Dir)
		}
	}

	m.Stats.UniqueBlobs = store.Len()
	m.Stats.StoredBytes = store.BytesStored()
	ds.BlobsAdded = store.Len() - blobsBefore
	ds.BytesAdded = store.BytesStored() - bytesBefore

	if err := store.Flush(); err != nil {
		return nil, DeltaStats{}, err
	}
	if err := WriteBlobManifest(filepath.Join(casDir, BlobManifestName), m); err != nil {
		return nil, DeltaStats{}, err
	}
	return m, ds, nil
}

// rawBytesOf sums the content bytes of a carried-forward repo's files by reading
// each blob's content length from the store (the bytes the repo contributes to
// RawBytes, counting every file reference).
func rawBytesOf(store *Store, r RepoBlobs) int64 {
	var n int64
	for _, f := range r.Files {
		if e, ok := store.byID[f.SHA]; ok {
			n += e.contentLen
		}
	}
	return n
}

// countPresent re-ingests a changed repo's file list (cheaply, before the real
// ingest) and counts how many of its distinct-by-abspath files already have their
// SHA in the store — i.e. the Puts that will be dedup no-ops. It is an honest
// measurement of the co-resident/unchanged-blob saving, separate from the actual
// ingest below. A re-ingest error yields 0 (the real ingest will handle it).
func countPresent(store *Store, ingestRepo ingestFn, dir string) int {
	files, err := ingestRepo(filepath.Base(dir), dir)
	if err != nil {
		return 0
	}
	n := 0
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f.AbsPath] {
			continue
		}
		seen[f.AbsPath] = true
		if store.Has(f.SHA) {
			n++
		}
	}
	return n
}
