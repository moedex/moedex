//go:build lsp

package navigate

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// These tests are the ADR 0018 evidence at the LSP-client layer: name-based
// lookup (workspace/symbol) and file-scoped enumeration (textDocument/
// documentSymbol) against a real gopls, mirroring lsp_test.go's position-based
// tests. Skipped automatically when gopls is not on PATH.

// querySymbolsWithRetry mirrors queryWithRetry (lsp_test.go) for the Symbol
// return shape: gopls may still be loading the workspace right after didOpen.
func querySymbolsWithRetry(t *testing.T, fn func(context.Context) ([]Symbol, error)) []Symbol {
	t.Helper()
	var last []Symbol
	for attempt := 0; attempt < 8; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		syms, err := fn(ctx)
		cancel()
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(syms) > 0 {
			return syms
		}
		last = syms
		time.Sleep(750 * time.Millisecond)
	}
	return last
}

// TestDecodeSymbolResult_MethodNotFound_ReturnsEmptyNotError pins the ADR 0018
// capability-honesty clause directly against decodeSymbolResult: a JSON-RPC
// -32601 (method not found) — the real gopls used elsewhere in this file
// implements both methods, so it can never produce this response — must
// degrade to (nil, nil), never a returned error, so a language server without
// name lookup fails soft rather than breaking the tool call. No server needed.
func TestDecodeSymbolResult_MethodNotFound_ReturnsEmptyNotError(t *testing.T) {
	for _, method := range []string{"workspace/symbol", "textDocument/documentSymbol"} {
		syms, err := decodeSymbolResult(nil, &rpcError{Code: codeMethodNotFound, Message: "unimplemented"}, method, "")
		if err != nil {
			t.Errorf("%s: err = %v, want nil", method, err)
		}
		if syms != nil {
			t.Errorf("%s: syms = %v, want nil", method, syms)
		}
	}
}

func TestWorkspaceSymbol_FindsCrossFileByName(t *testing.T) {
	root, shapeFile, _ := writeFixture(t)
	nav := newNav(t, root)

	syms := querySymbolsWithRetry(t, func(ctx context.Context) ([]Symbol, error) {
		return nav.WorkspaceSymbol(ctx, "Circle")
	})
	if len(syms) == 0 {
		t.Fatal("WorkspaceSymbol returned no results for \"Circle\"")
	}
	declLine := findPos(t, shapeFile, "type Circle struct", 1).Line
	var found bool
	for _, s := range syms {
		if s.Name == "Circle" && filepath.Base(s.Loc.File) == "shape.go" && s.Loc.Start.Line == declLine {
			found = true
			if s.Kind == "" {
				t.Errorf("Circle symbol has empty Kind")
			}
		}
	}
	if !found {
		t.Errorf("Circle decl (shape.go:%d) not found in workspace/symbol results: %v", declLine, syms)
	}
}

func TestWorkspaceSymbol_UnknownName_ReturnsEmpty(t *testing.T) {
	root, _, _ := writeFixture(t)
	nav := newNav(t, root)

	// A query matching nothing must degrade to empty, not error — no retry loop
	// needed since "no results" is itself the expected steady state.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	syms, err := nav.WorkspaceSymbol(ctx, "ThisSymbolDoesNotExistAnywhere12345")
	if err != nil {
		t.Fatalf("WorkspaceSymbol: %v", err)
	}
	if len(syms) != 0 {
		t.Errorf("expected no results, got %v", syms)
	}
}

func TestDocumentSymbol_FlattensFileDeclarations(t *testing.T) {
	root, shapeFile, _ := writeFixture(t)
	nav := newNav(t, root)

	syms := querySymbolsWithRetry(t, func(ctx context.Context) ([]Symbol, error) {
		return nav.DocumentSymbol(ctx, shapeFile)
	})
	if len(syms) == 0 {
		t.Fatal("DocumentSymbol returned no results")
	}
	names := make(map[string]bool)
	for _, s := range syms {
		names[s.Name] = true
		if filepath.Base(s.Loc.File) != "shape.go" {
			t.Errorf("symbol %q resolved to file %s, want shape.go", s.Name, s.Loc.File)
		}
	}
	for _, want := range []string{"Shape", "Circle", "Area"} {
		if !names[want] {
			t.Errorf("DocumentSymbol missing %q; got %v", want, syms)
		}
	}
}

func TestDocumentSymbol_UsesSelectionRangeNotFullRange(t *testing.T) {
	root, shapeFile, _ := writeFixture(t)
	nav := newNav(t, root)

	syms := querySymbolsWithRetry(t, func(ctx context.Context) ([]Symbol, error) {
		return nav.DocumentSymbol(ctx, shapeFile)
	})
	// The identifier "Circle" sits a few bytes after the line's "type " keyword;
	// the position must land ON the identifier (selectionRange), not on the
	// "type" keyword (the broader declaration range) — otherwise the ADR's
	// two-step name->position->find_references handoff can miss.
	identLine := identPos(t, shapeFile, "type Circle struct", "Circle")
	var found bool
	for _, s := range syms {
		if s.Name != "Circle" {
			continue
		}
		found = true
		if s.Loc.Start.Line != identLine.Line || s.Loc.Start.Col != identLine.Col {
			t.Errorf("Circle symbol position = %d:%d, want identifier position %d:%d", s.Loc.Start.Line, s.Loc.Start.Col, identLine.Line, identLine.Col)
		}
	}
	if !found {
		t.Fatal("Circle not found in DocumentSymbol results")
	}
}
