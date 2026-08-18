package search_test

import (
	"context"
	"testing"

	"moedex/internal/search"
)

// TestLiteralPositionalRejectsCrossLineSpan is the regression test for F-11:
// LiteralWithStats's positional begin/end-gram fast path verifies a candidate
// with bytes.Equal against the blob's raw, unsplit content and attributes the
// match to a single line via LineOf, without checking that the matched span
// stays within that line. A literal containing a raw newline byte is long
// enough (>= trigram.N) to take this fast path instead of the line-bounded
// scan fallback, and can byte-match content that straddles two real lines —
// a false positive ripgrep's line-oriented default would never report, since
// no single line ever contains a '\n' byte for the pattern to match against.
func TestLiteralPositionalRejectsCrossLineSpan(t *testing.T) {
	blobs := map[string]string{
		"a.txt": "food\nbar\n",
	}
	ix := buildIndex(blobs)

	// "od\nb" is 4 bytes (>= trigram.N == 3), so it takes the positional
	// begin/end-gram path rather than the line-bounded scan fallback. Its
	// begin-gram "od\n" and end-gram "d\nb" both occur in the blob at the
	// offsets the merge-join expects, so the candidate reaches the final
	// bytes.Equal check — which must reject it because the span crosses the
	// "food" / "bar" line boundary.
	got, err := search.Literal(context.Background(), ix, "od\nb")
	if err != nil {
		t.Fatalf("Literal: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Literal(%q) = %v, want no matches (query straddles two lines, which ripgrep's line-oriented default never matches)", "od\nb", got)
	}

	// Sanity check the fast path still finds an ordinary, single-line, >=3
	// byte literal so the new bound doesn't just reject everything.
	got, err = search.Literal(context.Background(), ix, "foo")
	if err != nil {
		t.Fatalf("Literal: %v", err)
	}
	if len(got) != 1 || got[0].Line != 1 {
		t.Errorf(`Literal("foo") = %v, want one match on line 1`, got)
	}
}
