//go:build !onnx

// Default build: the in-process ONNX embedder is NOT compiled, so the core has
// no dependency on the model or the ONNX Runtime binding. NewONNXEmbedder returns
// a clear error telling the caller to rebuild with -tags onnx. See onnx.go for
// the real implementation.

package embed

import (
	"context"
	"errors"
)

// errNoONNX is returned by every method when the binary was built without the
// onnx tag.
var errNoONNX = errors.New("embed: in-process embedder unavailable; rebuild with -tags onnx")

// ONNXEmbedder is a stub in the default build. The onnx-tagged build replaces it
// with a real all-MiniLM-L6-v2 embedder.
type ONNXEmbedder struct{}

// NewONNXEmbedder always fails in the default build.
func NewONNXEmbedder(runtimePath string) (*ONNXEmbedder, error) { return nil, errNoONNX }

func (*ONNXEmbedder) Embed(context.Context, []string) ([]Vector, error) { return nil, errNoONNX }
func (*ONNXEmbedder) Dim() int                                          { return 0 }
func (*ONNXEmbedder) Close() error                                      { return nil }
