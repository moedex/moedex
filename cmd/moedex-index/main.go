// Command moedex-index builds and refreshes the servable shard directory the
// retrieval daemon (moedex-serve) consumes. It is the offline indexing/freshness
// tool — the daemon itself only reads shards; this command produces and updates
// them.
//
// A servable shard dir is a flat directory of shard-NNNN.idx files plus a
// manifest.json freshness sidecar (repo->shard membership + each repo's git HEAD
// at ingest). Three subcommands:
//
//		moedex-index build   -corpus ROOT -shard-dir DIR [-shard-bytes N] [-force]
//		moedex-index check   -shard-dir DIR [-corpus ROOT]
//		moedex-index refresh -shard-dir DIR [-corpus ROOT] [-keep-backup]
//
//	  - build   indexes every git repo under -corpus into byte-sized shards and
//	            writes the manifest. The result is directly servable
//	            (moedex-serve -shard-dir DIR).
//	  - check   compares the manifest against the corpus on disk (each repo's
//	            current `git rev-parse HEAD`) and prints changed/added/removed
//	            repos. Read-only; never mutates the shard dir.
//	  - refresh runs check, then if anything changed rebuilds only the affected
//	            shards into a fresh dir and atomically swaps it into place (the old
//	            dir is kept as DIR.bak-* unless -keep-backup is omitted).
//
// The corpus root for check/refresh defaults to the Root recorded in the
// manifest, so an operator (or cron) only needs the shard dir; -corpus overrides
// it (e.g. if the corpus moved).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/ingest"
	"moedex/internal/parity"
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

	m, nShards, nFiles, err := buildShards(root, *shardDir, *shardBytes, logf)
	if err != nil {
		return err
	}
	if err := parity.WriteManifest(filepath.Join(*shardDir, parity.ManifestName), m); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	fmt.Printf("built %d shard(s), %d file(s) from %d repo(s) into %s\n", nShards, nFiles, len(m.Heads), *shardDir)
	return nil
}

// buildShards indexes every repo under root into byte-sized shards written to
// shardDir, and returns the freshness Manifest describing them. Packing matches
// parity.Build: a shard is flushed once its accumulated content reaches
// shardBytes (checked at repo boundaries, so a repo is never split across
// shards). Repos that fail to ingest are skipped, not fatal.
func buildShards(root, shardDir string, shardBytes int64, logf func(string, ...any)) (*parity.Manifest, int, int, error) {
	if shardBytes <= 0 {
		shardBytes = parity.DefaultShardBytes
	}
	repos, err := ingest.DiscoverRepos(root)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("discover repos: %w", err)
	}
	logf("discovered %d repos under %s", len(repos), root)

	var (
		shards   []parity.ShardManifest
		heads    []parity.RepoHead
		ix       = index.New()
		curBytes int64
		curRepos []string
		curSeen  = map[string]bool{}
		shardIdx int
		nFiles   int
	)
	flush := func() error {
		if ix.NumBlobs() == 0 {
			return nil
		}
		path := filepath.Join(shardDir, fmt.Sprintf("shard-%04d.idx", shardIdx))
		if err := diskstore.Save(ix, path); err != nil {
			return fmt.Errorf("save shard %d: %w", shardIdx, err)
		}
		shards = append(shards, parity.ShardManifest{Path: path, Repos: curRepos, ContentBytes: curBytes})
		logf("  flushed shard %d: %d blobs, %.1f MB", shardIdx, ix.NumBlobs(), float64(curBytes)/1e6)
		shardIdx++
		ix = index.New()
		curBytes = 0
		curRepos = nil
		curSeen = map[string]bool{}
		runtime.GC() // release the builder's posting map before the next shard
		return nil
	}

	for _, repo := range repos {
		files, err := ingest.Repo(filepath.Base(repo), repo)
		if err != nil {
			logf("  skip %s: %v", repo, err)
			continue
		}
		head, _ := ingest.Head(repo) // "" if unreadable; recorded as-is
		heads = append(heads, parity.RepoHead{Dir: repo, Label: filepath.Base(repo), Head: head})

		contributed := false
		seen := map[string]bool{}
		for _, f := range files {
			if seen[f.AbsPath] {
				continue // same abspath already in this repo's batch
			}
			seen[f.AbsPath] = true
			ix.AddFile(f.Repo, f.RelPath, f.AbsPath, f.SHA, f.Content)
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
	ch, err := parity.DetectChanges(m, root, ingest.DiscoverRepos, ingest.Head)
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
	ch, err := parity.DetectChanges(m, root, ingest.DiscoverRepos, ingest.Head)
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

	if *keepBackup {
		fmt.Printf("refreshed %s (previous dir kept at %s)\n", dir, bak)
	} else {
		if err := os.RemoveAll(bak); err != nil {
			fmt.Fprintf(os.Stderr, "moedex-index: warning: could not remove backup %s: %v\n", bak, err)
		}
		fmt.Printf("refreshed %s\n", dir)
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
	if err == nil && len(entries) > 0 {
		if !force {
			return fmt.Errorf("shard dir %s is not empty (use -force to overwrite)", dir)
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
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
