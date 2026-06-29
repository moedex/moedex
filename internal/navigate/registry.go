// This file has NO build tag: it is pure data plus pure functions and must
// compile in BOTH build arms (default pure-Go and `-tags lsp`), exactly like
// moduleRoot in navigate.go, so the Pool can route by a file's language in
// either arm. It pulls NO new go.mod dependencies.
//
// It is the multi-language productionizing of ADR 0017 Condition 1: the spike
// wired one server (gopls); this generalizes server selection to N languages,
// chosen by a file's extension, keyed per (root, language). Language servers
// remain EXTERNAL binaries on PATH (gopls, typescript-language-server,
// pyright-langserver, rust-analyzer, clangd) — never linked or imported.

package navigate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LangSpec is the launch recipe for one language server. Pure data — the exact
// command, args, optional initialize options, and the workspace-root markers
// that locate the server's project root.
type LangSpec struct {
	// Language is the canonical registry key, e.g. "go", "typescript".
	Language string
	// Command is the binary name resolved on PATH, e.g. "gopls".
	Command string
	// Args are the arguments passed to the command, e.g. {"--stdio"} for
	// typescript/pyright; nil for gopls/rust-analyzer/clangd (which speak LSP
	// over stdio by default and have no such flag).
	Args []string
	// InitOptions populates the initialize request's "initializationOptions";
	// nil means the field is omitted entirely (important for gopls — see
	// ensureInit). Shipped minimal/empty to keep shared servers light.
	InitOptions map[string]any
	// RootMarkers are files that mark this language's workspace root, in
	// nearest-first preference order. workspaceRoot walks up to the nearest
	// ancestor containing any of them. A marker containing '*' is matched as a
	// glob within each candidate directory (e.g. "*.sln", "*.csproj" for C#,
	// whose project files are named per-project).
	RootMarkers []string
	// ResolveEnv, when set, is called at server-launch time to compute extra
	// environment for the process (merged after os.Environ() and Config.Env).
	// It exists for servers that need to be pointed at a toolchain the OS doesn't
	// probe by default — notably csharp-ls, which needs DOTNET_ROOT when .NET is
	// installed off the default path (e.g. Homebrew). Returns nil to add nothing.
	// Resolved dynamically (not stored data) so the registry stays machine-portable.
	ResolveEnv func() []string `json:"-"`
}

// defaultRegistry is the exact set of language servers moedex ships, keyed by
// canonical language. Each entry is a verified launch recipe:
//
//   - gopls: no args, LSP over stdio by default (the original spike default).
//   - typescript-language-server: --stdio REQUIRED (it also has a CLI mode).
//   - pyright-langserver: --stdio REQUIRED.
//   - rust-analyzer: NO args — it has NO --stdio flag and speaks LSP over stdio
//     by default; passing --stdio would error. This is the single easiest fact
//     to get wrong, pinned by registry_test.go.
//   - clangd: NO required args (LSP over stdio by default).
//
// InitOptions are shipped empty (nil) across the board so heavy servers
// (rust-analyzer cargo check, clangd background-index) start light under a
// shared per-(root,language) process across many lanes (ADR 0017 Condition 2).
var defaultRegistry = map[string]LangSpec{
	"go": {
		Language:    "go",
		Command:     "gopls",
		Args:        nil,
		RootMarkers: []string{"go.mod", "go.work"},
	},
	"typescript": {
		Language:    "typescript",
		Command:     "typescript-language-server",
		Args:        []string{"--stdio"},
		RootMarkers: []string{"tsconfig.json", "jsconfig.json", "package.json"},
	},
	"python": {
		Language:    "python",
		Command:     "pyright-langserver",
		Args:        []string{"--stdio"},
		RootMarkers: []string{"pyproject.toml", "pyrightconfig.json", "setup.py", "setup.cfg"},
	},
	"rust": {
		Language:    "rust",
		Command:     "rust-analyzer",
		Args:        nil,
		RootMarkers: []string{"Cargo.toml"},
	},
	"cpp": {
		Language:    "cpp",
		Command:     "clangd",
		Args:        nil,
		RootMarkers: []string{"compile_commands.json", "compile_flags.txt", ".clangd"},
	},
	// C# is the dominant language in the TurnCommerce corpus (~60% of files), so
	// it is wired even though its server (csharp-ls, a dotnet global tool) needs a
	// little more setup than the others. Project roots are located by the per-
	// project *.sln / *.csproj files (globbed), with fixed-name fallbacks.
	"csharp": {
		Language:    "csharp",
		Command:     "csharp-ls",
		Args:        nil,
		RootMarkers: []string{"*.sln", "global.json", "Directory.Build.props", "*.csproj"},
		ResolveEnv:  dotnetRootEnv,
	},
	// SQL and ColdFusion are real parts of the TurnCommerce corpus (SQL ~2.9k
	// files, CFML ~2.2k), so they are wired despite thin LSP ecosystems — both
	// servers are best-effort and shallower than the C#/Go/TS servers:
	//   - sql-language-server: completion/hover/lint and schema-aware navigation;
	//     cross-file "references" is not a SQL concept, so refs/impl will usually
	//     be empty. Launched as `sql-language-server up --method stdio`.
	//   - cflsp (built from softwareCobbler/cfc): implements go-to-DEFINITION
	//     only — no references/implementation provider — so `def` works for CFML
	//     and refs/impl return empty. Run over stdio.
	// Both skip gracefully when their binary is absent, like every other entry.
	"sql": {
		Language:    "sql",
		Command:     "sql-language-server",
		Args:        []string{"up", "--method", "stdio"},
		RootMarkers: []string{".sqllsrc.json", ".git"},
	},
	"cfml": {
		Language: "cfml",
		Command:  "cflsp",
		Args:     []string{"--stdio"},
		// cflsp's onInitialize reads initializationOptions.config unconditionally
		// (it crashes on a missing initializationOptions); an empty config is fine
		// — the server fills defaults (engineVersion "lucee.5", etc.).
		InitOptions: map[string]any{"config": map[string]any{}},
		RootMarkers: []string{"Application.cfc", "Application.cfm", "box.json", "server.json", ".git"},
	},
	// The Angular front-ends pull in SCSS and HTML. The official VSCode servers
	// (vscode-langservers-extracted, one npm package, no build) cover them:
	//   - css: one server for .css/.scss/.sass/.less; real definition + references
	//     for variables/mixins/selectors (the strongest of the "extra" servers).
	//   - html: definition is shallow (mostly intra-document / link following) and
	//     it does NOT understand Angular component↔template binding — that needs the
	//     Angular Language Service (@angular/language-server), a heavier, version-
	//     pinned add not wired here. documentSymbol works.
	"css": {
		Language:    "css",
		Command:     "vscode-css-language-server",
		Args:        []string{"--stdio"},
		RootMarkers: []string{"package.json", "angular.json", ".git"},
	},
	"html": {
		Language:    "html",
		Command:     "vscode-html-language-server",
		Args:        []string{"--stdio"},
		RootMarkers: []string{"package.json", "angular.json", ".git"},
	},
}

// extToLang maps a file extension (lowercased, with leading dot) to a canonical
// registry language — i.e. which server handles the file. This is the ROUTING
// concept, distinct from the LSP didOpen languageId string (languageID() in
// lsp.go). typescript-language-server serves JavaScript too, so .js/.jsx route
// to "typescript" (a deliberate default; JS could be split to its own server
// later).
var extToLang = map[string]string{
	".go": "go",

	".ts":  "typescript",
	".tsx": "typescript",
	".mts": "typescript",
	".cts": "typescript",
	".js":  "typescript",
	".jsx": "typescript",
	".mjs": "typescript",
	".cjs": "typescript",

	".py":  "python",
	".pyi": "python",

	".rs": "rust",

	".cs":  "csharp",
	".csx": "csharp",

	".sql": "sql",

	".cfm":  "cfml",
	".cfc":  "cfml",
	".cfml": "cfml",

	".css":  "css",
	".scss": "css",
	".sass": "css",
	".less": "css",

	".html": "html",
	".htm":  "html",

	".c":   "cpp",
	".h":   "cpp",
	".cc":  "cpp",
	".cpp": "cpp",
	".cxx": "cpp",
	".hpp": "cpp",
	".hh":  "cpp",
	".hxx": "cpp",
	".m":   "cpp",
	".mm":  "cpp",
}

// LanguageForPath maps a file path to its canonical registry language by
// extension. ok is false for an unknown extension.
func LanguageForPath(path string) (string, bool) {
	lang, ok := extToLang[strings.ToLower(filepath.Ext(path))]
	return lang, ok
}

// SpecForLanguage looks a canonical language up in the registry.
func SpecForLanguage(lang string) (LangSpec, bool) {
	spec, ok := defaultRegistry[lang]
	return spec, ok
}

// SpecForPath composes LanguageForPath and SpecForLanguage: the launch recipe
// for whatever server handles the given file.
func SpecForPath(path string) (LangSpec, bool) {
	lang, ok := LanguageForPath(path)
	if !ok {
		return LangSpec{}, false
	}
	return SpecForLanguage(lang)
}

// workspaceRoot resolves a file to its (language, workspace-root) pair. It is
// pure and unit-testable without any server:
//
//  1. Determine the file's language by extension; unknown extensions default to
//     "go" (so the legacy go-only routing is unchanged).
//  2. Walk up from the file's directory to the nearest ancestor containing any
//     of the language's RootMarkers.
//  3. Fall back to the file's own directory if no marker is found.
//
// For a Go file this walks to the nearest go.mod/go.work, producing the SAME
// root moduleRoot does for the existing gopls tests — so (root,language) keying
// does not split the existing single-root Go servers into two.
func workspaceRoot(file string) (lang, root string) {
	lang, ok := LanguageForPath(file)
	if !ok {
		lang = "go"
	}
	spec := defaultRegistry[lang]

	dir := file
	if fi, err := os.Stat(file); err != nil || !fi.IsDir() {
		dir = filepath.Dir(file)
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	for {
		for _, marker := range spec.RootMarkers {
			if markerPresent(dir, marker) {
				return lang, dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return lang, filepath.Dir(file)
		}
		dir = parent
	}
}

// markerPresent reports whether a root marker exists in dir. A marker containing
// '*' is matched as a glob (e.g. "*.sln"); otherwise it is an exact filename.
func markerPresent(dir, marker string) bool {
	if strings.Contains(marker, "*") {
		matches, _ := filepath.Glob(filepath.Join(dir, marker))
		return len(matches) > 0
	}
	_, err := os.Stat(filepath.Join(dir, marker))
	return err == nil
}

// dotnetRootEnv returns a DOTNET_ROOT override so a framework-dependent server
// (csharp-ls) can find the .NET runtime when it is installed off the default
// probe path — e.g. Homebrew installs to <prefix>/opt/dotnet/libexec rather than
// /usr/local/share/dotnet. It resolves dotnet on PATH and picks the first
// candidate directory that actually contains a "shared" runtime folder. Returns
// nil if DOTNET_ROOT is already set, dotnet is not found, or none validate (in
// which case the server uses its own discovery and may error — surfaced honestly
// rather than guessed).
func dotnetRootEnv() []string {
	if os.Getenv("DOTNET_ROOT") != "" {
		return nil
	}
	p, err := exec.LookPath("dotnet")
	if err != nil {
		return nil
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	bindir := filepath.Dir(p)
	candidates := []string{
		bindir,                                      // standard layout: <root>/dotnet -> root has shared/
		filepath.Join(filepath.Dir(bindir), "libexec"), // Homebrew: <cellar>/bin/dotnet -> ../libexec
	}
	for _, c := range candidates {
		if fi, err := os.Stat(filepath.Join(c, "shared")); err == nil && fi.IsDir() {
			return []string{"DOTNET_ROOT=" + c}
		}
	}
	return nil
}
