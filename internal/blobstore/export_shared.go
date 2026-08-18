package blobstore

// export_shared.go holds the repo-to-shard packing driver shared by
// ExportShardDir (export.go) and ExportDedupedShardDir (export_deduped.go).
// Both exports replay the SAME manifest in the SAME order through the SAME
// byte-bucketing rule; the only thing that differs between them is HOW a
// flushed index is persisted (inlined MOEDEX03/04 vs content-less MOEDEX05 +
// shared content store). Factoring that one difference out as the saveShard
// callback keeps the two exports' shard packing identical by construction
// instead of by two hand-kept-in-sync copies of the same loop (see
// CODE_REVIEW_2026-06-30.md F-043, and the regression in
// export_packing_test.go that pins the two packings as identical).
//
// exportShards itself is staging-agnostic: it writes shard files under a
// caller-chosen writeDir but binds the returned manifest's paths to a
// separate recordedDir, and never touches disk for the manifest itself. This
// lets ExportShardDir / ExportDedupedShardDir stage a full export under a
// sibling temp dir and swap it into the live location atomically on success —
// see the crash-safe-swap doc in export.go / export_deduped.go — instead of
// writing directly into the live shard dir the way this file did before that
// fix (CODEBASE-REVIEW.md F-13).

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"moedex/internal/index"
	"moedex/internal/parity"
)

// exportShards walks m's repos in manifest order, replays each repo's recorded
// (sha, rel) file entries through index.AddFile, and flushes byte-bucketed
// shards (checked at repo boundaries, so a repo is never split across shards)
// via saveShard. saveShard persists the just-flushed index to a PHYSICAL path
// under writeDir; the returned manifest's ShardDir and each shard's Path are
// bound to recordedDir instead, so a caller staging the export under a sibling
// temp dir (writeDir, e.g. outShardDir+".export-<stamp>") gets back a manifest
// that is already correct for the FINAL live location (recordedDir) — no
// post-swap rewrite needed, mirroring RefreshDedupedShardDir/CompactCAS/
// CompactDedupedShardDir. A caller that wants no staging can simply pass the
// same directory for both.
//
// exportShards does NOT write the manifest to disk — it only returns it.
// Callers write it LAST (the completeness marker), after fsyncing the staged
// directory's other data files, so a crash before the manifest is durable
// never leaves a directory that looks complete but is missing content the
// manifest's shards would reference.
//
// Pass shardBytes<=0 for parity.DefaultShardBytes.
func exportShards(m *BlobManifest, store *Store, writeDir, recordedDir string, shardBytes int64, saveShard func(ix *index.Index, path string) error) (*parity.Manifest, error) {
	if shardBytes <= 0 {
		shardBytes = parity.DefaultShardBytes
	}
	if err := os.MkdirAll(writeDir, 0o755); err != nil {
		return nil, err
	}

	var (
		shards   []parity.ShardManifest
		heads    []parity.RepoHead
		ix       = index.New()
		curBytes int64
		curRepos []string
		curSeen  = map[string]bool{}
		shardIdx int
	)
	flush := func() error {
		if ix.NumBlobs() == 0 {
			return nil
		}
		name := fmt.Sprintf("shard-%04d.idx", shardIdx)
		physical := filepath.Join(writeDir, name)
		recorded := filepath.Join(recordedDir, name)
		if err := saveShard(ix, physical); err != nil {
			return fmt.Errorf("blobstore: save exported shard %d: %w", shardIdx, err)
		}
		shards = append(shards, parity.ShardManifest{Path: recorded, Repos: curRepos, ContentBytes: curBytes})
		shardIdx++
		ix = index.New()
		curBytes = 0
		curRepos = nil
		curSeen = map[string]bool{}
		return nil
	}

	for _, r := range m.Repos {
		heads = append(heads, parity.RepoHead{
			Dir: r.Dir, Label: r.Label, Head: r.Head,
			PrivacyFingerprint: r.PrivacyFingerprint,
			ProjectID:          r.ProjectID, Managed: r.Managed,
		})
		contributed := false
		seen := map[string]bool{}
		for _, f := range r.Files {
			abs := filepath.Join(r.Dir, f.RelPath)
			if seen[abs] {
				continue // same abspath already in this repo's batch (matches build)
			}
			seen[abs] = true
			content, err := store.Get(f.SHA)
			if err != nil {
				return nil, fmt.Errorf("blobstore: export repo %s file %s: %w", r.Label, f.RelPath, err)
			}
			ix.AddFile(r.Label, f.RelPath, abs, f.SHA, content)
			curBytes += int64(len(content))
			contributed = true
		}
		if contributed && !curSeen[r.Dir] {
			curSeen[r.Dir] = true
			curRepos = append(curRepos, r.Dir)
		}
		if curBytes >= shardBytes {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}

	out := &parity.Manifest{
		Version:  parity.ManifestVersion,
		Root:     m.Root,
		BuiltAt:  time.Now(),
		ShardDir: recordedDir,
		Heads:    heads,
		Shards:   shards,
	}
	return out, nil
}
