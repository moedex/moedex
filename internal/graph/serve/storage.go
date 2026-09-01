package graphserve

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"moedex/internal/diskstore"
	"moedex/internal/graph/artifact"
	"moedex/internal/shardset"
)

const (
	GraphFileName   = artifact.GraphFileName
	ClusterFileName = artifact.ClusterFileName
)

func GraphPath(dir string) string   { return artifact.GraphPath(dir) }
func ClusterPath(dir string) string { return artifact.ClusterPath(dir) }

func openSharedContent(dir string, paths []string) (*diskstore.ContentStore, error) {
	return shardset.OpenSharedContent(dir, paths)
}

func corpusFingerprint(shardPaths []string) string {
	hash := sha256.New()
	for _, path := range shardPaths {
		size := int64(-1)
		if info, err := os.Stat(path); err == nil {
			size = info.Size()
		}
		fmt.Fprintf(hash, "%s:%d\n", filepath.Base(path), size)
	}
	if len(shardPaths) > 0 {
		path := filepath.Join(filepath.Dir(shardPaths[0]), diskstore.ContentStoreName)
		if info, err := os.Stat(path); err == nil {
			fmt.Fprintf(hash, "%s:%d\n", diskstore.ContentStoreName, info.Size())
			if digest := diskstore.ContentStoreDirDigest(path); digest != "" {
				fmt.Fprintf(hash, "dir:%s\n", digest)
			}
		}
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}
