//go:build lsp

package navigate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests prove ADR 0017 Condition 1: type-resolved navigation that the
// syntactic symbol sidecar (ADR 0008) cannot provide. They drive a real gopls
// against a self-contained throwaway module (no corpus, no network) and assert
// cross-file definition, references, and interface-implementation resolution.
//
// Skipped automatically when gopls is not on PATH, matching the env-gated-skip
// convention used by the dense arm.

const fixtureShape = `package shape

// Shape is the interface whose implementations we navigate to.
type Shape interface {
	Area() float64
}

// Circle is a concrete Shape. References to this type span files.
type Circle struct {
	R float64
}

func (c Circle) Area() float64 {
	return 3.14159 * c.R * c.R
}
`

const fixtureMain = `package main

import "navfix/shape"

func describe(s shape.Shape) float64 {
	return s.Area()
}

func main() {
	a := shape.Circle{R: 2}
	b := shape.Circle{R: 3}
	_ = describe(a)
	_ = describe(b)
}
`

func writeFixture(t *testing.T) (root string, shapeFile, mainFile string) {
	t.Helper()
	root = t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module navfix\n\ngo 1.26\n")
	shapeFile = filepath.Join(root, "shape", "shape.go")
	mustWrite(t, shapeFile, fixtureShape)
	mainFile = filepath.Join(root, "main.go")
	mustWrite(t, mainFile, fixtureMain)
	return root, shapeFile, mainFile
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// findPos locates the nth (1-based) occurrence of needle in file and returns a
// 1-based (line, byte-col) Pos at its start — the cursor the query runs from.
func findPos(t *testing.T, file, needle string, occurrence int) Pos {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	text := string(data)
	idx, seen := -1, 0
	for {
		next := strings.Index(text[idx+1:], needle)
		if next < 0 {
			t.Fatalf("needle %q occurrence %d not found in %s", needle, occurrence, file)
		}
		idx = idx + 1 + next
		seen++
		if seen == occurrence {
			break
		}
	}
	line := 1 + strings.Count(text[:idx], "\n")
	lineStart := strings.LastIndex(text[:idx], "\n") + 1 // -1+1=0 for first line
	col := (idx - lineStart) + 1
	return Pos{File: file, Line: line, Col: col}
}

// identPos points the cursor at ident *inside* a unique anchor string — so we
// land on a real declaration, not an earlier mention of the same word in a
// comment. e.g. anchor "type Circle struct", ident "Circle".
func identPos(t *testing.T, file, anchor, ident string) Pos {
	t.Helper()
	off := strings.Index(anchor, ident)
	if off < 0 {
		t.Fatalf("ident %q not in anchor %q", ident, anchor)
	}
	p := findPos(t, file, anchor, 1)
	p.Col += off
	return p
}

// goplsAvailable reports whether gopls is on PATH; tests skip without it.
func goplsAvailable(t *testing.T) bool {
	t.Helper()
	_, err := exec.LookPath("gopls")
	return err == nil
}

func newNav(t *testing.T, root string) *LSP {
	t.Helper()
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping LSP navigation test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	nav, err := NewLSP(ctx, Config{RootDir: root})
	if err != nil {
		t.Fatalf("NewLSP: %v", err)
	}
	t.Cleanup(func() { _ = nav.Close() })
	return nav
}

// queryWithRetry re-runs a location query a few times: gopls may still be
// loading the workspace right after didOpen, returning empty before it settles.
func queryWithRetry(t *testing.T, fn func(context.Context) ([]Location, error)) []Location {
	t.Helper()
	var last []Location
	for attempt := 0; attempt < 8; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		locs, err := fn(ctx)
		cancel()
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(locs) > 0 {
			return locs
		}
		last = locs
		time.Sleep(750 * time.Millisecond)
	}
	return last
}

func TestDefinition_CrossFile(t *testing.T) {
	root, shapeFile, mainFile := writeFixture(t)
	nav := newNav(t, root)

	// Cursor on `Circle` in `shape.Circle{R: 2}` (main.go) must resolve to the
	// Circle type declaration in shape.go — a cross-file, import-resolved jump.
	at := findPos(t, mainFile, "Circle", 1)
	locs := queryWithRetry(t, func(ctx context.Context) ([]Location, error) {
		return nav.Definition(ctx, at)
	})
	if len(locs) == 0 {
		t.Fatal("Definition returned no locations")
	}
	got := locs[0]
	if filepath.Base(got.File) != "shape.go" {
		t.Errorf("definition resolved to %s, want shape.go", got.File)
	}
	declLine := findPos(t, shapeFile, "type Circle struct", 1).Line
	if got.Start.Line != declLine {
		t.Errorf("definition at line %d, want type Circle decl line %d", got.Start.Line, declLine)
	}
}

func TestReferences_CrossFile(t *testing.T) {
	root, shapeFile, mainFile := writeFixture(t)
	nav := newNav(t, root)

	// Cursor on the `Circle` type declaration; references must span main.go.
	at := identPos(t, shapeFile, "type Circle struct", "Circle")
	locs := queryWithRetry(t, func(ctx context.Context) ([]Location, error) {
		return nav.References(ctx, at, true)
	})
	if len(locs) < 2 {
		t.Fatalf("expected >=2 references to Circle, got %d: %v", len(locs), locs)
	}
	var inMain int
	for _, l := range locs {
		if filepath.Base(l.File) == filepath.Base(mainFile) {
			inMain++
		}
	}
	if inMain < 2 {
		t.Errorf("expected >=2 references in main.go (two Circle literals), got %d", inMain)
	}
}

func TestImplementations_Interface(t *testing.T) {
	root, shapeFile, _ := writeFixture(t)
	nav := newNav(t, root)

	// Cursor on the `Shape` interface declaration; Circle must be reported as an
	// implementation. This is the find_implementations role — type-resolved, not
	// a name match.
	at := identPos(t, shapeFile, "type Shape interface", "Shape")
	locs := queryWithRetry(t, func(ctx context.Context) ([]Location, error) {
		return nav.Implementations(ctx, at)
	})
	if len(locs) == 0 {
		t.Fatal("Implementations returned no locations")
	}
	circleLine := findPos(t, shapeFile, "type Circle struct", 1).Line
	var found bool
	for _, l := range locs {
		if filepath.Base(l.File) == "shape.go" && l.Start.Line == circleLine {
			found = true
		}
	}
	if !found {
		t.Errorf("Circle (shape.go:%d) not reported as a Shape implementation; got %v", circleLine, locs)
	}
}
