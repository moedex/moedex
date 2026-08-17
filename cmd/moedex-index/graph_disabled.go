//go:build !onnx

package main

import "moedex/internal/server"

func buildGraphSidecar(dir string) (string, server.GraphRefreshStats, error) {
	return server.RefreshGraphSidecar(dir)
}
