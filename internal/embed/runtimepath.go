package embed

import (
	"os"
	"runtime"
)

// ResolveONNXRuntimePath returns the explicitly configured ONNX Runtime shared
// library, then ONNXRUNTIME_LIB_PATH, then the first existing platform-standard
// installation. An empty result leaves final discovery to the ONNX binding.
func ResolveONNXRuntimePath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if configured := os.Getenv("ONNXRUNTIME_LIB_PATH"); configured != "" {
		return configured
	}
	return firstExistingONNXRuntimePath(defaultONNXRuntimePaths(runtime.GOOS))
}

func firstExistingONNXRuntimePath(candidates []string) string {
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func defaultONNXRuntimePaths(goos string) []string {
	switch goos {
	case "darwin":
		return []string{
			"/opt/homebrew/lib/libonnxruntime.dylib",
			"/usr/local/lib/libonnxruntime.dylib",
		}
	case "linux":
		return []string{
			"/home/linuxbrew/.linuxbrew/lib/libonnxruntime.so",
			"/usr/local/lib/libonnxruntime.so",
			"/usr/lib/libonnxruntime.so",
			"/usr/lib/x86_64-linux-gnu/libonnxruntime.so",
			"/usr/lib/aarch64-linux-gnu/libonnxruntime.so",
		}
	case "windows":
		return []string{"onnxruntime.dll"}
	default:
		return nil
	}
}
