package version

import "testing"

func TestInfoMCP(t *testing.T) {
	tests := []struct {
		name string
		info Info
		want string
	}{
		{"development", Info{Tag: "dev", Commit: "0123456789abcdef"}, "dev+0123456789ab"},
		{"tagged", Info{Tag: "v1.2.3", Commit: "abcdef"}, "v1.2.3+abcdef"},
		{"dirty stamped", Info{Tag: "dev", Commit: "abcdef", Modified: true, Source: "source-sha"}, "dev+abcdef.dirty.source-sha"},
		{"dirty unstamped", Info{Tag: "dev", Commit: "abcdef", Modified: true}, "dev+abcdef.dirty.unknown"},
		{"unknown", Info{Tag: "dev", Commit: "unknown"}, "dev+unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.MCP(); got != tt.want {
				t.Fatalf("MCP()=%q want %q", got, tt.want)
			}
		})
	}
}
