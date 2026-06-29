//go:build lsp

package main

import "testing"

// TestBuildConfig is pure logic — no language server is started — so it always
// runs under the lsp test build. It pins the three resolution modes: explicit
// -server override, forced -lang, and auto (empty Server -> Pool registry
// routing by file extension).
func TestBuildConfig(t *testing.T) {
	tests := []struct {
		name       string
		lang       string
		server     string
		file       string
		wantServer string // cfg.Server ("" == auto/registry routing)
		wantArgs   []string
		wantCanon  string
	}{
		// 1. explicit -server wins over everything.
		{name: "explicit server over lang", lang: "go", server: "my-langserver", wantServer: "my-langserver", wantCanon: ""},
		{name: "explicit server over file", file: "a.ts", server: "pyright-langserver", wantServer: "pyright-langserver", wantCanon: ""},
		// 2. forced -lang resolves the registry spec.
		{name: "forced go", lang: "go", wantServer: "gopls", wantCanon: "go"},
		{name: "forced ts alias", lang: "ts", wantServer: "typescript-language-server", wantArgs: []string{"--stdio"}, wantCanon: "typescript"},
		{name: "forced py alias", lang: "py", wantServer: "pyright-langserver", wantArgs: []string{"--stdio"}, wantCanon: "python"},
		{name: "forced rust", lang: "rust", wantServer: "rust-analyzer", wantCanon: "rust"},
		{name: "forced cpp", lang: "cpp", wantServer: "clangd", wantCanon: "cpp"},
		{name: "forced c alias maps to cpp", lang: "c", wantServer: "clangd", wantCanon: "cpp"},
		{name: "forced lang beats file", lang: "python", file: "x.go", wantServer: "pyright-langserver", wantArgs: []string{"--stdio"}, wantCanon: "python"},
		// 3. auto mode: empty Server (Pool routes per file), canonical inferred.
		{name: "auto go", file: "x.go", wantServer: "", wantCanon: "go"},
		{name: "auto tsx", file: "app.tsx", wantServer: "", wantCanon: "typescript"},
		{name: "auto python", file: "m.py", wantServer: "", wantCanon: "python"},
		{name: "auto rust", file: "lib.rs", wantServer: "", wantCanon: "rust"},
		{name: "auto cpp hpp", file: "h.hpp", wantServer: "", wantCanon: "cpp"},
		{name: "auto nothing given", wantServer: "", wantCanon: ""},
		{name: "auto unknown extension", file: "README.md", wantServer: "", wantCanon: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, canon := buildConfig(tc.lang, tc.server, tc.file)
			if cfg.Server != tc.wantServer {
				t.Errorf("server = %q, want %q", cfg.Server, tc.wantServer)
			}
			if canon != tc.wantCanon {
				t.Errorf("canonical = %q, want %q", canon, tc.wantCanon)
			}
			if !eqStrs(cfg.Args, tc.wantArgs) {
				t.Errorf("args = %v, want %v", cfg.Args, tc.wantArgs)
			}
		})
	}
}

func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
