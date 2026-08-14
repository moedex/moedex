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
// via saveShard. saveShard persists the just-flushed index to path; it is the
// only step that differs between ExportShardDir (diskstore.Save) and
// ExportDedupedShardDir (diskstore.SaveDeduped against a shared content-store
// sink). Pass shardBytes<=0 for parity.DefaultShardBytes.
func exportShards(m *BlobManifest, store *Store, outShardDir string, shardBytes int64, saveShard func(ix *index.Index, path string) error) (*parity.Manifest, error) {
	if shardBytes <= 0 {
		shardBytes = parity.DefaultShardBytes
	}
	if err := os.MkdirAll(outShardDir, 0o755); err != nil {
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
		path := filepath.Join(outShardDir, fmt.Sprintf("shard-%04d.idx", shardIdx))
		if err := saveShard(ix, path); err != nil {
			return fmt.Errorf("blobstore: save exported shard %d: %w", shardIdx, err)
		}
		shards = append(shards, parity.ShardManifest{Path: path, Repos: curRepos, ContentBytes: curBytes})
		shardIdx++
		ix = index.New()
		curBytes = 0
		curRepos = nil
		curSeen = map[string]bool{}
		return nil
	}

	for _, r := range m.Repos {
		heads = append(heads, parity.RepoHead{Dir: r.Dir, Label: r.Label, Head: r.Head, ProjectID: r.ProjectID, Managed: r.Managed})
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
		ShardDir: outShardDir,
		Heads:    heads,
		Shards:   shards,
	}
	if err := parity.WriteManifest(filepath.Join(outShardDir, parity.ManifestName), out); err != nil {
		return nil, fmt.Errorf("blobstore: write exported manifest: %w", err)
	}
	return out, nil
}
