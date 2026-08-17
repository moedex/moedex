// Command moedex-index builds and refreshes the servable shard directory the
// retrieval daemon (moedex-serve) consumes. It is the offline indexing/freshness
// tool — the daemon itself only reads shards; this command produces and updates
// them.
//
// A servable shard dir is a flat directory of shard-NNNN.idx files plus a
// manifest.json freshness sidecar (repo->shard membership + each repo's git HEAD
// at ingest). Three subcommands:
//
//	moedex-index build   -corpus ROOT -shard-dir DIR [-shard-bytes N] [-force]
//	moedex-index check   -shard-dir DIR [-corpus ROOT]
//	moedex-index refresh -shard-dir DIR [-corpus ROOT] [-keep-backup]
//
// A second family of subcommands operates the content-addressable blob store
// (CAS) — the storage layer that stores each unique blob ONCE for the whole
// corpus (global cross-shard dedup) and re-indexes a changed repo's net-new
// blobs only (per-blob delta). See internal/blobstore.
//
//		moedex-index cas-build   -corpus ROOT -cas-dir DIR
//		moedex-index cas-refresh -cas-dir DIR [-corpus ROOT]
//		moedex-index cas-export  -cas-dir DIR -shard-dir OUT [-shard-bytes N] [-force]
//		moedex-index cas-compact [-cas-dir DIR] [-shard-dir DIR]
//
//	  - cas-compact reclaims dead (unreferenced) content from the append-only content
//	    stores, IN PLACE, by rewriting each store from its own LIVE entries (NOT a re-
//	    ingest from git, NOT a full re-export). -cas-dir compacts the CAS pack
//	    (blobs.pack/blobs.idx) keeping only blobs referenced by the live manifest;
//	    -shard-dir compacts the deduped served store (blobs.dat) keeping only content
//	    referenced by the live MOEDEX05 shards. Pass either or both. The swap is crash-
//	    safe + recoverable. This is the CHEAP alternative to `cas-export -deduped
//	    -force` (which rebuilds a dead-free store from the CAS); both yield a dead-free
//	    store, but compaction re-exports nothing.
//
//	  - cas-export materializes a servable shard dir from the CAS. With -deduped it
//	    writes the deduped served format (content-less MOEDEX05 shards + one shared
//	    blobs.dat) so blob content is stored once corpus-wide; without it, the
//	    parity-preserving inlined bridge. A `-deduped` export over an EXISTING
//	    deduped dir is DELTA-AWARE: it appends only net-new content to blobs.dat and
//	    rewrites only the shards whose repos changed, carrying the rest forward byte-
//	    for-byte (the served-side analogue of cas-refresh's per-blob CAS delta).
//	    -force forces a full re-export.
//
//	  - build   indexes every git repo under -corpus into byte-sized shards and
//	            writes the manifest. The result is directly servable
//	            (moedex-serve -shard-dir DIR).
//	  - check   compares the manifest against the corpus on disk (each repo's
//	            current `git rev-parse HEAD`) and prints changed/added/removed
//	            repos. Read-only; never mutates the shard dir.
//	  - refresh runs check, then if anything changed rebuilds only the affected
//	            shards into a fresh dir and atomically swaps it into place. The old
//	            dir is removed after a successful swap unless -keep-backup is
//	            given, in which case it is kept as DIR.bak-*.
//
// The corpus root for check/refresh defaults to the Root recorded in the
// manifest, so an operator (or cron) only needs the shard dir; -corpus overrides
// it (e.g. if the corpus moved).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"moedex/internal/blobstore"
	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/ingest"
	"moedex/internal/parity"
	"moedex/internal/server"
	"moedex/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "build":
		err = runBuild(os.Args[2:])
	case "check":
		err = runCheck(os.Args[2:])
	case "refresh":
		err = runRefresh(os.Args[2:])
	case "cas-build":
		err = runCASBuild(os.Args[2:])
	case "cas-refresh":
		err = runCASRefresh(os.Args[2:])
	case "cas-export":
		err = runCASExport(os.Args[2:])
	case "cas-compact":
		err = runCASCompact(os.Args[2:])
	case "doctor":
		err = runDoctor(os.Args[2:])
	case "version", "-version", "--version":
		fmt.Println(version.Line("moedex-index", false))
		return
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "moedex-index: unknown subcommand %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "moedex-index: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `moedex-index — build and refresh the servable shard directory.

Usage:
  moedex-index build   -corpus ROOT -shard-dir DIR [-shard-bytes N] [-force] [-v]
  moedex-index check   -shard-dir DIR [-corpus ROOT]
  moedex-index refresh -shard-dir DIR [-corpus ROOT] [-keep-backup] [-v]
  moedex-index doctor  [-shard-dir DIR] [-addr HOST:PORT] [-strict]
      Read-only preflight: binary skew/shadows, shard-dir layout + correct refresh
      command, dense sidecar freshness, daemon + launchd health. Non-zero on a
      critical problem (so a refresh can abort before the destructive swap).

Content-addressable store (global cross-shard dedup + per-blob delta):
  moedex-index cas-build   -corpus ROOT -cas-dir DIR
  moedex-index cas-refresh -cas-dir DIR [-corpus ROOT]
  moedex-index cas-export  -cas-dir DIR -shard-dir OUT [-shard-bytes N] [-force]
  moedex-index cas-compact [-cas-dir DIR] [-shard-dir DIR]

  - cas-build   ingests every git repo under -corpus into a global content-
                addressed blob store at -cas-dir, storing each unique blob ONCE
                across the whole corpus (cross-shard dedup), and writes a
                repo->blobset manifest. Prints the dedup ratio.
  - cas-refresh diffs each repo's current blob set against the manifest and adds
                ONLY net-new blobs (per-blob delta); co-resident repos are not
                re-ingested. Prints blobs/bytes added.
  - cas-export  materializes a servable shard dir + manifest from the CAS. With
                -deduped, content-less MOEDEX05 shards + one shared blobs.dat
                (content stored once corpus-wide); over an EXISTING deduped dir it
                is DELTA-AWARE (appends only net-new content, rewrites only changed
                shards). Without -deduped, the inlined parity-preserving bridge.
                -force forces a full (re-)export.
  - cas-compact reclaims dead (unreferenced) content IN PLACE by rewriting each
                store from its own LIVE entries (never re-ingesting / re-exporting).
                -cas-dir compacts the CAS pack (keeps only manifest-referenced
                blobs); -shard-dir compacts the deduped served store blobs.dat
                (keeps only content referenced by the live shards). Pass either or
                both. Crash-safe + recoverable. Prints the bytes reclaimed.
`)
}

// ---------------------------------------------------------------------------
// build
// ---------------------------------------------------------------------------

func runBuild(args []string) error {
	fs := newFlagSet("build")
	corpus := fs.String("corpus", os.Getenv("MOEDEX_CORPUS"), "corpus root (every git repo beneath it is indexed)")
	shardDir := fs.String("shard-dir", "", "output shard directory (servable by moedex-serve)")
	shardBytes := fs.Int64("shard-bytes", parity.DefaultShardBytes, "target indexed-content bytes per shard")
	force := fs.Bool("force", false, "clear a non-empty shard dir before building")
	verbose := fs.Bool("v", false, "log per-shard progress")
	selective := fs.Bool("selective", false, "opt-in FREE-style selective trigram index (drop near-universal grams; parity-safe via force-scan fallback)")
	gramMaxDF := fs.Float64("gram-max-df", 0.9, "with -selective: keep a trigram only if it occurs in at most this fraction of blobs (0..1)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *corpus == "" || *shardDir == "" {
		return fmt.Errorf("build requires -corpus and -shard-dir")
	}
	root, err := filepath.Abs(*corpus)
	if err != nil {
		return err
	}
	if err := prepareDir(*shardDir, *force); err != nil {
		return err
	}
	logf := mkLogf(*verbose)

	var sel index.GramSelector
	if *selective {
		sel = index.FrequencyThresholdSelector{MaxDocFraction: *gramMaxDF}
		logf("selective index enabled: %s", sel.Describe())
	}

	m, nShards, nFiles, err := buildShards(root, *shardDir, *shardBytes, sel, logf)
	if err != nil {
		return err
	}
	if err := parity.WriteManifest(filepath.Join(*shardDir, parity.ManifestName), m); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	// Build the token, symbol, and graph sidecars so the daemon finds the ranking
	// indexes warm and the mmap graph ready on its next boot. Best-effort: a
	// sidecar failure must not fail an otherwise-good search-index build.
	sidecars := buildSidecars(*shardDir)
	fmt.Printf("built %d shard(s), %d file(s) from %d repo(s) into %s%s\n", nShards, nFiles, len(m.Heads), *shardDir, sidecars)
	return nil
}

// buildSidecars writes all offline-derived sidecars under dir and returns a short
// suffix for the success line. Each family is independent and best-effort: a
// graph failure does not discard valid token/symbol caches, or vice versa.
func buildSidecars(dir string) string {
	var built []string
	if _, _, err := server.BuildSidecars(dir); err != nil {
		fmt.Fprintf(os.Stderr, "moedex-index: warning: build ranking sidecars (daemon will rebuild on boot): %v\n", err)
	} else {
		built = append(built, "token", "symbol")
	}
	if _, err := server.BuildGraphSidecar(dir); err != nil {
		fmt.Fprintf(os.Stderr, "moedex-index: warning: build graph sidecar: %v\n", err)
	} else {
		built = append(built, "graph")
	}
	if len(built) == 0 {
		return ""
	}
	return " (+" + strings.Join(built, "/") + " sidecars)"
}

// buildShards indexes every repo under root into byte-sized shards written to
// shardDir, and returns the freshness Manifest describing them. Packing matches
// parity.Build: a shard is flushed once its accumulated content reaches
// shardBytes (checked at repo boundaries, so a repo is never split across
// shards). Repos that fail to ingest are skipped, not fatal.
// sel is nil for the default all-trigram build; when non-nil the shards are
// built selectively (see index.Builder), which is parity-safe via the
// IndexedGram membership gate — a dropped gram only ever widens the candidate
// set, never drops a match. The flush abstraction targets shardBuilder so the
// packing loop is identical for both paths.
func buildShards(root, shardDir string, shardBytes int64, sel index.GramSelector, logf func(string, ...any)) (*parity.Manifest, int, int, error) {
	if shardBytes <= 0 {
		shardBytes = parity.DefaultShardBytes
	}
	sources, err := ingest.DiscoverSources(root)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("discover sources: %w", err)
	}
	privacyFingerprints := make(map[string]string, len(sources))
	for _, source := range sources {
		if err := ingest.VerifySource(source); err != nil {
			return nil, 0, 0, err
		}
		fingerprint, err := ingest.AIPrivacyFingerprint(source.Dir)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("privacy preflight: %w", err)
		}
		privacyFingerprints[source.Dir] = fingerprint
	}
	logf("discovered %d repos under %s", len(sources), root)

	var (
		shards   []parity.ShardManifest
		heads    []parity.RepoHead
		sb       = index.NewBuildTarget(sel)
		curBytes int64
		curRepos []string
		curSeen  = map[string]bool{}
		shardIdx int
		nFiles   int
	)
	flush := func() error {
		if sb.NumBlobs() == 0 {
			return nil
		}
		path := filepath.Join(shardDir, fmt.Sprintf("shard-%04d.idx", shardIdx))
		ix := sb.Finalize()
		if err := diskstore.Save(ix, path); err != nil {
			return fmt.Errorf("save shard %d: %w", shardIdx, err)
		}
		shards = append(shards, parity.ShardManifest{Path: path, Repos: curRepos, ContentBytes: curBytes})
		logf("  flushed shard %d: %d blobs, %.1f MB", shardIdx, ix.NumBlobs(), float64(curBytes)/1e6)
		shardIdx++
		sb = index.NewBuildTarget(sel)
		curBytes = 0
		curRepos = nil
		curSeen = map[string]bool{}
		runtime.GC() // release the builder's posting map before the next shard
		return nil
	}

	for _, source := range sources {
		repo := source.Dir
		files, err := ingest.Repo(source.Namespace, repo)
		if err != nil {
			if ingest.IsPrivacyPolicyError(err) {
				return nil, 0, 0, fmt.Errorf("privacy preflight: %w", err)
			}
			logf("  skip %s: %v", repo, err)
			continue
		}
		currentPrivacyFingerprint, err := ingest.AIPrivacyFingerprint(repo)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("privacy preflight: %w", err)
		}
		if currentPrivacyFingerprint != privacyFingerprints[repo] {
			return nil, 0, 0, &ingest.PrivacyPolicyError{
				Path: filepath.Join(repo, ingest.AIPrivacyFileName),
				Err:  fmt.Errorf("policy changed during ingest"),
			}
		}
		head, _ := ingest.Head(repo) // "" if unreadable; recorded as-is
		heads = append(heads, parity.RepoHead{
			Dir: repo, Label: source.Namespace, Head: head,
			PrivacyFingerprint: currentPrivacyFingerprint,
			ProjectID:          source.ProjectID, Managed: source.Managed,
		})

		contributed := false
		seen := map[string]bool{}
		for _, f := range files {
			if seen[f.AbsPath] {
				continue // same abspath already in this repo's batch
			}
			seen[f.AbsPath] = true
			sb.AddFile(f.Repo, f.RelPath, f.AbsPath, f.SHA, f.Content)
			curBytes += int64(len(f.Content))
			nFiles++
			contributed = true
		}
		if contributed && !curSeen[repo] {
			curSeen[repo] = true
			curRepos = append(curRepos, repo)
		}
		if curBytes >= shardBytes {
			if err := flush(); err != nil {
				return nil, 0, 0, err
			}
		}
	}
	if err := flush(); err != nil {
		return nil, 0, 0, err
	}

	m := &parity.Manifest{
		Version:  parity.ManifestVersion,
		Root:     root,
		BuiltAt:  time.Now(),
		ShardDir: shardDir,
		Heads:    heads,
		Shards:   shards,
	}
	return m, shardIdx, nFiles, nil
}

// ---------------------------------------------------------------------------
// check
// ---------------------------------------------------------------------------

func runCheck(args []string) error {
	fs := newFlagSet("check")
	shardDir := fs.String("shard-dir", "", "shard directory containing manifest.json")
	corpus := fs.String("corpus", "", "corpus root (defaults to the Root recorded in the manifest)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *shardDir == "" {
		return fmt.Errorf("check requires -shard-dir")
	}
	m, root, err := loadManifestAndRoot(*shardDir, *corpus)
	if err != nil {
		return err
	}
	ch, err := parity.DetectChanges(m, root, ingest.DiscoverSourceDirs, ingest.Head)
	if err != nil {
		return err
	}
	printChanges(root, ch)
	return nil
}

// ---------------------------------------------------------------------------
// refresh
// ---------------------------------------------------------------------------

func runRefresh(args []string) error {
	fs := newFlagSet("refresh")
	shardDir := fs.String("shard-dir", "", "shard directory to refresh in place")
	corpus := fs.String("corpus", "", "corpus root (defaults to the Root recorded in the manifest)")
	keepBackup := fs.Bool("keep-backup", false, "keep the pre-refresh shard dir as DIR.bak-*")
	verbose := fs.Bool("v", false, "verbose")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *shardDir == "" {
		return fmt.Errorf("refresh requires -shard-dir")
	}
	logf := mkLogf(*verbose)

	dir, err := filepath.Abs(*shardDir)
	if err != nil {
		return err
	}
	m, root, err := loadManifestAndRoot(dir, *corpus)
	if err != nil {
		return err
	}
	ch, err := parity.DetectChanges(m, root, ingest.DiscoverSourceDirs, ingest.Head)
	if err != nil {
		return err
	}
	printChanges(root, ch)
	if !ch.Any() {
		fmt.Println("shard dir is up to date; nothing to rebuild")
		return nil
	}

	stamp := time.Now().Format("20060102-150405")
	tmp := dir + ".refresh-" + stamp
	logf("rebuilding affected shards into %s", tmp)
	if _, err := parity.Rebuild(m, ch, tmp, time.Now(), ingest.Head); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("rebuild: %w", err)
	}

	// Atomic-ish swap: move the live dir aside, move the rebuilt dir into place,
	// then rewrite the manifest's recorded paths to the live dir (Rebuild recorded
	// them under the temp path).
	//
	// NOTE (known crash window, pre-existing; not fixed in this slice): this inlined-
	// format (parity.Rebuild) refresh has the SAME two-rename window as the deduped
	// delta path — a crash BETWEEN the renames leaves no live dir at `dir`, and a
	// crash AFTER the swap but BEFORE rewriteManifestPaths leaves manifest shard paths
	// pointing at the gone temp dir. The deduped delta path
	// (blobstore.RefreshDedupedShardDir) closes both windows (manifest written with
	// final paths pre-swap + RecoverInterruptedDedupedSwap on the next run); porting
	// the same recovery here is a follow-up for the inlined freshness path.
	bak := dir + ".bak-" + stamp
	if err := os.Rename(dir, bak); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("move old shard dir aside: %w", err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		// Best effort to restore the old dir so we never leave the operator
		// without a servable shard dir.
		_ = os.Rename(bak, dir)
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("swap rebuilt shard dir into place: %w", err)
	}
	if err := rewriteManifestPaths(dir); err != nil {
		return fmt.Errorf("fix up manifest paths: %w", err)
	}

	// Rebuild the token/symbol/graph sidecars on the LIVE dir (the shard set
	// changed, so any prior sidecars are now stale). Best-effort, as in build.
	sidecars := buildSidecars(dir)

	if *keepBackup {
		fmt.Printf("refreshed %s%s (previous dir kept at %s)\n", dir, sidecars, bak)
	} else {
		if err := os.RemoveAll(bak); err != nil {
			fmt.Fprintf(os.Stderr, "moedex-index: warning: could not remove backup %s: %v\n", bak, err)
		}
		fmt.Printf("refreshed %s%s\n", dir, sidecars)
	}
	fmt.Println("note: the shard set changed — moedex-serve will rebuild the dense embedding cache on next start.")
	return nil
}

// rewriteManifestPaths rebinds the manifest's ShardDir and every shard Path to
// dir, after a rebuilt dir has been renamed into place. The shard files now live
// at dir/<basename>, so a later refresh can find them for carry-forward.
func rewriteManifestPaths(dir string) error {
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

// ---------------------------------------------------------------------------
// cas-build / cas-refresh / cas-export (content-addressable store)
// ---------------------------------------------------------------------------

func runCASBuild(args []string) error {
	fs := newFlagSet("cas-build")
	corpus := fs.String("corpus", os.Getenv("MOEDEX_CORPUS"), "corpus root (every git repo beneath it is ingested)")
	casDir := fs.String("cas-dir", "", "output content-addressable store directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *corpus == "" || *casDir == "" {
		return fmt.Errorf("cas-build requires -corpus and -cas-dir")
	}
	root, err := filepath.Abs(*corpus)
	if err != nil {
		return err
	}
	dir, err := filepath.Abs(*casDir)
	if err != nil {
		return err
	}
	m, err := blobstore.BuildCAS(root, dir)
	if err != nil {
		return err
	}
	fmt.Printf("cas-build: %d repos, %d unique blobs, %d file refs\n", len(m.Repos), m.Stats.UniqueBlobs, m.Stats.FileRefs)
	fmt.Printf("  stored %.1f MB (raw %.1f MB; dedup ratio %.2fx) at %s\n",
		float64(m.Stats.StoredBytes)/1e6, float64(m.Stats.RawBytes)/1e6, m.Stats.DedupRatio(), dir)
	return nil
}

func runCASRefresh(args []string) error {
	fs := newFlagSet("cas-refresh")
	casDir := fs.String("cas-dir", "", "content-addressable store directory to refresh")
	corpus := fs.String("corpus", "", "corpus root (defaults to the Root recorded in the manifest)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *casDir == "" {
		return fmt.Errorf("cas-refresh requires -cas-dir")
	}
	dir, err := filepath.Abs(*casDir)
	if err != nil {
		return err
	}
	old, err := blobstore.LoadBlobManifest(filepath.Join(dir, blobstore.BlobManifestName))
	if err != nil {
		return fmt.Errorf("load blob manifest (run `moedex-index cas-build` first?): %w", err)
	}
	root := ""
	if *corpus != "" {
		if root, err = filepath.Abs(*corpus); err != nil {
			return err
		}
	}
	m, ds, err := blobstore.RefreshCAS(old, root, dir)
	if err != nil {
		return err
	}
	fmt.Printf("cas-refresh: changed=%d added=%d removed=%d failed=%d\n",
		len(ds.ChangedRepos), len(ds.AddedRepos), len(ds.RemovedRepos), len(ds.FailedRepos))
	fmt.Printf("  delta: +%d blobs, +%.1f MB (%d dedup no-op Puts skipped)\n",
		ds.BlobsAdded, float64(ds.BytesAdded)/1e6, ds.PutsSkipped)
	fmt.Printf("  store now: %d unique blobs, %.1f MB stored, dedup ratio %.2fx\n",
		m.Stats.UniqueBlobs, float64(m.Stats.StoredBytes)/1e6, m.Stats.DedupRatio())
	if len(ds.FailedRepos) > 0 {
		// Carried forward unchanged (no data lost); surfaced so the operator knows
		// to investigate / expect a retry next refresh.
		fmt.Fprintf(os.Stderr, "moedex-index: WARNING: %d repo(s) failed to re-ingest and were carried forward unchanged (retry next refresh):\n", len(ds.FailedRepos))
		for _, r := range ds.FailedRepos {
			fmt.Fprintf(os.Stderr, "  - %s\n", r)
		}
	}
	return nil
}

func runCASExport(args []string) error {
	fs := newFlagSet("cas-export")
	casDir := fs.String("cas-dir", "", "content-addressable store directory to export from")
	shardDir := fs.String("shard-dir", "", "output servable shard directory")
	shardBytes := fs.Int64("shard-bytes", parity.DefaultShardBytes, "target indexed-content bytes per exported shard")
	force := fs.Bool("force", false, "clear a non-empty shard dir before exporting (forces a FULL re-export even over an existing deduped dir)")
	deduped := fs.Bool("deduped", false, "write the deduped served format (content-less MOEDEX05 shards + one shared blobs.dat content store) so blob content is stored once corpus-wide")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *casDir == "" || *shardDir == "" {
		return fmt.Errorf("cas-export requires -cas-dir and -shard-dir")
	}
	dir, err := filepath.Abs(*casDir)
	if err != nil {
		return err
	}
	out, err := filepath.Abs(*shardDir)
	if err != nil {
		return err
	}

	// DELTA-AWARE deduped re-export: when -deduped is set and an existing deduped dir
	// is present (and -force was NOT given), do an incremental re-export — append
	// only net-new blob content to the existing blobs.dat and rewrite only the shards
	// whose repo set intersects the changed repos, carrying the rest forward byte-for-
	// byte. -force forces a full re-export (clears the dir first). This mirrors how
	// cas-refresh extends the CAS pack in place rather than rebuilding it; the operator
	// runs `cas-refresh` then `cas-export -deduped` and the export auto-detects whether
	// to go full or delta. The (file,line) match set is identical either way (proven by
	// the parity gate); delta just does far less work when little changed.
	//
	// First heal any interrupted prior swap so the live dir is complete before we
	// decide full-vs-delta — otherwise a crash that left the live path momentarily
	// absent would mis-route to a full export. (No-op in the normal case.)
	if *deduped {
		if err := blobstore.RecoverInterruptedDedupedSwap(out); err != nil {
			return fmt.Errorf("recover interrupted deduped swap: %w", err)
		}
	}
	if *deduped && !*force && blobstore.IsDedupedDir(out) {
		m, ds, err := blobstore.RefreshDedupedShardDir(dir, out, *shardBytes)
		if err != nil {
			return err
		}
		if len(ds.ChangedRepos) == 0 && len(ds.AddedRepos) == 0 && len(ds.RemovedRepos) == 0 {
			fmt.Printf("cas-export (deduped, DELTA): no repo changes; served dir unchanged (%d shard(s), %d repo(s))\n",
				len(m.Shards), len(m.Heads))
			return nil
		}
		// The shard set changed, so the ranking and graph sidecars in the dir are
		// stale; rebuild them (best-effort, as build/refresh do).
		sidecars := buildSidecars(out)
		fmt.Printf("cas-export (deduped, DELTA): changed=%d added=%d removed=%d; shards rewritten=%d carried=%d\n",
			len(ds.ChangedRepos), len(ds.AddedRepos), len(ds.RemovedRepos), ds.ShardsRewritten, ds.ShardsCarried)
		fmt.Printf("  appended %d net-new blob(s), %.1f MB to blobs.dat (%d dedup no-op PutContent skipped)\n",
			ds.BlobsAppended, float64(ds.BytesAppended)/1e6, ds.PutsDeduped)
		fmt.Printf("  served dir now: %d shard(s) from %d repo(s) at %s%s\n", len(m.Shards), len(m.Heads), out, sidecars)
		if ds.DenseSeedCarried {
			fmt.Println("  dense embedding reuse seed carried forward (incremental refresh enabled)")
		}
		return nil
	}

	if err := prepareDir(out, *force); err != nil {
		return err
	}
	if *deduped {
		m, storedBytes, err := blobstore.ExportDedupedShardDir(dir, out, *shardBytes)
		if err != nil {
			return err
		}
		// Build the ranking and graph sidecars so the exported dir is immediately
		// warm (both builders read the shared content store transparently).
		sidecars := buildSidecars(out)
		fmt.Printf("cas-export (deduped): %d shard(s) from %d repo(s) into %s; shared content store %d bytes%s\n",
			len(m.Shards), len(m.Heads), out, storedBytes, sidecars)
		return nil
	}
	m, err := blobstore.ExportShardDir(dir, out, *shardBytes)
	if err != nil {
		return err
	}
	// Build the ranking and graph sidecars so the exported dir is immediately
	// servable warm, mirroring build. Best-effort.
	sidecars := buildSidecars(out)
	fmt.Printf("cas-export: %d shard(s) from %d repo(s) into %s%s\n", len(m.Shards), len(m.Heads), out, sidecars)
	return nil
}

// ---------------------------------------------------------------------------
// cas-compact (compaction-GC: reclaim dead content from the append-only stores)
// ---------------------------------------------------------------------------

func runCASCompact(args []string) error {
	fs := newFlagSet("cas-compact")
	casDir := fs.String("cas-dir", "", "CAS dir to compact (rewrite blobs.pack/idx keeping only manifest-referenced blobs)")
	shardDir := fs.String("shard-dir", "", "deduped served dir to compact (rewrite blobs.dat keeping only content referenced by the live shards)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *casDir == "" && *shardDir == "" {
		return fmt.Errorf("cas-compact requires -cas-dir and/or -shard-dir")
	}

	if *casDir != "" {
		dir, err := filepath.Abs(*casDir)
		if err != nil {
			return err
		}
		st, err := blobstore.CompactCAS(dir)
		if err != nil {
			return err
		}
		fmt.Printf("cas-compact (CAS pack): kept %d live blob(s) / %.1f MB; reclaimed %d dead blob(s) / %.1f MB\n",
			st.LiveBlobs, float64(st.LiveBytes)/1e6, st.DeadBlobs, float64(st.DeadBytes)/1e6)
		fmt.Printf("  blobs.pack content: %.1f MB -> %.1f MB at %s\n",
			float64(st.BeforeBytes)/1e6, float64(st.AfterBytes)/1e6, dir)
	}

	if *shardDir != "" {
		dir, err := filepath.Abs(*shardDir)
		if err != nil {
			return err
		}
		st, err := blobstore.CompactDedupedShardDir(dir)
		if err != nil {
			return err
		}
		// The content store changed (dead content dropped), but the shards and their
		// (file,line) match set did not; ranking and graph sidecars stay valid. We do
		// NOT rebuild them — the live shard/content identities are unchanged.
		fmt.Printf("cas-compact (deduped served): kept %d live blob(s) / %.1f MB; reclaimed %d dead blob(s) / %.1f MB across %d shard(s)\n",
			st.LiveBlobs, float64(st.LiveBytes)/1e6, st.DeadBlobs, float64(st.DeadBytes)/1e6, st.Shards)
		fmt.Printf("  blobs.dat content: %.1f MB -> %.1f MB at %s\n",
			float64(st.BeforeBytes)/1e6, float64(st.AfterBytes)/1e6, dir)
	}
	return nil
}

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	return fs
}

func loadManifestAndRoot(shardDir, corpusOverride string) (*parity.Manifest, string, error) {
	m, err := parity.LoadManifest(filepath.Join(shardDir, parity.ManifestName))
	if err != nil {
		return nil, "", fmt.Errorf("load manifest (run `moedex-index build` first?): %w", err)
	}
	root := m.Root
	if corpusOverride != "" {
		abs, err := filepath.Abs(corpusOverride)
		if err != nil {
			return nil, "", err
		}
		root = abs
	}
	if root == "" {
		return nil, "", fmt.Errorf("no corpus root in manifest; pass -corpus")
	}
	return m, root, nil
}

func printChanges(root string, ch parity.Changes) {
	fmt.Printf("corpus root: %s\n", root)
	if !ch.Any() {
		fmt.Println("no changes: every indexed repo is at its recorded HEAD")
		return
	}
	report := func(label string, repos []string) {
		if len(repos) == 0 {
			return
		}
		sort.Strings(repos)
		fmt.Printf("%s (%d):\n", label, len(repos))
		for _, r := range repos {
			fmt.Printf("  %s\n", r)
		}
	}
	report("changed", ch.Changed)
	report("added", ch.Added)
	report("removed", ch.Removed)
}

func prepareDir(dir string, force bool) error {
	entries, err := os.ReadDir(dir)
	switch {
	case err == nil:
		if len(entries) > 0 {
			if !force {
				return fmt.Errorf("shard dir %s is not empty (use -force to overwrite)", dir)
			}
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
		}
	case errors.Is(err, fs.ErrNotExist):
		// Nothing there yet; MkdirAll below creates it.
	default:
		return fmt.Errorf("shard dir %s: %w", dir, err)
	}
	return os.MkdirAll(dir, 0o755)
}

func mkLogf(verbose bool) func(string, ...any) {
	if !verbose {
		return func(string, ...any) {}
	}
	return func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", a...)
	}
}
