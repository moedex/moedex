package embed

import (
	"strings"
	"testing"
)

func TestONNXOptionsRejectNegativeThreads(t *testing.T) {
	for _, options := range []ONNXOptions{{IntraOpThreads: -1}, {InterOpThreads: -1}} {
		_, err := NewONNXEmbedderWithOptions("", options)
		if err == nil || !strings.Contains(err.Error(), "threads must be non-negative") {
			t.Fatalf("NewONNXEmbedderWithOptions(%+v) error = %v, want validation error", options, err)
		}
	}
}
