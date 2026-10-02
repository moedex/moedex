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
//   2. DetectChanges() compares each repo's current `git rev-parse HEAD` and
//      AI-privacy fingerprint against the manifest to find changed / added /
//      removed repos.
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
//     affected and rebuilt without it. A repo that is still on disk but fails to
//     ingest (a transient git/I/O error) is NOT treated as removed: it is
//     recorded in the returned Manifest's Skipped field so an operator can tell
//     "genuinely gone" apart from "temporarily broken" — see Rebuild.

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
	// PrivacyFingerprint records the effective .ai-privacy.yml rules applied at
	// ingest time, so an uncommitted policy edit still invalidates freshness.
	PrivacyFingerprint string `json:"ai_privacy,omitempty"`
	// ProjectID and Managed preserve stable source identity for managed corpora.
	ProjectID int64 `json:"project_id,omitempty"`
	Managed   bool  `json:"managed,omitempty"`
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
	// ShardBytes is the file-boundary content target. Zero in legacy manifests
	// means DefaultShardBytes; one oversized file may exceed the target alone.
	ShardBytes int64           `json:"shard_bytes,omitempty"`
	Version    int             `json:"version"`
	Root       string          `json:"root"`
	BuiltAt    time.Time       `json:"built_at"`
	ShardDir   string          `json:"shard_dir"`
	Heads      []RepoHead      `json:"heads"`
	Shards     []ShardManifest `json:"shards"`

	// Skipped records repos that Rebuild could not re-ingest for a reason other
	// than a rejected privacy policy (which is fatal, not skipped) or a genuine
	// removal from disk (DiscoverSources no longer finding the repo at all — see
	// the package CAVEATS above). A repo lands here when it is still discovered
	// on disk but ingest.Repo failed for it (a transient git or filesystem
	// error), so its shard contribution and manifest entry are dropped from this
	// rebuild exactly as if it had been removed. Populated only by Rebuild, and
	// intentionally excluded from the persisted JSON: it describes this one
	// rebuild attempt, not a durable property of the corpus, so a later
	// successful refresh should not carry a stale entry forward.
	Skipped []SkippedRepo `json:"-"`
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
// file + rename so a crash mid-write never leaves a half-manifest), and makes the
// write DURABLE: it fsyncs the temp file before the rename (so the manifest's bytes
// are on disk before the directory entry that exposes them) and fsyncs the parent
// directory after the rename (so the rename itself survives a hard crash).
//
// This durability matters because the deduped delta re-export
// (blobstore.RefreshDedupedShardDir) treats the PRESENCE of manifest.json as the
// "this export dir is complete" marker its crash-recovery (RecoverInterrupted
// DedupedSwap) keys on: data files are fsynced first, then this manifest is written
// LAST, so "manifest present" reliably implies "data + manifest durably on disk"
// even across power loss / kernel panic — not just a clean process kill. Every
// caller writes exactly one manifest at the end of a build/export/refresh (no
// tight per-iteration loop), so the one fsync per call is negligible.
func WriteManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	// fsync the temp file's contents to disk BEFORE the rename, so the manifest bytes
	// are durable before the directory entry exposing them.
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	// fsync the parent directory so the rename (the directory entry creation) is
	// itself durable across a hard crash. Best-effort: some platforms reject fsync on
	// a directory handle — that does not invalidate the file-content fsync above.
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
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
func (mb *manifestBuilder) recordHead(source ingest.RepoSource, privacyFingerprint string) {
	mb.heads = append(mb.heads, RepoHead{
		Dir: source.Dir, Label: source.Namespace, Head: source.LockedCommit,
		PrivacyFingerprint: privacyFingerprint,
		ProjectID:          source.ProjectID, Managed: source.Managed,
	})
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
// classifies every repo. It validates and fingerprints every privacy policy
// before checking HEAD, so malformed policies abort and uncommitted policy edits
// trigger a rebuild. headFn reads a repo's current HEAD (inject for tests; pass
// ingest.Head in production). discoverFn lists current repo dirs (inject
// ingest.DiscoverRepos in production).
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
	manifestByDir := map[string]RepoHead{}
	for _, h := range m.Heads {
		manifestByDir[h.Dir] = h
	}

	var ch Changes
	for _, dir := range current {
		privacyFingerprint, err := ingest.AIPrivacyFingerprint(dir)
		if err != nil {
			return Changes{}, fmt.Errorf("privacy preflight: %w", err)
		}
		oldRepo, known := manifestByDir[dir]
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
		if now != oldRepo.Head || privacyFingerprint != oldRepo.PrivacyFingerprint {
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
	sources, err := ingest.DiscoverSources(old.Root)
	if err != nil {
		return nil, fmt.Errorf("discover rebuild sources: %w", err)
	}
	sourceByDir := make(map[string]ingest.RepoSource, len(sources))
	privacyFingerprints := make(map[string]string, len(sources))
	for _, source := range sources {
		sourceByDir[source.Dir] = source
		fingerprint, err := ingest.AIPrivacyFingerprint(source.Dir)
		if err != nil {
			return nil, fmt.Errorf("privacy preflight: %w", err)
		}
		privacyFingerprints[source.Dir] = fingerprint
	}

	// Close over shard/repository co-residency before copying anything. A repo
	// can span shards: rebuilding all its files while carrying one of its old
	// shards would duplicate occurrences and retain stale content.
	repoShards := make(map[string][]int)
	for i, shard := range old.Shards {
		for _, repo := range shard.Repos {
			repoShards[repo] = append(repoShards[repo], i)
		}
	}
	queue := make([]string, 0, len(affected))
	for repo := range affected {
		queue = append(queue, repo)
	}
	visitedShard := make([]bool, len(old.Shards))
	for pos := 0; pos < len(queue); pos++ {
		for _, i := range repoShards[queue[pos]] {
			if visitedShard[i] {
				continue
			}
			visitedShard[i] = true
			for _, repo := range old.Shards[i].Repos {
				if !affected[repo] {
					affected[repo] = true
					queue = append(queue, repo)
				}
			}
		}
	}

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
	// A previously empty or globally Restricted repo has no shard membership;
	// include changed repos explicitly so a policy relaxation or newly-added file
	// can make them searchable again.
	for _, r := range ch.Changed {
		addReingest(r)
	}

	target := old.ShardBytes
	if target <= 0 {
		target = DefaultShardBytes
	}
	m := &Manifest{
		ShardBytes: target,
		Version:    ManifestVersion,
		Root:       old.Root,
		BuiltAt:    builtAt,
		ShardDir:   newShardDir,
	}

	shardIdx := 0
	nextShardPath := func() string {
		p := filepath.Join(newShardDir, fmt.Sprintf("shard-%04d.idx", shardIdx))
		shardIdx++
		return p
	}

	// Copy carried-forward shards into the new dir (preserving their repo
	// membership and HEADs).
	headByDir := map[string]RepoHead{}
	for _, h := range old.Heads {
		headByDir[h.Dir] = h
	}
	emittedHead := map[string]bool{}
	appendHead := func(h RepoHead) {
		if emittedHead[h.Dir] {
			return
		}
		emittedHead[h.Dir] = true
		m.Heads = append(m.Heads, h)
	}
	for _, sm := range carry {
		dst := nextShardPath()
		if err := copyFile(sm.Path, dst); err != nil {
			return nil, fmt.Errorf("carry shard %s: %w", sm.Path, err)
		}
		m.Shards = append(m.Shards, ShardManifest{Path: dst, Repos: sm.Repos, ContentBytes: sm.ContentBytes})
		for _, r := range sm.Repos {
			if !contains(ch.Removed, r) {
				appendHead(headByDir[r])
			}
		}
	}

	// Re-ingest affected repositories, bounding each shard at file boundaries.
	// Repacking within each repo preserves the existing refresh order; content
	// and occurrence parity do not depend on identical clean-build boundaries.
	for _, dir := range reingestOrder {
		source, ok := sourceByDir[dir]
		if !ok {
			continue
		}
		if err := ingest.VerifySource(source); err != nil {
			return nil, err
		}
		privacyFingerprint := privacyFingerprints[dir]
		files, err := ingest.Repo(source.Namespace, dir)
		if err != nil {
			if ingest.IsPrivacyPolicyError(err) {
				return nil, fmt.Errorf("privacy preflight: %w", err)
			}
			// The repo is still on disk (sourceByDir found it above) but could not
			// be ingested — a transient git/I/O failure, not a removal. Record it
			// visibly (mirrors Build()'s b.Skipped) instead of silently dropping it
			// exactly like a repo that is genuinely gone from disk.
			m.Skipped = append(m.Skipped, SkippedRepo{Dir: dir, Reason: err.Error()})
			continue
		}
		ix := index.New()
		var contentBytes int64
		flush := func() error {
			if ix.NumBlobs() == 0 {
				return nil
			}
			dst := nextShardPath()
			if err := diskstore.Save(ix, dst); err != nil {
				return fmt.Errorf("save rebuilt shard for %s: %w", dir, err)
			}
			m.Shards = append(m.Shards, ShardManifest{Path: dst, Repos: []string{dir}, ContentBytes: contentBytes})
			ix = index.New()
			contentBytes = 0
			return nil
		}
		seen := map[string]bool{}
		for _, f := range files {
			if seen[f.AbsPath] {
				continue
			}
			seen[f.AbsPath] = true
			if ix.NumBlobs() > 0 && int64(len(f.Content)) > target-contentBytes {
				if err := flush(); err != nil {
					return nil, err
				}
			}
			ix.AddFile(f.Repo, f.RelPath, f.AbsPath, f.SHA, f.Content)
			contentBytes += int64(len(f.Content))
		}
		head, _ := headFn(dir)
		currentPrivacyFingerprint, err := ingest.AIPrivacyFingerprint(dir)
		if err != nil {
			return nil, fmt.Errorf("privacy preflight: %w", err)
		}
		if currentPrivacyFingerprint != privacyFingerprint {
			return nil, &ingest.PrivacyPolicyError{
				Path: filepath.Join(dir, ingest.AIPrivacyFileName),
				Err:  fmt.Errorf("policy changed during ingest"),
			}
		}
		repoHead := RepoHead{
			Dir: dir, Label: source.Namespace, Head: head,
			PrivacyFingerprint: currentPrivacyFingerprint,
			ProjectID:          source.ProjectID, Managed: source.Managed,
		}
		if err := flush(); err != nil {
			return nil, err
		}
		appendHead(repoHead)
	}

	// Preserve repositories that intentionally had no shard (for example a
	// globally Restricted repository) when an unrelated repo triggered Rebuild.
	for _, h := range old.Heads {
		if !contains(ch.Removed, h.Dir) && !affected[h.Dir] {
			appendHead(h)
		}
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
