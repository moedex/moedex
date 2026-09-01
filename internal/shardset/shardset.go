// Package shardset owns the neutral on-disk shard opening seam shared by
// retrieval, offline graph construction, and online graph queries.
package shardset

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"moedex/internal/diskstore"
)

// Paths returns the sorted, non-empty *.idx set under dir.
func Paths(dir string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.idx"))
	if err != nil {
		return nil, fmt.Errorf("shardset: glob shards: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("shardset: no *.idx shards under %s", dir)
	}
	sort.Strings(paths)
	return paths, nil
}

// OpenSharedContent opens the content store for a uniformly deduped shard set,
// or returns nil for a legacy inlined-content set.
func OpenSharedContent(dir string, shardPaths []string) (*diskstore.ContentStore, error) {
	path := filepath.Join(dir, diskstore.ContentStoreName)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("shardset: stat shared content store: %w", err)
	}
	store, err := diskstore.OpenContentStoreVerified(path, contentHasher())
	if err != nil {
		return nil, fmt.Errorf("shardset: open shared content store: %w", err)
	}
	for _, shardPath := range shardPaths {
		if !diskstore.IsDeduped(shardPath) {
			_ = store.Close()
			return nil, fmt.Errorf("shardset: shard %s is not deduped but dir has shared content store %s", shardPath, path)
		}
	}
	return store, nil
}

func contentHasher() func([]byte) string {
	switch strings.ToLower(os.Getenv("MOEDEX_VERIFY_CONTENT")) {
	case "0", "false", "no", "off":
		return nil
	default:
		return diskstore.GitBlobSHA1
	}
}
