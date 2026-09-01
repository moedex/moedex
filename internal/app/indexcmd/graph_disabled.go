//go:build !onnx

package indexcmd

import graphbuild "moedex/internal/graph/build"

func buildGraph(dir string) (string, graphbuild.GraphRefreshStats, error) {
	return graphbuild.RefreshGraph(dir)
}
