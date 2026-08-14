package blobstore

// BuildCAS / RefreshCAS are the offline-indexer entry points that drive the CAS.
// They reuse ingest.DiscoverRepos / ingest.Repo / ingest.Head VERBATIM, so the
// CAS ingests the exact same text-only, BOM-stripped, binary-skipped blob
// universe as a parity build — preserving the parity scope F (see export.go).

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"moedex/internal/ingest"
)

// statFn is the seam used to probe a repo's .git presence; production uses
// os.Stat. Tests override it to simulate a non-ENOENT stat error (EACCES/EIO/
// racing parent-dir failure) and assert that "couldn't tell" never causes a
// still-present repo to be dropped.
var statFn = os.Stat

// repoGone reports whether a repo's git repo is GENUINELY gone from disk — the
// ONLY condition under which a refresh may classify it as removed. It probes the
// SAME signal DiscoverRepos keys on (a ".git" entry, dir OR file — a worktree/
// submodule gitlink is a ".git" file), and distinguishes three outcomes:
//
//   - stat SUCCESS              -> present; gone=false (carry forward / handled).
//   - stat error, IsNotExist    -> genuinely removed; gone=true.
//   - stat error, NOT IsNotExist (EACCES/EIO/transient) -> AMBIGUOUS, we could
//     NOT tell; gone=false so the entry is carried forward, never dropped.
//
// Only a definitive ENOENT classifies a repo as removed; any "couldn't tell"
// outcome is treated as still-present (the data-loss-averse choice — carrying a
// stale entry forward is at worst a deferred removal, but a wrongful drop is
// silent, irrecoverable under-approximation). The caller records ambiguous cases
// in FailedRepos so the next refresh retries.
func repoGone(repoDir string) bool {
	_, err := statFn(filepath.Join(repoDir, ".git"))
	return err != nil && os.IsNotExist(err)
}

// contentKey is the CONTENT-TRUE blob key: the SHA-1 of the bytes actually
// ingested (f.Content), computed in git's blob-object form ("blob <len>\0" +
// content). This is the fix for the dirty-file SHA skew: ingest reads
// working-tree bytes but f.SHA comes from `git ls-files -s` (the committed/index
// blob SHA), so for a file whose working tree differs from its committed blob the
// two disagree. Keying the CAS by f.SHA would dedup-skip a dirty file whose
// committed SHA is already stored, dropping its real bytes; keying by a hash of
// the ingested bytes guarantees a Put only dedups when the stored bytes are
// genuinely identical.
//
// For a CLEAN file (working tree == committed blob) this reproduces git's blob
// SHA exactly (verified against `git hash-object`), so on a clean corpus the CAS
// keys are byte-identical to the f.SHA the direct parity build keys index.AddFile
// by — the export stays byte-equivalent to a direct build. For a dirty file the
// keys diverge precisely where they must, making the content findable.
//
// BOM SEMANTICS (intentional, parity-safe): contentKey is computed on the bytes
// ingest actually produced — i.e. AFTER ingest.Repo strips a leading UTF-8 BOM.
// So a BOM-prefixed file and a non-BOM file with identical post-strip content map
// to the SAME key and dedup to ONE CAS blob, whereas a direct `moedex-index build`
// (which keys its index by git's blob SHA of the UNstripped file) keeps them as
// two blobs. This does NOT break parity: BOTH paths trigram-index and search the
// SAME BOM-stripped bytes (ingest strips before either path sees content), so the
// searchable content and (file,line) match results are identical. The CAS merely
// dedups MORE — one blob carrying BOTH file refs instead of two blobs carrying one
// ref each — which surfaces every matching path (no ref dropped) and cannot
// under-approximate. (See TestBOMAndNonBOMDedupToOneBlobBothPathsSurface.)
func contentKey(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// DeltaStats reports what a RefreshCAS actually did, for honest measurement of
// the per-blob delta: BlobsAdded is the number of net-new unique blobs physically
// appended to the pack, BytesAdded the content bytes those added (pack growth),
// and PutsSkipped the number of Put attempts that were dedup no-ops (an already
// present SHA — the cross-shard / co-resident saving).
type DeltaStats struct {
	ChangedRepos []string
	AddedRepos   []string
	RemovedRepos []string
	// FailedRepos are repos still present on disk whose re-ingest transiently
	// failed during this refresh. Their PRIOR manifest entry is carried forward
	// unchanged (no data loss), so they remain fully searchable; the next refresh
	// retries them. A failure here is NOT a removal — only a repo genuinely gone
	// from disk is classified removed.
	FailedRepos []string
	BlobsAdded  int
	BytesAdded  int64
	PutsSkipped int
}

// ingestFn / headFn / discoverFn are injected in tests; production passes the
// real ingest functions. They mirror the seams parity.DetectChanges already uses.
type (
	ingestFn         = func(repoName, dir string) ([]ingest.File, error)
	headFn           = func(dir string) (string, error)
	discoverFn       = func(root string) ([]string, error)
	sourceDiscoverFn = func(root string) ([]ingest.RepoSource, error)
)

// BuildCAS discovers every repo under root, ingests each, and Puts every text
// file's (SHA, content) into a CAS at casDir. Because Put is idempotent on SHA,
// a blob shared by N repos is stored ONCE regardless of which shard it would have
// landed in — this is the global cross-shard dedup the per-shard model cannot do.
// It writes the BlobManifest (repo->blobset + global dedup Stats) under casDir and
// returns the manifest. The CAS is created fresh: an existing pack at casDir is
// reused (Open is append-only), so call on an empty dir for a clean build.
func BuildCAS(root, casDir string) (*BlobManifest, error) {
	return buildCASSources(root, casDir, ingest.DiscoverSources, ingest.Repo, ingest.Head)
}

func buildCAS(root, casDir string, discover discoverFn, ingestRepo ingestFn, head headFn) (*BlobManifest, error) {
	return buildCASSources(root, casDir, legacySourceDiscover(discover, head), ingestRepo, head)
}

func buildCASSources(root, casDir string, discover sourceDiscoverFn, ingestRepo ingestFn, head headFn) (*BlobManifest, error) {
	sources, err := discover(root)
	if err != nil {
		return nil, fmt.Errorf("blobstore: discover sources: %w", err)
	}
	for _, source := range sources {
		if err := ingest.VerifySource(source); err != nil {
			return nil, err
		}
	}
	store, err := Open(casDir)
	if err != nil {
		return nil, err
	}
	defer store.Close()

	m := &BlobManifest{Version: BlobManifestVersion, Root: root, CASDir: casDir}
	for _, source := range sources {
		rb, raw, refs, _, err := ingestSourceBlobs(store, source, ingestRepo, head)
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
// into the store, and returns the repo's RepoBlobs record, the raw bytes seen,
// the file-ref count, and the number of Puts that were dedup no-ops (skipped,
// an already-present SHA — store.Put's added=false). It dedups by AbsPath within
// the repo's batch exactly as parity.Build/buildShards do (nested-repo overlap
// guard).
func ingestRepoBlobs(store *Store, dir string, ingestRepo ingestFn, head headFn) (RepoBlobs, int64, int, int, error) {
	source := ingest.RepoSource{Namespace: filepath.Base(dir), Dir: dir}
	return ingestSourceBlobs(store, source, ingestRepo, head)
}

func ingestSourceBlobs(store *Store, source ingest.RepoSource, ingestRepo ingestFn, head headFn) (RepoBlobs, int64, int, int, error) {
	if err := ingest.VerifySource(source); err != nil {
		return RepoBlobs{}, 0, 0, 0, err
	}
	files, err := ingestRepo(source.Namespace, source.Dir)
	if err != nil {
		return RepoBlobs{}, 0, 0, 0, err
	}
	h, _ := head(source.Dir) // "" if unreadable; recorded as-is (commitless-repo stable)
	rb := RepoBlobs{Dir: source.Dir, Label: source.Namespace, Head: h, ProjectID: source.ProjectID, Managed: source.Managed}

	var raw int64
	var skipped int
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f.AbsPath] {
			continue // same abspath already in this repo's batch
		}
		seen[f.AbsPath] = true
		// Key by a hash of the bytes actually ingested, NOT git's index SHA, so a
		// dirty file (working tree != committed blob) is stored under its real
		// content hash and a Put only dedups against genuinely identical bytes.
		key := contentKey(f.Content)
		added, err := store.Put(key, f.Content)
		if err != nil {
			return RepoBlobs{}, 0, 0, 0, err
		}
		if !added {
			skipped++
		}
		rb.Files = append(rb.Files, FileEntry{SHA: key, RelPath: f.RelPath})
		raw += int64(len(f.Content))
	}
	return rb, raw, len(rb.Files), skipped, nil
}

// RefreshCAS performs a per-blob delta refresh against an existing CAS. Given the
// prior manifest, it re-discovers repos under root and:
//
//   - for a CHANGED repo (HEAD differs from the recorded one) or an ADDED repo
//     (absent from the manifest), it re-ingests ONLY that repo and Puts its files;
//     idempotent Put means only the repo's net-new blob SHAs are physically added,
//     and — crucially — its co-resident repos are NOT re-ingested (the win over
//     parity.Rebuild, which re-ingests every repo sharing an affected shard);
//   - for a REMOVED repo (its .git is genuinely gone from disk), it drops the
//     repo's manifest entry; its now-unreferenced blobs are LEFT in the
//     append-only pack (compaction/GC is deferred — see package doc), so a future
//     compactor has the manifest's referenced-set as a correct liveness signal;
//   - for a repo whose re-ingest TRANSIENTLY FAILS but is still present on disk,
//     OR an old-manifest repo that the current discovery walk did not return but
//     whose .git is still present on disk (a transient discovery MISS — same
//     data-loss class, different trigger), it CARRIES FORWARD the prior manifest
//     entry unchanged (so none of that repo's searchable content is lost) and
//     records the repo in ds.FailedRepos; the next refresh retries it. Only a repo
//     whose .git is genuinely gone is "removed";
//   - an UNCHANGED repo is carried forward verbatim with zero Puts.
//
// HEAD comparison matches parity.DetectChanges: an unreadable current HEAD is
// normalized to "" and compared against the recorded "", so a commitless repo is
// stable across refreshes (not a perpetual change trigger).
//
// It rewrites the manifest and returns the new manifest plus DeltaStats proving
// the delta was minimal. root defaults to old.Root when empty.
func RefreshCAS(old *BlobManifest, root, casDir string) (*BlobManifest, DeltaStats, error) {
	return refreshCASSources(old, root, casDir, ingest.DiscoverSources, ingest.Repo, ingest.Head)
}

func refreshCAS(old *BlobManifest, root, casDir string, discover discoverFn, ingestRepo ingestFn, head headFn) (*BlobManifest, DeltaStats, error) {
	return refreshCASSources(old, root, casDir, legacySourceDiscover(discover, head), ingestRepo, head)
}

func refreshCASSources(old *BlobManifest, root, casDir string, discover sourceDiscoverFn, ingestRepo ingestFn, head headFn) (*BlobManifest, DeltaStats, error) {
	if root == "" {
		root = old.Root
	}
	current, err := discover(root)
	if err != nil {
		return nil, DeltaStats{}, fmt.Errorf("blobstore: discover sources: %w", err)
	}
	for _, source := range current {
		if err := ingest.VerifySource(source); err != nil {
			return nil, DeltaStats{}, err
		}
	}
	currentSet := map[string]bool{}
	for _, source := range current {
		currentSet[source.Dir] = true
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

	for _, source := range current {
		dir := source.Dir
		oldRepo, known := old.RepoOf(dir)
		if !known {
			// Added repo: ingest fully. A transient ingest failure of a brand-new
			// repo has nothing to carry forward (it was never in the manifest), so
			// it is simply omitted this round and surfaced as failed (the next
			// refresh retries it) — matching parity.Build's skip-on-ingest-error.
			ds.AddedRepos = append(ds.AddedRepos, dir)
			rb, raw, refs, _, err := ingestSourceBlobs(store, source, ingestRepo, head)
			if err != nil {
				ds.FailedRepos = append(ds.FailedRepos, dir)
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
		if now == oldRepo.Head && sourceMatchesRepo(source, oldRepo) {
			// Unchanged: carry the recorded blob set forward verbatim; zero Puts.
			m.Repos = append(m.Repos, oldRepo)
			m.Stats.RawBytes += rawBytesOf(store, oldRepo)
			m.Stats.FileRefs += len(oldRepo.Files)
			continue
		}
		// Changed: re-ingest ONLY this repo; only net-new blobs are physically added.
		ds.ChangedRepos = append(ds.ChangedRepos, dir)
		rb, raw, refs, skipped, err := ingestSourceBlobs(store, source, ingestRepo, head)
		if err != nil {
			// Re-ingest FAILED for a repo that is STILL ON DISK (discover found it):
			// this is a transient git/read error, NOT a removal. Dropping it here
			// would silently lose all of this repo's previously searchable content
			// from every subsequent export. CARRY FORWARD the prior manifest entry
			// unchanged so its blobs/paths survive, and surface the failure; the
			// next refresh retries it. (Drop it from ChangedRepos — it did not
			// actually change in the manifest this round.)
			ds.ChangedRepos = ds.ChangedRepos[:len(ds.ChangedRepos)-1]
			ds.FailedRepos = append(ds.FailedRepos, dir)
			m.Repos = append(m.Repos, oldRepo)
			m.Stats.RawBytes += rawBytesOf(store, oldRepo)
			m.Stats.FileRefs += len(oldRepo.Files)
			continue
		}
		ds.PutsSkipped += skipped
		m.Repos = append(m.Repos, rb)
		m.Stats.RawBytes += raw
		m.Stats.FileRefs += refs
	}
	// Old-manifest repos absent from the current discovered set. A discovery MISS
	// is NOT proof of removal: DiscoverRepos can transiently fail to return a repo
	// (an unreadable parent dir is skipped mid-walk, a racing rename, etc.). Probe
	// the .git before classifying — same data-loss class as the re-ingest-failure
	// path, different trigger. The classification is total and airtight (repoGone):
	//   - .git GENUINELY gone (stat ENOENT) -> a real removal; drop by omission.
	//   - .git present, OR stat AMBIGUOUS (a non-ENOENT error: EACCES/EIO/transient
	//     where we could NOT tell) -> CARRY FORWARD the prior manifest entry
	//     unchanged (no content lost) and surface it as failed, so the next refresh
	//     retries it. Only a definitive ENOENT removes; "couldn't tell" never drops.
	for _, r := range old.Repos {
		if currentSet[r.Dir] {
			continue // handled in the main loop above
		}
		if repoGone(r.Dir) {
			ds.RemovedRepos = append(ds.RemovedRepos, r.Dir)
			continue
		}
		ds.FailedRepos = append(ds.FailedRepos, r.Dir)
		m.Repos = append(m.Repos, r)
		m.Stats.RawBytes += rawBytesOf(store, r)
		m.Stats.FileRefs += len(r.Files)
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

func legacySourceDiscover(discover discoverFn, head headFn) sourceDiscoverFn {
	return func(root string) ([]ingest.RepoSource, error) {
		dirs, err := discover(root)
		if err != nil {
			return nil, err
		}
		sources := make([]ingest.RepoSource, 0, len(dirs))
		for _, dir := range dirs {
			h, err := head(dir)
			if err != nil {
				h = ""
			}
			sources = append(sources, ingest.RepoSource{Namespace: filepath.Base(dir), Dir: dir, LockedCommit: h})
		}
		return sources, nil
	}
}

func sourceMatchesRepo(source ingest.RepoSource, repo RepoBlobs) bool {
	return source.Namespace == repo.Label && source.ProjectID == repo.ProjectID && source.Managed == repo.Managed
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
