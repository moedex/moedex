package server

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"moedex/internal/diskstore"
	"moedex/internal/embed"
)

// ShardDirInfo is a cheap, read-only snapshot of a servable shard dir for
// `moedex-index doctor`: enough to name the layout, pick the correct refresh
// command, and tell whether the dense sidecar is fresh — WITHOUT loading the
// corpus (no mmap of blob content or vectors).
type ShardDirInfo struct {
	Dir    string
	Shards int  // number of *.idx shards
	Deduped bool // shards are content-less MOEDEX05 (need a shared blobs.dat)

	HasBlobsDat     bool // shared deduped content store (blobs.dat) present
	HasManifest     bool // manifest.json — standard build dir (refresh -shard-dir)
	HasBlobManifest bool // blobmanifest.json — CAS-exported dir (cas-refresh -cas-dir)

	// Dense embedding sidecar.
	StoreExists      bool
	StoreMetaPresent bool
	StoreVersion     uint32 // 1 = legacy keyless, 2 = incremental-ready
	StoreChunks      int
	StoreModel       string
	StoreFresh       bool // store meta fingerprint == the current shard set
}

// RefreshCommand returns the refresh subcommand that matches this dir's layout, so
// doctor can state it explicitly (the incident was running the wrong one).
func (i ShardDirInfo) RefreshCommand() string {
	if i.HasBlobManifest {
		return "moedex-index cas-refresh -cas-dir " + i.Dir
	}
	return "moedex-index refresh -shard-dir " + i.Dir
}

// InspectShardDir gathers ShardDirInfo for dir. It returns an error only when the
// dir cannot be read or holds no shards (an undetectable layout — the dangerous
// case doctor must surface, not guess past).
func InspectShardDir(dir string) (ShardDirInfo, error) {
	info := ShardDirInfo{Dir: dir}
	paths, err := filepath.Glob(filepath.Join(dir, "*.idx"))
	if err != nil {
		return info, fmt.Errorf("glob shards: %w", err)
	}
	sort.Strings(paths)
	info.Shards = len(paths)
	if info.Shards == 0 {
		return info, fmt.Errorf("no *.idx shards under %s", dir)
	}
	info.Deduped = diskstore.IsDeduped(paths[0])

	statExists := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	info.HasBlobsDat = statExists(diskstore.ContentStoreName)
	info.HasManifest = statExists("manifest.json")
	info.HasBlobManifest = statExists("blobmanifest.json")

	storePath := filepath.Join(dir, "corpus-embeddings.store")
	if hdr, err := embed.PeekStore(storePath); err == nil {
		info.StoreExists = true
		info.StoreVersion = hdr.Version
		info.StoreChunks = hdr.Count
		if m, ok := readStoreMeta(storePath); ok {
			info.StoreMetaPresent = true
			info.StoreModel = m.Model
			info.StoreFresh = m.Fingerprint == corpusFingerprint(paths)
		}
	}
	return info, nil
}
