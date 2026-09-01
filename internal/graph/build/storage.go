package graphbuild

import (
	"moedex/internal/diskstore"
	"moedex/internal/shardset"
)

func globShards(dir string) ([]string, error) { return shardset.Paths(dir) }

func openSharedContent(dir string, paths []string) (*diskstore.ContentStore, error) {
	return shardset.OpenSharedContent(dir, paths)
}
