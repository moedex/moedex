package serve

import "moedex/internal/graph/artifact"

const (
	GraphFileName   = artifact.GraphFileName
	ClusterFileName = artifact.ClusterFileName
)

func GraphPath(dir string) string   { return artifact.GraphPath(dir) }
func ClusterPath(dir string) string { return artifact.ClusterPath(dir) }
