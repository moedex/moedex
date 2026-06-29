//go:build lsp

package main

import (
	"strings"

	"moedex/internal/navigate"
)

// langAliases normalizes a user-typed -lang selector to the canonical language
// keys used by internal/navigate's server registry. The registry itself
// (commands, args, extension map) is the single source of truth — this file only
// translates the selectors a human might type ("ts", "c++") into those keys, so
// the cmd no longer duplicates (and drifts from) the launch table.
var langAliases = map[string]string{
	"go": "go", "golang": "go",
	"typescript": "typescript", "ts": "typescript", "tsx": "typescript",
	"javascript": "typescript", "js": "typescript", "jsx": "typescript",
	"python": "python", "py": "python",
	"rust": "rust", "rs": "rust",
	"cpp": "cpp", "c": "cpp", "c++": "cpp", "cc": "cpp", "cxx": "cpp",
}

// canonLang resolves a user selector to a navigate canonical language, or "".
func canonLang(lang string) string {
	return langAliases[strings.ToLower(strings.TrimSpace(lang))]
}

// buildConfig decides how the Pool should launch servers for this invocation and
// returns the canonical language for display. Resolution order:
//
//  1. explicit -server: pin that one command (override mode — Config.Server set).
//  2. explicit -lang: force that language's registry spec for every file.
//  3. otherwise (the common case): leave Config.Server EMPTY so the Pool routes
//     each query to the right server per file extension through navigate's
//     registry — the actual multi-language path. (Previously the cmd always
//     passed a resolved Server, which pinned the Pool in single-server mode and
//     made registry routing unreachable.)
func buildConfig(lang, server, file string) (navigate.Config, string) {
	if server != "" {
		return navigate.Config{Server: server}, ""
	}
	if canon := canonLang(lang); canon != "" {
		if spec, ok := navigate.SpecForLanguage(canon); ok {
			return navigate.Config{Server: spec.Command, Args: spec.Args}, canon
		}
	}
	// Auto mode: registry routing by file extension happens inside the Pool.
	display, _ := navigate.LanguageForPath(file)
	return navigate.Config{}, display
}

// displayServer names the server command for -json/-stats reporting. In override
// or forced-lang mode that is cfg.Server; in auto mode it is resolved from the
// query file the same way the Pool will resolve it.
func displayServer(cfg navigate.Config, file string) string {
	if cfg.Server != "" {
		return cfg.Server
	}
	if spec, ok := navigate.SpecForPath(file); ok {
		return spec.Command
	}
	return ""
}
