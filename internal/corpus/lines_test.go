package corpus

import "testing"

// Characterization tests for firstLine/lastLine, locking down their current
// behavior before factoring them onto a shared direction-parameterized helper
// (F-047): both walk a byte slice split on "\n" and return the first/last line
// that is non-empty after trimming surrounding whitespace.

func TestFirstLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"single line", "hello", "hello"},
		{"leading blank lines", "\n\n  \nhello\nworld", "hello"},
		{"trims surrounding whitespace", "  hello world  \nsecond", "hello world"},
		{"only whitespace", "  \n\t\n   ", ""},
		{"no trailing newline", "first\nsecond", "first"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstLine([]byte(tc.in)); got != tc.want {
				t.Errorf("firstLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLastLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"single line", "hello", "hello"},
		{"trailing blank lines", "hello\nworld\n\n  \n", "world"},
		{"trims surrounding whitespace", "first\n  last line  \n", "last line"},
		{"only whitespace", "  \n\t\n   ", ""},
		{"no trailing newline", "first\nsecond", "second"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lastLine([]byte(tc.in)); got != tc.want {
				t.Errorf("lastLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
