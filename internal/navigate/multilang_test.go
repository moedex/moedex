//go:build lsp

package navigate

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// These tests prove the multi-language routing path end-to-end against a real
// second-language server. They skip when that server's binary is absent —
// exactly like the gopls tests skip without gopls (env-gated skips are
// acceptable per CLAUDE.md). Only gopls is installed in CI here, so these skip;
// they document and verify the (root,language) routing when a server is present.

// TestLanguageID checks the LSP didOpen languageId mapping. languageID lives in
// the lsp-tagged build, so this test does too. It is the DISTINCT concept from
// the registry language (extToLang): .tsx routes to the "typescript" server but
// carries the "typescriptreact" didOpen languageId.
func TestLanguageID(t *testing.T) {
	cases := map[string]string{
		"a.go":  "go",
		"a.ts":  "typescript",
		"a.tsx": "typescriptreact",
		"a.js":  "javascript",
		"a.jsx": "javascriptreact",
		"a.py":  "python",
		"a.pyi": "python",
		"a.rs":  "rust",
		"a.c":   "c",
		"a.h":   "c",
		"a.cpp": "cpp",
		"a.hpp": "cpp",
		"a.m":   "objective-c",
		"a.mm":  "objective-cpp",
		"a.txt": "plaintext",
	}
	for path, want := range cases {
		if got := languageID(path); got != want {
			t.Errorf("languageID(%q) = %q, want %q", path, got, want)
		}
	}
}

func serverAvailable(t *testing.T, cmd string) bool {
	t.Helper()
	_, err := exec.LookPath(cmd)
	return err == nil
}

// TestDefinition_TypeScript exercises the typescript registry entry: a .ts file
// routes through workspaceRoot -> ("typescript", <tsconfig dir>) ->
// typescript-language-server, and a cross-symbol definition resolves.
func TestDefinition_TypeScript(t *testing.T) {
	if !serverAvailable(t, "typescript-language-server") {
		t.Skip("typescript-language-server not on PATH; skipping TS routing test")
	}
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "tsconfig.json"), `{"compilerOptions":{"strict":true},"include":["*.ts"]}`)
	shape := filepath.Join(root, "shape.ts")
	mustWrite(t, shape, "export class Circle {\n  constructor(public r: number) {}\n  area(): number { return 3.14159 * this.r * this.r; }\n}\n")
	main := filepath.Join(root, "main.ts")
	mustWrite(t, main, "import { Circle } from './shape';\n\nconst c = new Circle(2);\nconsole.log(c.area());\n")

	// Routing assertion (does not need the server to succeed): the file resolves
	// to the typescript language at the tsconfig root.
	lang, gotRoot := workspaceRoot(main)
	if lang != "typescript" {
		t.Fatalf("workspaceRoot lang = %q, want typescript", lang)
	}
	wantRoot, _ := filepath.Abs(root)
	if gotRoot != wantRoot {
		t.Fatalf("workspaceRoot root = %q, want %q", gotRoot, wantRoot)
	}

	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })

	// Query the definition at the usage site `new Circle(2)` — this resolves
	// cross-file to the class declaration in shape.ts (the import-clause symbol
	// resolves to its own local binding, a valid but non-cross-file TS answer).
	// typescript-language-server returns the in-file binding first and only
	// resolves cross-file once the project has loaded, so retry until shape.ts
	// appears rather than accepting the first non-empty result.
	at := identPos(t, main, "new Circle(2)", "Circle")
	var inShape bool
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && !inShape {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		locs, err := pool.Definition(ctx, at)
		cancel()
		if err != nil {
			t.Fatalf("ts definition: %v", err)
		}
		for _, l := range locs {
			if filepath.Base(l.File) == "shape.ts" {
				inShape = true
			}
		}
		if !inShape {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if !inShape {
		t.Error("definition never resolved cross-file to shape.ts within deadline")
	}
}

// TestPython_Routing proves a .py file routes to the python registry entry. It
// only asserts routing + spec selection (no pyright needed), then runs a real
// query guarded by a LookPath skip.
func TestPython_Routing(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "pyproject.toml"), "[tool.pyright]\n")
	src := filepath.Join(root, "pkg", "main.py")
	mustWrite(t, src, "def f():\n    return 1\n")

	lang, gotRoot := workspaceRoot(src)
	if lang != "python" {
		t.Fatalf("workspaceRoot lang = %q, want python", lang)
	}
	wantRoot, _ := filepath.Abs(root)
	if gotRoot != wantRoot {
		t.Fatalf("workspaceRoot root = %q, want %q", gotRoot, wantRoot)
	}
	spec, _ := SpecForLanguage(lang)
	if spec.Command != "pyright-langserver" {
		t.Fatalf("python spec command = %q, want pyright-langserver", spec.Command)
	}

	if !serverAvailable(t, "pyright-langserver") {
		t.Skip("pyright-langserver not on PATH; routing verified, skipping live query")
	}
	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })
	at := identPos(t, src, "def f", "f")
	// Just ensure the call path works (references on the def itself).
	if _, err := pool.References(context.Background(), at, true); err != nil {
		t.Fatalf("pyright references: %v", err)
	}
}

// TestCSharp_Routing proves a .cs file routes to the csharp registry entry, that
// the *.csproj GLOB root marker locates the project root, and (when csharp-ls is
// installed) that references resolve. C# is the dominant language in the corpus,
// so this is the most load-bearing registry entry.
func TestCSharp_Routing(t *testing.T) {
	root := t.TempDir()
	// Project file has a per-project name — only the "*.csproj" glob marker finds it.
	mustWrite(t, filepath.Join(root, "App.csproj"),
		`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net10.0</TargetFramework></PropertyGroup></Project>`)
	src := filepath.Join(root, "Program.cs")
	mustWrite(t, src, "class Calc\n{\n    public int Add(int a, int b) => a + b;\n}\n\nclass Program\n{\n    static void Main()\n    {\n        var c = new Calc();\n        System.Console.WriteLine(c.Add(2, 3));\n    }\n}\n")

	lang, gotRoot := workspaceRoot(src)
	if lang != "csharp" {
		t.Fatalf("workspaceRoot lang = %q, want csharp", lang)
	}
	wantRoot, _ := filepath.Abs(root)
	if gotRoot != wantRoot {
		t.Fatalf("workspaceRoot root = %q, want %q (the *.csproj glob marker should locate it)", gotRoot, wantRoot)
	}
	spec, _ := SpecForLanguage(lang)
	if spec.Command != "csharp-ls" {
		t.Fatalf("csharp spec command = %q, want csharp-ls", spec.Command)
	}

	if !serverAvailable(t, "csharp-ls") {
		t.Skip("csharp-ls not on PATH; routing verified, skipping live query")
	}
	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })
	// References to Add() — declaration + the call site. csharp-ls loads the MSBuild
	// project asynchronously, so retry until the call site appears.
	at := identPos(t, src, "public int Add", "Add")
	deadline := time.Now().Add(45 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		locs, err := pool.References(ctx, at, true)
		cancel()
		if err != nil {
			t.Fatalf("csharp references: %v", err)
		}
		if n = len(locs); n >= 2 {
			break
		}
		time.Sleep(time.Second)
	}
	if n < 2 {
		t.Errorf("expected >=2 references to Add() (decl + call), got %d", n)
	}
}

// TestPolyglotRoot proves a single repo root containing both Go and TypeScript
// spawns TWO distinct pooled servers keyed by language. The Go server is created
// (gopls present); the TS entry is created only if its server is present. The
// (root,language) keying is the load-bearing property.
func TestPolyglotRoot(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping polyglot routing test")
	}
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module poly\n\ngo 1.26\n")
	mustWrite(t, filepath.Join(root, "tsconfig.json"), `{"include":["*.ts"]}`)
	goFile := filepath.Join(root, "main.go")
	mustWrite(t, goFile, "package main\n\nfunc main() {}\n")

	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })

	ctx := context.Background()
	goLang, goRoot := workspaceRoot(goFile)
	if _, err := pool.NavigatorFor(ctx, goLang, goRoot); err != nil {
		t.Fatalf("go navigator: %v", err)
	}

	// The go entry must be keyed under ("go", root).
	pool.mu.Lock()
	_, hasGo := pool.entries[keyFor("go", goRoot)]
	_, hasTS := pool.entries[keyFor("typescript", goRoot)]
	pool.mu.Unlock()
	if !hasGo {
		t.Fatal("expected a (go, root) pooled server")
	}
	if hasTS {
		t.Fatal("did not request a typescript server; none should exist yet")
	}

	if serverAvailable(t, "typescript-language-server") {
		tsRoot, _ := filepath.Abs(root)
		if _, err := pool.NavigatorFor(ctx, "typescript", tsRoot); err != nil {
			t.Fatalf("ts navigator: %v", err)
		}
		pool.mu.Lock()
		n := len(pool.entries)
		pool.mu.Unlock()
		if n != 2 {
			t.Fatalf("expected 2 servers (go + typescript) at one root, got %d", n)
		}
	}
}

// TestStackLangRouting pins extension→language→server routing for the corpus's
// real "long-tail" languages (SQL, CFML, the Angular front-end's SCSS/CSS/HTML).
// Pure routing — no server needed, always runs.
func TestStackLangRouting(t *testing.T) {
	cases := []struct{ file, lang, command string }{
		{"q.sql", "sql", "sql-language-server"},
		{"Service.cfc", "cfml", "cflsp"},
		{"index.cfm", "cfml", "cflsp"},
		{"styles.scss", "css", "vscode-css-language-server"},
		{"theme.less", "css", "vscode-css-language-server"},
		{"app.css", "css", "vscode-css-language-server"},
		{"page.html", "html", "vscode-html-language-server"},
		{"Calc.cs", "csharp", "csharp-ls"},
	}
	for _, c := range cases {
		lang, ok := LanguageForPath(c.file)
		if !ok || lang != c.lang {
			t.Errorf("%s: lang=%q ok=%v, want %q", c.file, lang, ok, c.lang)
			continue
		}
		spec, ok := SpecForLanguage(lang)
		if !ok || spec.Command != c.command {
			t.Errorf("%s: command=%q, want %q", c.file, spec.Command, c.command)
		}
	}
}

// TestCSS_Definition exercises a real SCSS go-to-definition (the strongest of the
// extra servers): a $variable usage resolves to its declaration. Skips without
// vscode-css-language-server.
func TestCSS_Definition(t *testing.T) {
	if !serverAvailable(t, "vscode-css-language-server") {
		t.Skip("vscode-css-language-server not on PATH; skipping SCSS def test")
	}
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "package.json"), `{"name":"x"}`)
	scss := filepath.Join(root, "styles.scss")
	mustWrite(t, scss, "$primary: #336699;\n.btn {\n  color: $primary;\n}\n")

	if lang, _ := workspaceRoot(scss); lang != "css" {
		t.Fatalf("scss routed to %q, want css", lang)
	}
	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })

	at := identPos(t, scss, "color: $primary", "$primary")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	locs, err := pool.Definition(ctx, at)
	if err != nil {
		t.Fatalf("scss definition: %v", err)
	}
	if len(locs) == 0 || locs[0].Start.Line != 1 {
		t.Errorf("expected $primary to resolve to its decl on line 1, got %v", locs)
	}
}

// TestCFML_Definition exercises CFML go-to-definition via cflsp (built from
// softwareCobbler/cfc): a function call resolves to its declaration. cflsp
// implements definition only (no references/implementation). Skips without cflsp.
func TestCFML_Definition(t *testing.T) {
	if !serverAvailable(t, "cflsp") {
		t.Skip("cflsp not on PATH; skipping CFML def test (build softwareCobbler/cfc to enable)")
	}
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Application.cfc"), "component {}\n")
	src := filepath.Join(root, "Calc.cfc")
	mustWrite(t, src, "component {\n    function add(a, b) {\n        return a + b;\n    }\n    function run() {\n        return add(2, 3);\n    }\n}\n")

	if lang, _ := workspaceRoot(src); lang != "cfml" {
		t.Fatalf("cfc routed to %q, want cfml", lang)
	}
	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })

	at := identPos(t, src, "return add(2, 3)", "add")
	deadline := time.Now().Add(30 * time.Second)
	var defLine int
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		locs, err := pool.Definition(ctx, at)
		cancel()
		if err != nil {
			t.Fatalf("cfml definition: %v", err)
		}
		if len(locs) > 0 {
			defLine = locs[0].Start.Line
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if defLine != 2 {
		t.Errorf("expected add() to resolve to its decl on line 2, got line %d", defLine)
	}
}

// TestSQLGracefulDegrade proves a server lacking a nav method (sql-language-server
// implements no definition/references) degrades to empty results, NOT an error
// (the -32601 MethodNotFound handling). Skips without the server.
func TestSQLGracefulDegrade(t *testing.T) {
	if !serverAvailable(t, "sql-language-server") {
		t.Skip("sql-language-server not on PATH; skipping SQL degrade test")
	}
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".sqllsrc.json"), `{"connections":[]}`)
	src := filepath.Join(root, "q.sql")
	mustWrite(t, src, "SELECT id FROM users WHERE id = 1;\n")

	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Must NOT error with -32601; returns empty because SQL has no definition.
	locs, err := pool.Definition(ctx, Pos{File: src, Line: 1, Col: 8})
	if err != nil {
		t.Fatalf("expected graceful empty result, got error: %v", err)
	}
	if len(locs) != 0 {
		t.Logf("note: sql-language-server returned %d locations (unexpected but not a failure)", len(locs))
	}
}
