// Package fold computes ASCII case-fold variants for a rune, shared by the
// Cox trigram reduction ([moedex/internal/query]) and the search-time verify
// prefilter ([moedex/internal/search]). Both need the identical answer for
// every rune: a fork between them is a soundness bug (the candidate set and
// the verifier would disagree, producing a ripgrep-parity mismatch).
package fold

import "unicode"

// ASCIIVariants returns the distinct ASCII bytes a single rune can take under
// Go's `(?i)` folding, plus whether its ENTIRE fold orbit stays ASCII. The
// orbit is the unicode.SimpleFold cycle; ASCII letters whose orbit leaves
// ASCII (k -> U+212A KELVIN SIGN, s -> U+017F LATIN SMALL LETTER LONG S)
// report allASCII=false so callers skip positions that include them rather
// than under-approximate.
func ASCIIVariants(r rune) (bytes []byte, allASCII bool) {
	allASCII = true
	for c := r; ; {
		if c < 0x80 {
			bytes = append(bytes, byte(c))
		} else {
			allASCII = false
		}
		c = unicode.SimpleFold(c)
		if c == r {
			break // SimpleFold cycles back to the start
		}
	}
	return bytes, allASCII
}
