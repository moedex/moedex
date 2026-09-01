//go:build lsp

package navcmd

import (
	"testing"

	"moedex/internal/navigate"
)

// TestParsePos_WindowsDriveLetterPath proves parsePos parses trailing
// :LINE[:COL] from the right instead of splitting the whole argument on
// every ':' — a naive whole-string split mis-parses Windows absolute paths
// like "C:\x.go:10:2" because the drive-letter colon is indistinguishable
// from the FILE/LINE separator.
func TestParsePos_WindowsDriveLetterPath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want navigate.Pos
	}{
		{"posix with col", "foo.go:10:2", navigate.Pos{File: "foo.go", Line: 10, Col: 2}},
		{"posix without col", "foo.go:10", navigate.Pos{File: "foo.go", Line: 10, Col: 1}},
		{"windows with col", `C:\x.go:10:2`, navigate.Pos{File: `C:\x.go`, Line: 10, Col: 2}},
		{"windows without col", `C:\x.go:10`, navigate.Pos{File: `C:\x.go`, Line: 10, Col: 1}},
		{"windows nested path", `C:\src\pkg\x.go:42:6`, navigate.Pos{File: `C:\src\pkg\x.go`, Line: 42, Col: 6}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parsePos(c.in)
			if err != nil {
				t.Fatalf("parsePos(%q) returned error: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("parsePos(%q) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

func TestParsePos_RejectsMissingLine(t *testing.T) {
	if _, err := parsePos("foo.go"); err == nil {
		t.Errorf("parsePos(%q) expected an error, got nil", "foo.go")
	}
}
