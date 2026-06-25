package server

// dedup.go is the serving-spine seam for the DEDUPED served shard format
// (MOEDEX05 + a shared content store, written by blobstore.ExportDedupedShardDir).
//
// A deduped dir stores each unique blob's content ONCE in a shared content store
// (diskstore.ContentStoreName / blobs.dat); the *.idx shards carry only postings +
// per-blob content-hash references + file refs. server.Corpus and server.RankCorpus
// open that shared store ONCE for the dir and resolve every shard's blob content as
// zero-copy sub-slices of its mmap — so a blob whose repos span several shards is
// mapped once for the whole served corpus (the CAS's cross-shard dedup, now on the
// served side) and content stays off the Go heap.
//
// Format detection is per dir and conservative: the shared store is used only when
// it is present AND parses. A legacy inlined-content dir (MOEDEX03/04, no blobs.dat)
// loads exactly as before. The external API (Open / OpenRank signatures) is unchanged.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"moedex/internal/diskstore"
	"moedex/internal/index"
)

// openSharedContent opens the shared content store for a deduped dir, or returns
// (nil, nil) for a legacy dir. It is "deduped" iff blobs.dat is present; in that
// case every *.idx shard MUST be MOEDEX05 (a dir mixing formats is a build bug, not
// a supported state) — a non-deduped shard alongside a shared store is reported as
// an error rather than silently mis-loaded. A present-but-unparseable store is also
// an error (the shards reference it; loading them without it would drop content).
func openSharedContent(dir string, shardPaths []string) (*diskstore.ContentStore, error) {
	csPath := filepath.Join(dir, diskstore.ContentStoreName)
	if _, err := os.Stat(csPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil // legacy inlined-content dir
		}
		return nil, fmt.Errorf("server: stat shared content store: %w", err)
	}
	cs, err := diskstore.OpenContentStore(csPath)
	if err != nil {
		return nil, fmt.Errorf("server: open shared content store: %w", err)
	}
	for _, p := range shardPaths {
		if !diskstore.IsDeduped(p) {
			cs.Close()
			return nil, fmt.Errorf("server: shard %s is not deduped but dir has a shared content store %s", p, csPath)
		}
	}
	return cs, nil
}

// openShard loads one shard for the retrieval Corpus, dispatching on format: a
// deduped (MOEDEX05) shard resolves content from cs (which must be non-nil for a
// deduped dir); a legacy shard loads via diskstore.LoadMmap. Both keep postings
// mmap'd off the heap.
func openShard(path string, cs *diskstore.ContentStore) (*index.Index, io.Closer, error) {
	if cs != nil && diskstore.IsDeduped(path) {
		ix, region, err := diskstore.LoadMmapDeduped(path, cs)
		if err != nil {
			return nil, nil, err
		}
		return ix, region, nil
	}
	return diskstore.LoadMmap(path)
}
