// Package sourcescope defines literal source-location filters shared by retrieval
// adapters. Scope selects indexed occurrences, not semantic symbol identity.
package sourcescope

import (
	"fmt"
	"path"
	"strings"
	"unicode"

	"moedex/internal/index"
)

type Scope struct {
	Repo       string `json:"repo,omitempty"`
	PathPrefix string `json:"path_prefix,omitempty"`
	Language   string `json:"language,omitempty"`
}

var languageExtensions = map[string][]string{
	"go": {".go"}, "csharp": {".cs"}, "typescript": {".ts", ".tsx", ".mts", ".cts"},
	"javascript": {".js", ".jsx", ".mjs", ".cjs"}, "python": {".py", ".pyi"},
	"sql": {".sql"}, "cfml": {".cfm", ".cfc"}, "html": {".html", ".htm"},
	"css": {".css"}, "scss": {".scss"}, "sass": {".sass"}, "less": {".less"},
	"rust": {".rs"}, "c": {".c", ".h"}, "cpp": {".cc", ".cpp", ".cxx", ".hpp", ".hh", ".hxx"},
	"java": {".java"}, "kotlin": {".kt", ".kts"}, "swift": {".swift"}, "ruby": {".rb"},
	"php": {".php"}, "shell": {".sh", ".bash", ".zsh"}, "json": {".json"},
	"yaml": {".yaml", ".yml"}, "toml": {".toml"}, "xml": {".xml"}, "markdown": {".md", ".markdown"},
}

// Languages returns canonical supported names in a stable order.
func Languages() []string {
	return []string{"c", "cfml", "cpp", "csharp", "css", "go", "html", "java", "javascript", "json", "kotlin", "less", "markdown", "php", "python", "ruby", "rust", "sass", "scss", "shell", "sql", "swift", "toml", "typescript", "xml", "yaml"}
}

func (s Scope) Empty() bool { return s.Repo == "" && s.PathPrefix == "" && s.Language == "" }

func (s Scope) Validate() error {
	for _, value := range []string{s.Repo, s.PathPrefix, s.Language} {
		if strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return fmt.Errorf("source scope cannot contain control characters")
		}
	}
	if s.Language != "" {
		if _, ok := languageExtensions[s.Language]; !ok {
			return fmt.Errorf("unsupported source language %q", s.Language)
		}
	}
	if strings.ContainsAny(s.PathPrefix, "*?[]") {
		return fmt.Errorf("path_prefix must be literal, not a glob")
	}
	if s.PathPrefix != "" {
		prefix := strings.TrimSuffix(s.PathPrefix, "/")
		if prefix == "" || strings.HasPrefix(prefix, "/") || strings.Contains(prefix, "\\") || (len(prefix) >= 2 && prefix[1] == ':') {
			return fmt.Errorf("path_prefix must be a relative slash-separated path")
		}
		for _, segment := range strings.Split(prefix, "/") {
			if segment == "" || segment == "." || segment == ".." {
				return fmt.Errorf("path_prefix cannot contain empty, dot, or parent segments")
			}
		}
	}
	return nil
}

// Match assumes Validate succeeded. Paths are literal, case-sensitive prefixes
// on segment boundaries; a trailing slash is optional. Language uses the final
// extension case-insensitively, without interpreting file contents.
func (s Scope) Match(file index.FileRef) bool {
	if s.Repo != "" && file.Repo != s.Repo {
		return false
	}
	prefix := strings.TrimSuffix(s.PathPrefix, "/")
	if prefix != "" && file.RelPath != prefix && !strings.HasPrefix(file.RelPath, prefix+"/") {
		return false
	}
	if s.Language != "" {
		extension := strings.ToLower(path.Ext(file.RelPath))
		for _, allowed := range languageExtensions[s.Language] {
			if extension == allowed {
				return true
			}
		}
		return false
	}
	return true
}
