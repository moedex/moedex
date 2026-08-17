//go:build !onnx

package main

import "moedex/internal/server"

func buildGraphSidecar(dir string) (string, error) {
	return server.BuildGraphSidecar(dir)
}
