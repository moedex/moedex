//go:build lsp

package navcmd

import (
	"testing"

	"moedex/internal/navigate"
)

// TestBuildConfig is pure logic — no language server is started — so it always
// runs under the lsp test build. It pins the three resolution modes: explicit
// -server override, forced -lang, and auto (empty Server -> Pool registry
// routing by file extension).
//
// Forced -lang must produce Config{Language: canon} and leave Server/Args
// EMPTY, not a resolved Command/Args pair: setting Server here would put NewLSP
// on its explicit-override branch, which never consults the registry's
// InitOptions or ResolveEnv (e.g. cfml's required initializationOptions.config,
// csharp's DOTNET_ROOT). Only Config.Language reaches NewLSP's registry branch.
func TestBuildConfig(t *testing.T) {
	tests := []struct {
		name         string
		lang         string
		server       string
		file         string
		wantServer   string // cfg.Server ("" == auto/registry routing)
		wantArgs     []string
		wantLanguage string // cfg.Language (forced-lang mode only)
		wantCanon    string
	}{
		// 1. explicit -server wins over everything.
		{name: "explicit server over lang", lang: "go", server: "my-langserver", wantServer: "my-langserver", wantCanon: ""},
		{name: "explicit server over file", file: "a.ts", server: "pyright-langserver", wantServer: "pyright-langserver", wantCanon: ""},
		// 2. forced -lang resolves to Config.Language, NOT a resolved Server/Args —
		// resolution itself is the registry's job (NewLSP), so InitOptions and
		// ResolveEnv travel with it.
		{name: "forced go", lang: "go", wantLanguage: "go", wantCanon: "go"},
		{name: "forced ts alias", lang: "ts", wantLanguage: "typescript", wantCanon: "typescript"},
		{name: "forced py alias", lang: "py", wantLanguage: "python", wantCanon: "python"},
		{name: "forced rust", lang: "rust", wantLanguage: "rust", wantCanon: "rust"},
		{name: "forced cpp", lang: "cpp", wantLanguage: "cpp", wantCanon: "cpp"},
		{name: "forced c alias maps to cpp", lang: "c", wantLanguage: "cpp", wantCanon: "cpp"},
		{name: "forced lang beats file", lang: "python", file: "x.go", wantLanguage: "python", wantCanon: "python"},
		// csharp/cfml are the registry entries that actually carry ResolveEnv /
		// InitOptions — the case the old Server/Args-copying path silently broke.
		{name: "forced csharp", lang: "csharp", wantLanguage: "csharp", wantCanon: "csharp"},
		{name: "forced cs alias maps to csharp", lang: "cs", wantLanguage: "csharp", wantCanon: "csharp"},
		{name: "forced cfml", lang: "cfml", wantLanguage: "cfml", wantCanon: "cfml"},
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
			if cfg.Language != tc.wantLanguage {
				t.Errorf("language = %q, want %q", cfg.Language, tc.wantLanguage)
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

// TestBuildConfigForcedLangPreservesRegistryExtras locks in the actual bug fix:
// forced -lang mode must produce a Config that, when handed to NewLSP, takes the
// registry branch (Server empty) rather than the explicit-override branch
// (Server set) — only the registry branch resolves InitOptions/ResolveEnv. cfml
// and csharp are the two registry entries where this matters today.
func TestBuildConfigForcedLangPreservesRegistryExtras(t *testing.T) {
	for _, lang := range []string{"cfml", "csharp"} {
		cfg, canon := buildConfig(lang, "", "")
		if cfg.Server != "" {
			t.Errorf("lang %q: cfg.Server = %q, want empty so NewLSP takes the registry branch (preserving InitOptions/ResolveEnv)", lang, cfg.Server)
		}
		if cfg.Language != canon {
			t.Errorf("lang %q: cfg.Language = %q, want %q", lang, cfg.Language, canon)
		}
		spec, ok := navigate.SpecForLanguage(canon)
		if !ok {
			t.Fatalf("lang %q: no registry spec for canonical %q", lang, canon)
		}
		if cfg.Language != spec.Language {
			t.Errorf("lang %q: cfg.Language = %q does not match registry spec.Language %q", lang, cfg.Language, spec.Language)
		}
	}
}

// TestDisplayServer pins how the -json/-stats server name is reported across all
// three modes. In forced-lang mode cfg.Server is empty (see TestBuildConfig), so
// displayServer must resolve the command from cfg.Language — NOT fall through to
// the file-extension lookup, which would report the wrong server whenever the
// forced language and the query file's extension disagree.
func TestDisplayServer(t *testing.T) {
	tests := []struct {
		name string
		cfg  navigate.Config
		file string
		want string
	}{
		{name: "explicit override", cfg: navigate.Config{Server: "my-langserver"}, file: "x.go", want: "my-langserver"},
		{name: "forced lang matches file", cfg: navigate.Config{Language: "python"}, file: "m.py", want: "pyright-langserver"},
		{name: "forced lang beats file", cfg: navigate.Config{Language: "python"}, file: "x.go", want: "pyright-langserver"},
		{name: "forced csharp no file", cfg: navigate.Config{Language: "csharp"}, file: "", want: "csharp-ls"},
		{name: "forced cfml", cfg: navigate.Config{Language: "cfml"}, file: "x.go", want: "cflsp"},
		{name: "auto by file", cfg: navigate.Config{}, file: "a.ts", want: "typescript-language-server"},
		{name: "auto unknown file", cfg: navigate.Config{}, file: "README.md", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayServer(tc.cfg, tc.file); got != tc.want {
				t.Errorf("displayServer = %q, want %q", got, tc.want)
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
