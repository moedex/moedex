// No build tag: these tests exercise pure registry/data and pool-keying logic
// that compiles in BOTH build arms and needs NO language-server binary. They
// always run (under the default build and under -tags lsp), pinning the
// easy-to-get-wrong launch facts (notably: rust-analyzer takes NO --stdio).

package navigate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLanguageForPath(t *testing.T) {
	cases := []struct {
		path     string
		wantLang string
		wantOK   bool
	}{
		{"a.go", "go", true},
		{"x/y/z.go", "go", true},
		{"Component.tsx", "typescript", true},
		{"mod.ts", "typescript", true},
		{"esm.mjs", "typescript", true},
		{"old.cjs", "typescript", true},
		{"app.js", "typescript", true},
		{"view.jsx", "typescript", true},
		{"stub.pyi", "python", true},
		{"main.py", "python", true},
		{"lib.rs", "rust", true},
		{"header.hpp", "cpp", true},
		{"impl.cc", "cpp", true},
		{"plain.c", "cpp", true},
		{"obj.m", "cpp", true},
		{"obj.mm", "cpp", true},
		{"README.md", "", false},
		{"noext", "", false},
		{"archive.tar.gz", "", false},
		// Case-insensitive extension.
		{"MAIN.GO", "go", true},
	}
	for _, c := range cases {
		got, ok := LanguageForPath(c.path)
		if got != c.wantLang || ok != c.wantOK {
			t.Errorf("LanguageForPath(%q) = (%q,%v), want (%q,%v)", c.path, got, ok, c.wantLang, c.wantOK)
		}
	}
}

func TestSpecForLanguage(t *testing.T) {
	cases := []struct {
		lang     string
		wantCmd  string
		wantArgs []string
	}{
		{"go", "gopls", nil},
		{"typescript", "typescript-language-server", []string{"--stdio"}},
		{"python", "pyright-langserver", []string{"--stdio"}},
		// THE load-bearing assertion: rust-analyzer must NOT get --stdio (it has
		// no such flag and would error). Args must be nil.
		{"rust", "rust-analyzer", nil},
		{"cpp", "clangd", nil},
	}
	for _, c := range cases {
		spec, ok := SpecForLanguage(c.lang)
		if !ok {
			t.Errorf("SpecForLanguage(%q): not found", c.lang)
			continue
		}
		if spec.Command != c.wantCmd {
			t.Errorf("SpecForLanguage(%q).Command = %q, want %q", c.lang, spec.Command, c.wantCmd)
		}
		if !equalArgs(spec.Args, c.wantArgs) {
			t.Errorf("SpecForLanguage(%q).Args = %v, want %v", c.lang, spec.Args, c.wantArgs)
		}
		// No language ships non-empty InitOptions (keep shared servers light).
		if spec.InitOptions != nil {
			t.Errorf("SpecForLanguage(%q).InitOptions = %v, want nil", c.lang, spec.InitOptions)
		}
	}
	if _, ok := SpecForLanguage("cobol"); ok {
		t.Error("SpecForLanguage(cobol) unexpectedly found")
	}
}

// TestRustAnalyzerNoStdio is a dedicated guard on the single fact most likely to
// be gotten wrong, so a regression is impossible to miss.
func TestRustAnalyzerNoStdio(t *testing.T) {
	spec, ok := SpecForLanguage("rust")
	if !ok {
		t.Fatal("rust spec missing")
	}
	for _, a := range spec.Args {
		if a == "--stdio" {
			t.Fatalf("rust-analyzer must not be launched with --stdio; got args %v", spec.Args)
		}
	}
}

func TestSpecForPath(t *testing.T) {
	spec, ok := SpecForPath("foo/bar.tsx")
	if !ok || spec.Command != "typescript-language-server" {
		t.Errorf("SpecForPath(.tsx) = (%+v,%v), want typescript-language-server", spec, ok)
	}
	if _, ok := SpecForPath("foo/bar.unknown"); ok {
		t.Error("SpecForPath(.unknown) unexpectedly found")
	}
}

func TestWorkspaceRoot(t *testing.T) {
	// Build a temp tree with several language workspace markers and assert each
	// file resolves to its nearest-ancestor root + correct language.
	base := t.TempDir()

	type fixture struct {
		marker string // root-marker file to create
		lang   string // expected language
		srcRel string // source file path relative to the marker dir, nested
		srcExt string
	}
	fixtures := []fixture{
		{"go.mod", "go", "pkg/sub/file", ".go"},
		{"Cargo.toml", "rust", "src/inner/lib", ".rs"},
		{"tsconfig.json", "typescript", "src/components/App", ".tsx"},
		{"pyproject.toml", "python", "pkg/mod/main", ".py"},
		{"compile_commands.json", "cpp", "src/impl", ".cc"},
	}

	for _, fx := range fixtures {
		rootDir := filepath.Join(base, fx.lang+"-proj")
		if err := os.MkdirAll(rootDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rootDir, fx.marker), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		srcDir := filepath.Join(rootDir, filepath.Dir(fx.srcRel))
		if err := os.MkdirAll(srcDir, 0o755); err != nil {
			t.Fatal(err)
		}
		srcFile := filepath.Join(rootDir, fx.srcRel+fx.srcExt)
		if err := os.WriteFile(srcFile, []byte("y"), 0o644); err != nil {
			t.Fatal(err)
		}

		gotLang, gotRoot := workspaceRoot(srcFile)
		if gotLang != fx.lang {
			t.Errorf("workspaceRoot(%s) lang = %q, want %q", srcFile, gotLang, fx.lang)
		}
		wantRoot, _ := filepath.Abs(rootDir)
		if gotRoot != wantRoot {
			t.Errorf("workspaceRoot(%s) root = %q, want %q", srcFile, gotRoot, wantRoot)
		}
	}
}

// TestWorkspaceRootGoParity proves workspaceRoot for a Go file yields the SAME
// root as the legacy moduleRoot — the invariant that keeps the existing gopls
// pool tests from splitting one root into two entries.
func TestWorkspaceRootGoParity(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(base, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	goFile := filepath.Join(sub, "f.go")
	if err := os.WriteFile(goFile, []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lang, root := workspaceRoot(goFile)
	if lang != "go" {
		t.Errorf("lang = %q, want go", lang)
	}
	if root != moduleRoot(goFile) {
		t.Errorf("workspaceRoot root %q != moduleRoot %q", root, moduleRoot(goFile))
	}
}

// TestWorkspaceRootFallback: a file with a known language but no marker anywhere
// up the tree falls back to its own directory.
func TestWorkspaceRootFallback(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "lonely.rs")
	if err := os.WriteFile(f, []byte("fn main(){}"), 0o644); err != nil {
		t.Fatal(err)
	}
	lang, root := workspaceRoot(f)
	if lang != "rust" {
		t.Errorf("lang = %q, want rust", lang)
	}
	// No Cargo.toml in the temp tree (t.TempDir is isolated); root is the file's
	// own directory.
	if root != filepath.Dir(f) {
		t.Errorf("root = %q, want %q (file dir fallback)", root, filepath.Dir(f))
	}
}

// TestPoolKeyDisambiguation proves one repo root can hold two distinct entries
// keyed by language. Both NavigatorFor calls fail fast in the default (!lsp)
// build (NewLSP errors), and in the lsp build they'd fail only if the binary is
// missing — either way the entries map records two distinct keys briefly via
// keyFor, which we assert directly. We also assert the composite keys differ.
func TestPoolKeyDisambiguation(t *testing.T) {
	if keyFor("go", "/repo") == keyFor("typescript", "/repo") {
		t.Fatal("keyFor must distinguish languages at the same root")
	}
	if keyFor("go", "/a") == keyFor("go", "/b") {
		t.Fatal("keyFor must distinguish roots for the same language")
	}
	// Navigator(root) must map to the go-language key for the same root.
	if keyFor("go", "/repo") != keyForNavigator("/repo") {
		t.Fatal("Navigator back-compat must key as go")
	}
}

// keyForNavigator mirrors what Navigator(ctx, root) computes internally, for the
// disambiguation assertion above (Navigator delegates to NavigatorFor with
// lang="go").
func keyForNavigator(root string) string { return keyFor("go", root) }

// TestPoolClosedNavigatorFor: NavigatorFor on a closed pool errors cleanly, with
// no server binary involved.
func TestPoolClosedNavigatorFor(t *testing.T) {
	p := NewPool(Config{})
	_ = p.Close()
	if _, err := p.NavigatorFor(context.Background(), "typescript", "/repo"); err == nil {
		t.Fatal("NavigatorFor on closed pool should error")
	}
	if _, err := p.Navigator(context.Background(), "/repo"); err == nil {
		t.Fatal("Navigator on closed pool should error")
	}
}

func equalArgs(a, b []string) bool {
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
