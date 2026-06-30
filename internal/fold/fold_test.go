package fold

import (
	"slices"
	"testing"
)

func sorted(b []byte) []byte {
	out := append([]byte(nil), b...)
	slices.Sort(out)
	return out
}

func TestASCIIVariants(t *testing.T) {
	cases := []struct {
		name     string
		r        rune
		want     []byte
		allASCII bool
	}{
		{"lower letter", 'a', []byte("aA"), true},
		{"upper letter", 'P', []byte("Pp"), true},
		{"digit has no fold orbit", '5', []byte("5"), true},
		// k's fold orbit includes U+212A KELVIN SIGN, so it must report
		// allASCII=false even though 'k' and 'K' themselves are ASCII.
		{"k orbit leaves ASCII via Kelvin sign", 'k', []byte("kK"), false},
		{"K orbit leaves ASCII via Kelvin sign", 'K', []byte("kK"), false},
		// s's fold orbit includes U+017F LATIN SMALL LETTER LONG S.
		{"s orbit leaves ASCII via long s", 's', []byte("sS"), false},
		{"S orbit leaves ASCII via long s", 'S', []byte("sS"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotBytes, gotASCII := ASCIIVariants(c.r)
			if string(sorted(gotBytes)) != string(sorted(c.want)) {
				t.Errorf("ASCIIVariants(%q) bytes = %q, want %q", c.r, gotBytes, c.want)
			}
			if gotASCII != c.allASCII {
				t.Errorf("ASCIIVariants(%q) allASCII = %v, want %v", c.r, gotASCII, c.allASCII)
			}
		})
	}
}

// A non-ASCII rune with an all-non-ASCII orbit still reports each byte it
// would take only when that fold member is itself < 0x80; here none are, so
// bytes should be empty and allASCII false.
func TestASCIIVariantsNonASCIIRune(t *testing.T) {
	gotBytes, gotASCII := ASCIIVariants('ſ') // U+017F, folds with s/S only
	if gotASCII {
		t.Errorf("ASCIIVariants('ſ') allASCII = true, want false")
	}
	want := []byte("sS")
	if string(sorted(gotBytes)) != string(sorted(want)) {
		t.Errorf("ASCIIVariants('ſ') bytes = %q, want %q", gotBytes, want)
	}
}
