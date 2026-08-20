package embed

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResolveONNXRuntimePathPrecedence(t *testing.T) {
	t.Setenv("ONNXRUNTIME_LIB_PATH", "/env/libonnxruntime.so")
	if got := ResolveONNXRuntimePath("/flag/libonnxruntime.so"); got != "/flag/libonnxruntime.so" {
		t.Fatalf("explicit path = %q", got)
	}
	if got := ResolveONNXRuntimePath(""); got != "/env/libonnxruntime.so" {
		t.Fatalf("environment path = %q", got)
	}
}

func TestFirstExistingONNXRuntimePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "libonnxruntime.so")
	if got := firstExistingONNXRuntimePath([]string{filepath.Join(dir, "missing"), path}); got != "" {
		t.Fatalf("path unexpectedly exists before fixture creation: %q", got)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := firstExistingONNXRuntimePath([]string{filepath.Join(dir, "missing"), path}); got != path {
		t.Fatalf("first existing path = %q, want %q", got, path)
	}
}

func TestDefaultONNXRuntimePaths(t *testing.T) {
	if got := defaultONNXRuntimePaths("darwin"); !reflect.DeepEqual(got, []string{
		"/opt/homebrew/lib/libonnxruntime.dylib",
		"/usr/local/lib/libonnxruntime.dylib",
	}) {
		t.Fatalf("darwin paths = %v", got)
	}
	if got := defaultONNXRuntimePaths("plan9"); got != nil {
		t.Fatalf("unknown-platform paths = %v", got)
	}
}
