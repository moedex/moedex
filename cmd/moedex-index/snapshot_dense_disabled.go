//go:build !onnx

package main

import "fmt"

func buildSnapshotDense(string) error {
	return fmt.Errorf("snapshot dense component requires a moedex-index binary built with -tags onnx")
}
