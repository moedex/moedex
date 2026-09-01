// Package artifact owns the stable filenames shared by offline graph builders
// and online graph readers.
package artifact

import "path/filepath"

const (
	GraphFileName   = "corpus-graph.graph"
	ClusterFileName = "corpus-graph.clusters.json"
)

func GraphPath(dir string) string   { return filepath.Join(dir, GraphFileName) }
func ClusterPath(dir string) string { return filepath.Join(dir, ClusterFileName) }
