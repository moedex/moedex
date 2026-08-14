package parity

import (
	"testing"
	"unicode/utf8"
)

// TestObserveUnicodeLeftExtensionReachesLineStart guards the boundary the
// left-extension scan in observeUnicode must handle without an
// index-out-of-range: a multibyte literal whose word/multibyte run extends
// all the way to byte 0 of the line. The loop's lo>0 guard must stop the scan
// exactly at lo==0, never reading line[-1]. (See F-054: the original
// condition's operator-precedence trap only happened to stay safe because the
// second clause redundantly re-guarded lo>0; parenthesizing changes the
// grouping, not the boundary behavior, so this test must keep passing.)
func TestObserveUnicodeLeftExtensionReachesLineStart(t *testing.T) {
	p := newTermPool(1)
	line := []byte("caf\xC3\xA9") // "café": ASCII run immediately followed by a 2-byte rune, no left padding
	p.observeUnicode(line)

	want := "café"
	found := false
	for _, u := range p.unicodes {
		if u == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("observeUnicode(%q): want %q in unicodes, got %v", line, want, p.unicodes)
	}
}

func TestObserveUnicodeRejectsInvalidUTF8AndKeepsValidRuns(t *testing.T) {
	p := newTermPool(1)
	line := []byte("Bef\xfcllen caf\xc3\xa9\xffna\xc3\xafve")
	p.observeUnicode(line)

	want := map[string]bool{"café": false, "naïve": false}
	for _, u := range p.unicodes {
		if !utf8.ValidString(u) {
			t.Fatalf("observeUnicode sampled invalid UTF-8 %q", u)
		}
		if _, ok := want[u]; ok {
			want[u] = true
		}
	}
	for u, found := range want {
		if !found {
			t.Errorf("observeUnicode did not retain valid run %q; got %q", u, p.unicodes)
		}
	}
}
