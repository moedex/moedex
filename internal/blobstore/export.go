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
	"path/filepath"

	"moedex/internal/diskstore"
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

	return exportShards(m, store, outShardDir, shardBytes, diskstore.Save)
}
