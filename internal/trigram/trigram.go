// Package trigram defines moedex's positional-trigram primitive.
//
// A trigram is three consecutive bytes, and offsets are byte offsets. (The
// original Zoekt used rune offsets; a clean-room build uses bytes: content
// stays 1× in memory, the positional-distance delta for a literal is a fixed
// byte length, and candidate verification — which is byte-exact, like ripgrep —
// preserves correctness over UTF-8 regardless of where trigrams straddle rune
// boundaries.)
package trigram

// N is the gram size. Cox's reasoning holds for 2026: too few distinct 2-grams,
// too many distinct 4-grams, so 3-grams it is.
const N = 3

// Trigram is three consecutive bytes.
type Trigram [N]byte

// String renders a trigram for debugging.
func (t Trigram) String() string { return string(t[:]) }
