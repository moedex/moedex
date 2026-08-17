//go:build !onnx

package main

import "moedex/internal/server"

func buildGraphSidecar(dir string) (string, error) {
	path, _, err := server.BuildGraphSidecar(dir)
	return path, err
}
