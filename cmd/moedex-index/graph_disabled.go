//go:build !onnx

package main

import "moedex/internal/server"

func buildGraph(dir string) (string, server.GraphRefreshStats, error) {
	return server.RefreshGraph(dir)
}
