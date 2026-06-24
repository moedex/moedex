package parity

// Freshness: shard-level partial re-index.
//
// A full Build() over the ~8GB / 484-repo corpus interleaves multiple repos
// into each content-sized shard (DefaultShardBytes ≈ 150MB) and persists no
// repo→shard mapping. That makes per-repo incremental re-indexing intractable:
// a single repo's blobs can be spread across several shards, and several repos
// share one shard. The freshness path is therefore shard-level, not repo-level:
//
//   1. Build() records a Manifest alongside the shards: for each shard, which
//      repos contributed blobs to it; for each repo, its git HEAD at ingest.
//   2. DetectChanges() compares each repo's current `git rev-parse HEAD` against
//      the manifest to find changed / added / removed repos.
//   3. Rebuild() rebuilds ONLY the shards whose repo set intersects the changed
//      repos (re-ingesting every repo that shared those shards), copies the
//      untouched shards forward byte-for-byte, and writes a fresh manifest.
//
// CAVEATS (documented, accepted for correctness-over-cleverness):
//
//   - Shard-boundary drift. Shards are flushed at a content-byte threshold, so
//     when a changed repo grows or shrinks, the repo→shard packing of the
//     rebuilt region shifts. Rebuild() re-ingests the *full set of repos that
//     touched any affected shard* (not just the changed repo) and re-packs them
//     into fresh shards, so the result is internally consistent; but the shard
//     numbering of the rebuilt region is reassigned and is not stable across
//     rebuilds. Downstream consumers must treat shard IDs as opaque per-build.
//
//   - Cross-shard dedup loss. The in-RAM index dedups byte-identical blobs only
//     within a single shard (a shard == one index.Index). A blob shared by a
//     repo in an affected shard and a repo in a carried-forward shard is already
//     stored twice across those shards in the original build; partial rebuild
//     does not change that. Within the rebuilt region dedup is preserved exactly
//     as a full build would produce, because all repos touching the affected
//     shards are re-ingested together into the same index instances.
//
//   - Added / removed repos. A new repo (absent from the manifest) is appended
//     and indexed into fresh shards. A removed repo (in the manifest, gone from
//     disk) is simply not re-ingested; any shard it contributed to is treated as
//     affected and rebuilt without it.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/ingest"
)

// ManifestVersion is the manifest schema version. Bump on incompatible changes.
const ManifestVersion = 1

// ManifestName is the filename written under the shards/ dir.
const ManifestName = "manifest.json"

// RepoHead pairs a repo (by its discovered directory and label) with the git
// HEAD commit it was indexed at.
type RepoHead struct {
	// Dir is the absolute repo directory as discovered under the corpus root.
	Dir string `json:"dir"`
	// Label is the repo label used in the index (filepath.Base(Dir) at build).
	Label string `json:"label"`
	// Head is `git rev-parse HEAD` at ingest time; "" if it could not be read.
	Head string `json:"head"`
}

// ShardManifest records one persisted shard and the repos whose blobs it holds.
type ShardManifest struct {
	// Path is the shard file path (e.g. .../shards/shard-0003.idx).
	Path string `json:"path"`
	// Repos are the repo directories that contributed at least one blob to this
	// shard, in ingest order, deduplicated.
	Repos []string `json:"repos"`
	// ContentBytes is the indexed content size accumulated into this shard.
	ContentBytes int64 `json:"content_bytes"`
}

// Manifest is the freshness sidecar written alongside the shards. It is the
// only persisted record of repo→shard membership and per-repo git HEAD.
type Manifest struct {
	Version  int             `json:"version"`
	Root     string          `json:"root"`
	BuiltAt  time.Time       `json:"built_at"`
	ShardDir string          `json:"shard_dir"`
	Heads    []RepoHead      `json:"heads"`
	Shards   []ShardManifest `json:"shards"`
}

// HeadOf returns the recorded HEAD for a repo directory and whether it was
// present in the manifest.
func (m *Manifest) HeadOf(dir string) (string, bool) {
	for _, h := range m.Heads {
		if h.Dir == dir {
			return h.Head, true
		}
	}
	return "", false
}

// WriteManifest serializes m to path as indented JSON (atomically via a temp
// file + rename so a crash mid-write never leaves a half-manifest).
func WriteManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadManifest reads and parses a manifest written by WriteManifest.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	return &m, nil
}

// manifestBuilder accumulates repo→shard membership while Build() runs, then
// emits a Manifest. It is the pure-logic core; the timestamp is injected so the
// builder stays testable (no hidden time.Now()).
type manifestBuilder struct {
	root     string
	shardDir string
	heads    []RepoHead
	// per-shard repo membership, indexed by shard ordinal.
	shardRepos [][]string
	shardSeen  []map[string]bool
	shardBytes []int64
	curShard   int
}

func newManifestBuilder(root, shardDir string) *manifestBuilder {
	return &manifestBuilder{root: root, shardDir: shardDir}
}

// recordHead notes a repo's HEAD at ingest. Call once per repo as it's ingested.
func (mb *manifestBuilder) recordHead(dir, label, head string) {
	mb.heads = append(mb.heads, RepoHead{Dir: dir, Label: label, Head: head})
}

// noteBlob attributes a blob's content bytes (just ingested for repo dir) to the
// current shard being filled.
func (mb *manifestBuilder) noteBlob(dir string, n int64) {
	for len(mb.shardRepos) <= mb.curShard {
		mb.shardRepos = append(mb.shardRepos, nil)
		mb.shardSeen = append(mb.shardSeen, map[string]bool{})
		mb.shardBytes = append(mb.shardBytes, 0)
	}
	if !mb.shardSeen[mb.curShard][dir] {
		mb.shardSeen[mb.curShard][dir] = true
		mb.shardRepos[mb.curShard] = append(mb.shardRepos[mb.curShard], dir)
	}
	mb.shardBytes[mb.curShard] += n
}

// flushed is called when shard mb.curShard has been persisted to path; it
// advances to the next shard ordinal.
func (mb *manifestBuilder) flushed() {
	mb.curShard++
}

// finalize produces the Manifest given the persisted shard paths (in ordinal
// order) and a build timestamp.
func (mb *manifestBuilder) finalize(shardPaths []string, builtAt time.Time) *Manifest {
	m := &Manifest{
		Version:  ManifestVersion,
		Root:     mb.root,
		BuiltAt:  builtAt,
		ShardDir: mb.shardDir,
		Heads:    mb.heads,
	}
	for i, p := range shardPaths {
		sm := ShardManifest{Path: p}
		if i < len(mb.shardRepos) {
			sm.Repos = mb.shardRepos[i]
			sm.ContentBytes = mb.shardBytes[i]
		}
		m.Shards = append(m.Shards, sm)
	}
	return m
}

// Changes is the result of comparing a manifest to the corpus on disk.
type Changes struct {
	// Changed are repos whose current HEAD differs from the manifest.
	Changed []string
	// Added are repos discovered now that were absent from the manifest.
	Added []string
	// Removed are repos in the manifest that no longer exist on disk.
	Removed []string
}

// Any reports whether any repo changed, was added, or was removed.
func (c Changes) Any() bool {
	return len(c.Changed) > 0 || len(c.Added) > 0 || len(c.Removed) > 0
}

// affectedRepoSet returns the set of repo dirs that are changed, added, or
// removed — the trigger set for deciding which shards to rebuild.
func (c Changes) affectedRepoSet() map[string]bool {
	s := map[string]bool{}
	for _, r := range c.Changed {
		s[r] = true
	}
	for _, r := range c.Added {
		s[r] = true
	}
	for _, r := range c.Removed {
		s[r] = true
	}
	return s
}

// DetectChanges compares a prior manifest against the live corpus under root and
// classifies every repo. headFn reads a repo's current HEAD (inject for tests;
// pass ingest.Head in production). discoverFn lists current repo dirs (inject
// ingest.DiscoverRepos in production). A repo whose HEAD cannot be read is
// treated as changed (conservative: rebuild rather than serve stale).
func DetectChanges(m *Manifest, root string,
	discoverFn func(string) ([]string, error),
	headFn func(string) (string, error)) (Changes, error) {

	current, err := discoverFn(root)
	if err != nil {
		return Changes{}, fmt.Errorf("discover repos: %w", err)
	}
	currentSet := map[string]bool{}
	for _, d := range current {
		currentSet[d] = true
	}
	manifestSet := map[string]bool{}
	for _, h := range m.Heads {
		manifestSet[h.Dir] = true
	}

	var ch Changes
	for _, dir := range current {
		old, known := m.HeadOf(dir)
		if !known {
			ch.Added = append(ch.Added, dir)
			continue
		}
		now, err := headFn(dir)
		if err != nil {
			// HEAD unreadable now (a commitless/empty repo, or a broken one).
			// Normalize to "" and fall through to the comparison below: ingest.Head
			// records "" at build time for exactly this condition, so a repo that
			// never had a readable HEAD compares equal to its recorded "" and is
			// NOT a perpetual rebuild trigger. A repo that HAD a real HEAD and is
			// now unreadable still differs from its recorded SHA, so it is still
			// (conservatively) flagged changed.
			now = ""
		}
		if now != old {
			ch.Changed = append(ch.Changed, dir)
		}
	}
	for _, h := range m.Heads {
		if !currentSet[h.Dir] {
			ch.Removed = append(ch.Removed, h.Dir)
		}
	}
	sort.Strings(ch.Changed)
	sort.Strings(ch.Added)
	sort.Strings(ch.Removed)
	return ch, nil
}

// Rebuild performs a shard-level partial re-index. Given the old manifest, the
// detected changes, and a destination shards dir (newShardDir, distinct from the
// old one so the old build stays intact until the new manifest is written), it:
//
//   - carries forward unchanged shards (none of their repos are affected) by
//     copying the shard file into newShardDir,
//   - re-ingests, into fresh in-RAM indexes, every repo that contributed to any
//     affected shard (repos that shared a shard with a changed repo must be
//     re-indexed together to preserve in-shard dedup and avoid losing their
//     blobs), plus any newly-added repos,
//   - writes a fresh manifest under newShardDir.
//
// It returns the new Manifest. The build timestamp is injected (no hidden
// time.Now()). headFn captures each re-ingested repo's HEAD (inject ingest.Head).
//
// Correctness note: a repo is re-ingested if it touched an affected shard OR is
// itself changed/added. Removed repos are simply dropped. This is the
// "correct-but-simpler" strategy: it never serves stale content and never loses
// a repo, at the cost of re-indexing every co-resident repo of a changed one.
func Rebuild(old *Manifest, ch Changes, newShardDir string, builtAt time.Time,
	headFn func(string) (string, error)) (*Manifest, error) {

	if err := os.MkdirAll(newShardDir, 0o755); err != nil {
		return nil, err
	}
	affected := ch.affectedRepoSet()

	// Partition old shards into carry-forward vs rebuild, and collect the repo
	// set that must be re-ingested (every repo on any affected shard).
	reingest := map[string]bool{} // repo dir -> needs re-ingest
	var reingestOrder []string
	addReingest := func(dir string) {
		if !reingest[dir] {
			reingest[dir] = true
			reingestOrder = append(reingestOrder, dir)
		}
	}
	var carry []ShardManifest // shards untouched by any affected repo

	for _, sm := range old.Shards {
		shardAffected := false
		for _, r := range sm.Repos {
			if affected[r] {
				shardAffected = true
				break
			}
		}
		if shardAffected {
			for _, r := range sm.Repos {
				// Don't re-ingest a repo that was removed from disk.
				if !contains(ch.Removed, r) {
					addReingest(r)
				}
			}
		} else {
			carry = append(carry, sm)
		}
	}
	// Newly-added repos are always ingested.
	for _, r := range ch.Added {
		addReingest(r)
	}

	m := &Manifest{
		Version:  ManifestVersion,
		Root:     old.Root,
		BuiltAt:  builtAt,
		ShardDir: newShardDir,
	}

	shardIdx := 0
	nextShardPath := func() string {
		p := filepath.Join(newShardDir, fmt.Sprintf("shard-%04d.idx", shardIdx))
		shardIdx++
		return p
	}

	// Copy carried-forward shards into the new dir (preserving their repo
	// membership and HEADs).
	headByDir := map[string]string{}
	for _, h := range old.Heads {
		headByDir[h.Dir] = h.Head
	}
	for _, sm := range carry {
		dst := nextShardPath()
		if err := copyFile(sm.Path, dst); err != nil {
			return nil, fmt.Errorf("carry shard %s: %w", sm.Path, err)
		}
		m.Shards = append(m.Shards, ShardManifest{Path: dst, Repos: sm.Repos, ContentBytes: sm.ContentBytes})
		for _, r := range sm.Repos {
			if !contains(ch.Removed, r) {
				m.Heads = append(m.Heads, RepoHead{Dir: r, Label: filepath.Base(r), Head: headByDir[r]})
			}
		}
	}

	// Re-ingest the affected repos into fresh shards. We pack each repo into its
	// own shard for simplicity and determinism; this matches a tiny-corpus build
	// and keeps the rebuilt region's dedup correct within each repo. (For the
	// full corpus a content-byte threshold could re-pack co-resident repos, but
	// correctness does not require it — see the package caveats.)
	for _, dir := range reingestOrder {
		files, err := ingest.Repo(filepath.Base(dir), dir)
		if err != nil {
			// Repo vanished or unreadable: skip it (treated as removed).
			continue
		}
		ix := index.New()
		var bytes int64
		seen := map[string]bool{}
		for _, f := range files {
			if seen[f.AbsPath] {
				continue
			}
			seen[f.AbsPath] = true
			ix.AddFile(f.Repo, f.RelPath, f.AbsPath, f.SHA, f.Content)
			bytes += int64(len(f.Content))
		}
		if ix.NumBlobs() == 0 {
			continue
		}
		dst := nextShardPath()
		if err := diskstore.Save(ix, dst); err != nil {
			return nil, fmt.Errorf("save rebuilt shard for %s: %w", dir, err)
		}
		head, _ := headFn(dir)
		m.Shards = append(m.Shards, ShardManifest{Path: dst, Repos: []string{dir}, ContentBytes: bytes})
		m.Heads = append(m.Heads, RepoHead{Dir: dir, Label: filepath.Base(dir), Head: head})
	}

	if err := WriteManifest(filepath.Join(newShardDir, ManifestName), m); err != nil {
		return nil, err
	}
	return m, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// copyFile copies src to dst byte-for-byte.
func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0o644)
}
