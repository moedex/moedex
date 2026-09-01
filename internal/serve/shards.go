package serve

import "moedex/internal/shardset"

func globShards(dir string) ([]string, error) { return shardset.Paths(dir) }
