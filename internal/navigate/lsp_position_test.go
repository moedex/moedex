//go:build lsp

package navigate

import "testing"

// TestPosToLSP_ClampsBelowOne proves posToLSP never emits a negative LSP
// position. Pos is documented as 1-based (navigate.go:51-52), but exported
// Pool/LSP methods accept any Pos from a direct library caller; Line==0 or
// Col==0 must clamp to LSP's zero floor rather than going negative, since a
// negative line/character is invalid per the LSP spec and server behavior on
// receiving one is undefined.
func TestPosToLSP_ClampsBelowOne(t *testing.T) {
	cases := []struct {
		name string
		in   Pos
		want lspPosition
	}{
		{"normal", Pos{Line: 1, Col: 1}, lspPosition{Line: 0, Character: 0}},
		{"zero col", Pos{Line: 5, Col: 0}, lspPosition{Line: 4, Character: 0}},
		{"zero line", Pos{Line: 0, Col: 5}, lspPosition{Line: 0, Character: 4}},
		{"zero both", Pos{Line: 0, Col: 0}, lspPosition{Line: 0, Character: 0}},
		{"negative line", Pos{Line: -3, Col: 1}, lspPosition{Line: 0, Character: 0}},
		{"negative col", Pos{Line: 1, Col: -3}, lspPosition{Line: 0, Character: 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := posToLSP(c.in)
			if got != c.want {
				t.Errorf("posToLSP(%+v) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}
