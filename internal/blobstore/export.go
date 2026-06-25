package blobstore

// ExportShardDir is the parity-preserving bridge: it materializes a
// today-compatible servable *.idx shard dir + parity.Manifest FROM the CAS, so
// the existing daemon (moedex-serve), server.Open/OpenRank, and the full-corpus
// parity harness consume it with ZERO changes. This is what keeps ripgrep parity
// and the warm serving spine 100% working while the new storage layer (global
// dedup + per-blob delta) is validated independently.
//
// SLICE-1 BOUNDARY (documented, accepted): the CAS itself stores each blob ONCE
// (full global cross-shard dedup), but the EXPORTED shard dir still inlines a
// blob's content into every shard that holds a repo carrying it — exactly as a
// direct parity build does today — because the export streams blobs back through
// the unchanged index.AddFile / diskstore.Save pipeline (which dedups only within
// a single shard). The retrieval path's switch to reference CAS blobs by SHA (so
// served shards also dedup) is the explicit NEXT slice. The dedup + delta win is
// fully realized at the storage layer; the export is a migration seam, not the
// final served format.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/parity"
)

// ExportShardDir reads the CAS + BlobManifest at casDir and writes a servable
// shard dir to outShardDir: byte-bucketed *.idx shards (flushed at shardBytes of
// accumulated content, checked at repo boundaries so a repo is never split) plus
// a parity.Manifest. It replays each repo's recorded file entries (sha, rel) in
// manifest repo order through index.AddFile — the SAME (sha, content, repo, rel,
// abs) tuples a direct parity build would index — so within-shard dedup, content
// bytes, and FileRefs are byte-equivalent to a direct build. Pass shardBytes<=0
// for parity.DefaultShardBytes.
//
// The export is deterministic: repos are processed in manifest order (which is
// DiscoverRepos' sorted order at build/refresh time), and a repo's files in
// recorded order, so shard contents are reproducible across exports of the same
// manifest. Shard IDs remain opaque per-export (same caveat as parity.Manifest).
func ExportShardDir(casDir, outShardDir string, shardBytes int64) (*parity.Manifest, error) {
	m, err := LoadBlobManifest(filepath.Join(casDir, BlobManifestName))
	if err != nil {
		return nil, fmt.Errorf("blobstore: load blob manifest: %w", err)
	}
	store, err := Open(casDir)
	if err != nil {
		return nil, err
	}
	defer store.Close()

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
		if err := diskstore.Save(ix, path); err != nil {
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
		heads = append(heads, parity.RepoHead{Dir: r.Dir, Label: r.Label, Head: r.Head})
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
