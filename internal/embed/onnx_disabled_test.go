//go:build !onnx

package embed

import "testing"

// TestNewONNXEmbedderFromFilesDisabled pins the twin-pair contract between
// onnx.go and onnx_disabled.go: every exported constructor in the real
// (-tags onnx) build must have a matching stub in the default build. Without
// the stub, any !onnx caller of embed.NewONNXEmbedderFromFiles fails to
// compile rather than getting a clear runtime error.
func TestNewONNXEmbedderFromFilesDisabled(t *testing.T) {
	e, err := NewONNXEmbedderFromFiles("", "model.onnx", "tokenizer.json", []string{"input_ids", "attention_mask"}, 768, 256)
	if e != nil {
		t.Errorf("NewONNXEmbedderFromFiles: got non-nil embedder %v, want nil", e)
	}
	if err != errNoONNX {
		t.Errorf("NewONNXEmbedderFromFiles: err = %v, want errNoONNX", err)
	}
}
