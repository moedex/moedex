package fmindex

import (
	"bytes"
	"index/suffixarray"
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

// bruteLocate is the primary oracle: every 0-based offset where pattern occurs
// in text, found by a linear bytes scan (overlaps included). It is the simplest
// possible reference and deliberately shares no code with the FM-index.
func bruteLocate(text, pattern []byte) []int {
	if len(pattern) == 0 {
		return nil
	}
	var out []int
	for i := 0; i+len(pattern) <= len(text); i++ {
		if bytes.Equal(text[i:i+len(pattern)], pattern) {
			out = append(out, i)
		}
	}
	return out
}

// saLocate is the second, independent oracle: index/suffixarray (SA-IS in
// stdlib). Lookup returns positions in arbitrary order, so we sort. It cannot be
// used when text contains the sentinel, but neither can the FM-index.
func saLocate(text, pattern []byte) []int {
	if len(pattern) == 0 {
		return nil
	}
	ix := suffixarray.New(text)
	pos := ix.Lookup(pattern, -1)
	sort.Ints(pos)
	return pos
}

// assertLocateMatchesOracles builds an FM-index over text and checks that
// Locate and Count agree with BOTH oracles for the given pattern.
func assertLocateMatchesOracles(t *testing.T, text, pattern []byte) {
	t.Helper()
	fm, err := Build(text)
	if err != nil {
		t.Fatalf("Build(%q) error: %v", text, err)
	}
	want := bruteLocate(text, pattern)
	sort.Ints(want)

	gotLoc := fm.Locate(pattern)
	if !equalInts(gotLoc, want) {
		t.Fatalf("Locate(%q) in %q = %v, brute oracle = %v", pattern, text, gotLoc, want)
	}
	if sa := saLocate(text, pattern); !equalInts(sa, want) {
		t.Fatalf("suffixarray oracle disagrees with brute oracle for %q in %q: sa=%v brute=%v", pattern, text, sa, want)
	}
	if gotCnt := fm.Count(pattern); gotCnt != len(want) {
		t.Fatalf("Count(%q) in %q = %d, want %d", pattern, text, gotCnt, len(want))
	}
}

func equalInts(a, b []int) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func TestBuildRejectsNUL(t *testing.T) {
	for _, in := range [][]byte{
		{0x00},
		[]byte("ab\x00cd"),
		append([]byte("trailing"), 0x00),
	} {
		if _, err := Build(in); err != ErrNULInput {
			t.Errorf("Build(%q): err = %v, want ErrNULInput", in, err)
		}
	}
}

func TestBuildRejectsBadParams(t *testing.T) {
	if _, err := BuildWithParams([]byte("abc"), 0, 4); err == nil {
		t.Error("BuildWithParams with saCheckpoint=0 should error")
	}
	if _, err := BuildWithParams([]byte("abc"), 4, 0); err == nil {
		t.Error("BuildWithParams with saSample=0 should error")
	}
}

func TestEmptyText(t *testing.T) {
	fm, err := Build(nil)
	if err != nil {
		t.Fatalf("Build(nil) error: %v", err)
	}
	if fm.Len() != 0 {
		t.Errorf("Len() = %d, want 0", fm.Len())
	}
	if got := fm.Locate([]byte("x")); got != nil {
		t.Errorf("Locate on empty text = %v, want nil", got)
	}
	if got := fm.Count([]byte("x")); got != 0 {
		t.Errorf("Count on empty text = %d, want 0", got)
	}
	// Empty pattern must return empty/0 like suffixarray.Lookup("").
	if got := fm.Locate(nil); got != nil {
		t.Errorf("Locate(nil) = %v, want nil", got)
	}
	if got := fm.Count(nil); got != 0 {
		t.Errorf("Count(nil) = %d, want 0", got)
	}
}

func TestEmptyPatternOnNonEmptyText(t *testing.T) {
	fm, _ := Build([]byte("hello"))
	// suffixarray.Lookup("", -1) returns no positions; we match that.
	if got := fm.Locate([]byte("")); got != nil {
		t.Errorf("Locate(\"\") = %v, want nil", got)
	}
	if got := fm.Count([]byte("")); got != 0 {
		t.Errorf("Count(\"\") = %d, want 0", got)
	}
}

func TestAdversarialInputs(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		pattern string
	}{
		{"repetitive run hit", "aaaaaaa", "aa"},
		{"repetitive run miss", "aaaaaaa", "ab"},
		{"overlapping aa in aaaa", "aaaa", "aa"},      // must be [0,1,2]
		{"overlapping aba", "abababab", "aba"},        // overlaps at 0,2,4
		{"single byte text hit", "x", "x"},
		{"single byte text miss", "x", "y"},
		{"pattern equals whole text", "moedex", "moedex"},
		{"pattern longer than text", "ab", "abc"},
		{"pattern at very start", "needle in haystack", "needle"},
		{"pattern at very end", "haystack needle", "needle"},
		{"all distinct bytes", "abcdefg", "cde"},
		{"miss entirely", "abcdefg", "xyz"},
		{"newlines in text", "line1\nline2\nline1\n", "line1"},
		{"pattern is newline", "a\nb\nc\n", "\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertLocateMatchesOracles(t, []byte(c.text), []byte(c.pattern))
		})
	}
}

// TestHighByteValues confirms the 256-byte alphabet and UNSIGNED byte ordering.
// A signed-byte comparison (the classic FM-index bug) would mis-sort 0x80-0xFF.
func TestHighByteValues(t *testing.T) {
	// Text mixing low and high bytes; note: deliberately NOT valid UTF-8, which
	// is fine — the FM-index is byte-oriented like moedex's verify model.
	text := []byte{0x01, 0xFF, 0x80, 0x01, 0xFF, 0x7F, 0x80, 0xFF}
	patterns := [][]byte{
		{0xFF},
		{0x80},
		{0x01, 0xFF},
		{0xFF, 0x80},
		{0x7F, 0x80},
		{0x00 ^ 0x80}, // 0x80 again, distinct literal
	}
	for _, p := range patterns {
		assertLocateMatchesOracles(t, text, p)
	}
}

// TestLocateMatchesOracle is the core property test: random texts over alphabets
// of increasing size, with patterns drawn both from random bytes (mostly misses)
// and from real substrings of the text (guaranteed hits). Seeded for
// reproducibility.
func TestLocateMatchesOracle(t *testing.T) {
	alphabets := [][]byte{
		[]byte("ab"),
		[]byte("abc"),
		[]byte("abcdef"),
		highByteAlphabet(), // 64 distinct bytes incl. 0x80-0xFF, excl. 0x00
	}
	rng := rand.New(rand.NewSource(0xF1DE7))
	const iterations = 1200
	for it := 0; it < iterations; it++ {
		alpha := alphabets[rng.Intn(len(alphabets))]
		// Lengths include 0 and 1 to exercise edge cases.
		n := rng.Intn(60)
		text := make([]byte, n)
		for i := range text {
			text[i] = alpha[rng.Intn(len(alpha))]
		}

		var pattern []byte
		if n > 0 && rng.Intn(2) == 0 {
			// Substring of text: guaranteed-hit case (also exercises overlaps).
			start := rng.Intn(n)
			plen := 1 + rng.Intn(n-start)
			pattern = append([]byte(nil), text[start:start+plen]...)
		} else {
			// Random pattern over the alphabet: mostly misses, sometimes hits.
			plen := 1 + rng.Intn(5)
			pattern = make([]byte, plen)
			for i := range pattern {
				pattern[i] = alpha[rng.Intn(len(alpha))]
			}
		}
		assertLocateMatchesOracles(t, text, pattern)
	}
}

func highByteAlphabet() []byte {
	var a []byte
	for b := 1; b < 256; b += 4 { // skip 0x00; sample across the whole range
		a = append(a, byte(b))
	}
	return a
}

// TestSamplingRateInvariance asserts Locate/Count results are identical across
// SA sample strides and rank checkpoint strides — correctness must not depend on
// sampling density. This catches LF-walk / sampled-row / checkpoint-stride bugs.
func TestSamplingRateInvariance(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	text := make([]byte, 400)
	for i := range text {
		text[i] = byte('a' + rng.Intn(4))
	}
	pattern := []byte("aba")
	want := bruteLocate(text, pattern)
	sort.Ints(want)

	checkpoints := []int{1, 2, 7, 64, 1000}
	samples := []int{1, 2, 4, 17, 500}
	for _, cp := range checkpoints {
		for _, s := range samples {
			fm, err := BuildWithParams(text, cp, s)
			if err != nil {
				t.Fatalf("BuildWithParams(cp=%d,s=%d) error: %v", cp, s, err)
			}
			if got := fm.Locate(pattern); !equalInts(got, want) {
				t.Errorf("Locate with cp=%d s=%d = %v, want %v", cp, s, got, want)
			}
			if got := fm.Count(pattern); got != len(want) {
				t.Errorf("Count with cp=%d s=%d = %d, want %d", cp, s, got, len(want))
			}
		}
	}
}

// TestLFIsPermutation (white-box) asserts LF maps the n rows bijectively onto
// the n rows — a fundamental BWT invariant. A non-permutation means the C[] /
// rank wiring is wrong.
func TestLFIsPermutation(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	text := make([]byte, 200)
	for i := range text {
		text[i] = byte('a' + rng.Intn(5))
	}
	fm, err := Build(text)
	if err != nil {
		t.Fatal(err)
	}
	seen := make([]bool, fm.n)
	for row := 0; row < fm.n; row++ {
		dst := fm.lf(row)
		if dst < 0 || dst >= fm.n {
			t.Fatalf("lf(%d) = %d out of range [0,%d)", row, dst, fm.n)
		}
		if seen[dst] {
			t.Fatalf("lf is not a permutation: row %d collides at %d", row, dst)
		}
		seen[dst] = true
	}
}

// TestRankConsistency (white-box) asserts the checkpointed rank equals a
// brute-force rank at every position and for every symbol present, across block
// boundaries. This catches the checkpoint-stride off-by-one.
func TestRankConsistency(t *testing.T) {
	rng := rand.New(rand.NewSource(123))
	text := make([]byte, 300)
	for i := range text {
		text[i] = byte('a' + rng.Intn(6))
	}
	// Small checkpoint stride so many block boundaries are crossed.
	fm, err := BuildWithParams(text, 5, 8)
	if err != nil {
		t.Fatal(err)
	}
	symbols := map[byte]bool{}
	for _, b := range fm.bwt {
		symbols[b] = true
	}
	for sym := range symbols {
		brute := 0
		for pos := 0; pos <= fm.n; pos++ {
			if got := fm.rank(sym, pos); got != brute {
				t.Fatalf("rank(%q, %d) = %d, brute = %d", sym, pos, got, brute)
			}
			if pos < fm.n && fm.bwt[pos] == sym {
				brute++
			}
		}
	}
}

// TestRealisticCodeText runs the property check on code-like text with the
// punctuation and repetition source code actually contains.
func TestRealisticCodeText(t *testing.T) {
	text := []byte(`func (ix *Index) Postings(t trigram.Trigram) []Posting {
	if ix.pp != nil {
		return ix.pp.Postings(t)
	}
	return ix.postings[t]
}`)
	for _, p := range []string{"func", "return", "Postings", "ix.pp", "{", "Trigram", "nonexistent"} {
		assertLocateMatchesOracles(t, text, []byte(p))
	}
}
