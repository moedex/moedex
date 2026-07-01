// No build tag: Symbol is pure data (like Location/Pos in navigate.go) and must
// compile and be testable in both build arms without a language server.

package navigate

import "testing"

// TestSymbol_String pins the ADR 0018 output format — name TAB kind TAB
// file:line:col — since find_symbol/symbols_overview MCP tools and Protostar's
// moedex-nav parser both key off this exact shape.
func TestSymbol_String(t *testing.T) {
	s := Symbol{
		Name: "Circle",
		Kind: "Struct",
		Loc: Location{
			File:  "shape.go",
			Start: Pos{File: "shape.go", Line: 9, Col: 6},
		},
	}
	want := "Circle\tStruct\tshape.go:9:6"
	if got := s.String(); got != want {
		t.Errorf("Symbol.String() = %q, want %q", got, want)
	}
}
