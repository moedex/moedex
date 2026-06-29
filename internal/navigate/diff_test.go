//go:build lsp

package navigate

import (
	"encoding/json"
	"testing"
)

// These tests are server-free: they exercise the pure range-diff core
// (diffRange / offsetToPos / parseSyncKind) that produces incremental
// textDocument/didChange contentChanges. They always run under `make test-lsp`
// (no goplsAvailable skip) — the deterministic proof that the byte-based,
// utf-8-gated range math is correct, independent of any language server.

// applyRange splices old[:startByte] + text + old[endByte:], where the byte
// offsets are recovered by re-walking lines/columns of the LSP range against
// old. It is the inverse of diffRange: applying the returned change to old must
// reconstruct new exactly. This locks the "faithful single-span replacement"
// property.
func applyRange(old []byte, rng lspRange, text string) []byte {
	startByte := posToOffset(old, rng.Start)
	endByte := posToOffset(old, rng.End)
	out := make([]byte, 0, startByte+len(text)+(len(old)-endByte))
	out = append(out, old[:startByte]...)
	out = append(out, text...)
	out = append(out, old[endByte:]...)
	return out
}

// posToOffset converts a 0-based LSP position (utf-8 byte character) back to a
// byte offset into buf — the inverse of offsetToPos.
func posToOffset(buf []byte, p lspPosition) int {
	line, col := 0, 0
	for i := 0; i < len(buf); i++ {
		if line == p.Line && col == p.Character {
			return i
		}
		if buf[i] == '\n' {
			if line == p.Line {
				// Target line shorter than requested character: clamp at newline.
				return i
			}
			line++
			col = 0
		} else {
			col++
		}
	}
	return len(buf)
}

func TestDiffRange(t *testing.T) {
	cases := []struct {
		name   string
		old    string
		new    string
		wantOK bool
	}{
		{"identical", "abc\ndef\n", "abc\ndef\n", false},
		{"append at EOF", "abc\n", "abc\nxyz\n", true},
		{"single char mid-line", "abc\ndef\n", "abc\ndXf\n", true},
		{"whole line insertion", "a\nb\n", "a\nNEW\nb\n", true},
		{"deletion", "abc\ndef\nghi\n", "abc\nghi\n", true},
		{"utf8 edit after multibyte", "héllo world\n", "héllo WORLD\n", true},
		{"prefix suffix overlap insert", "ab", "aXb", true},
		{"edit on later line", "l0\nl1\nl2\nl3\n", "l0\nl1\nlEDIT\nl3\n", true},
		{"empty to content", "", "hello", true},
		{"content to empty", "hello", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old, new := []byte(tc.old), []byte(tc.new)
			rng, text, ok := diffRange(old, new)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			// Reconstruction property: applying the change to old yields new.
			got := applyRange(old, rng, text)
			if string(got) != tc.new {
				t.Fatalf("reconstruct: got %q, want %q (range=%+v text=%q)", got, tc.new, rng, text)
			}
		})
	}
}

// TestDiffRange_UTF8ByteColumns locks the contract that characters are BYTE
// columns, not rune columns: an edit after a 2-byte rune (é) must report the
// character as the byte count, and the multibyte prefix must be untouched.
func TestDiffRange_UTF8ByteColumns(t *testing.T) {
	old := []byte("héllo\n") // 'é' is 2 bytes (0xC3 0xA9): bytes h(0) é(1,2) l(3) l(4) o(5)
	new := []byte("héXlo\n") // replace first 'l' with 'X' at byte offset 3
	rng, text, ok := diffRange(old, new)
	if !ok {
		t.Fatal("expected a change")
	}
	if rng.Start.Line != 0 || rng.End.Line != 0 {
		t.Fatalf("line should be 0, got start=%d end=%d", rng.Start.Line, rng.End.Line)
	}
	// The 'l' sits at byte column 3 (h=0, é occupies bytes 1-2, l=3) — proving
	// byte not rune counting (rune column would be 2).
	if rng.Start.Character != 3 {
		t.Fatalf("start character should be byte column 3, got %d", rng.Start.Character)
	}
	if rng.End.Character != 4 {
		t.Fatalf("end character should be byte column 4, got %d", rng.End.Character)
	}
	if text != "X" {
		t.Fatalf("text = %q, want %q", text, "X")
	}
	if got := applyRange(old, rng, text); string(got) != string(new) {
		t.Fatalf("reconstruct: got %q, want %q", got, new)
	}
}

func TestOffsetToPos(t *testing.T) {
	buf := []byte("ab\ncde\nf")
	cases := []struct {
		off       int
		line, col int
	}{
		{0, 0, 0},
		{1, 0, 1},
		{2, 0, 2},  // the '\n' itself, still line 0
		{3, 1, 0},  // first char after newline
		{6, 1, 3},  // the second '\n', line 1
		{7, 2, 0},  // 'f'
		{8, 2, 1},  // EOF
		{99, 2, 1}, // clamped past EOF
	}
	for _, tc := range cases {
		p := offsetToPos(buf, tc.off)
		if p.Line != tc.line || p.Character != tc.col {
			t.Errorf("offsetToPos(%d) = (%d,%d), want (%d,%d)", tc.off, p.Line, p.Character, tc.line, tc.col)
		}
	}
}

func TestParseSyncKind(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"int none", `0`, 1}, // None advertised for our outbound changes -> Full
		{"int full", `1`, 1},
		{"int incremental", `2`, 2},
		{"object change 2", `{"openClose":true,"change":2}`, 2},
		{"object change 1", `{"openClose":true,"change":1}`, 1},
		{"object no change", `{"openClose":true}`, 1}, // default Full
		{"null", `null`, 1},
		{"absent", ``, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw json.RawMessage
			if tc.raw != "" {
				raw = json.RawMessage(tc.raw)
			}
			if got := parseSyncKind(raw); got != tc.want {
				t.Errorf("parseSyncKind(%q) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}
