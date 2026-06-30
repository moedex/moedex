package parity

import "testing"

// F-051: regex (non-literal) queries must be wrapped in Zoekt's explicit
// content-regex syntax (content:/.../) so that query-language metacharacters
// inside the corpus-derived pattern (field prefixes, leading '-', spaces,
// parens) can never be reinterpreted as Zoekt query operators.
func TestZoektQueryString_RegexWrappedAsContentRegex(t *testing.T) {
	cases := []struct {
		name string
		q    Query
		want string
	}{
		{
			name: "plain regex",
			q:    Query{Pattern: `foo.*bar`, Literal: false},
			want: `case:yes content:/foo.*bar/`,
		},
		{
			name: "ignore case",
			q:    Query{Pattern: `foo.*bar`, Literal: false, IgnoreCase: true},
			want: `case:no content:/foo.*bar/`,
		},
		{
			name: "embedded field-like token is inert inside the regex atom",
			q:    Query{Pattern: `file:secret`, Literal: false},
			want: `case:yes content:/file:secret/`,
		},
		{
			name: "leading dash does not become negation",
			q:    Query{Pattern: `-rf`, Literal: false},
			want: `case:yes content:/-rf/`,
		},
		{
			name: "internal whitespace does not split atoms",
			q:    Query{Pattern: `foo bar`, Literal: false},
			want: `case:yes content:/foo bar/`,
		},
		{
			name: "internal slash is escaped so it can't close the atom early",
			q:    Query{Pattern: `a/b`, Literal: false},
			want: `case:yes content:/a\/b/`,
		},
		{
			name: "trailing backslash is escaped so it can't swallow the delimiter",
			q:    Query{Pattern: `a\`, Literal: false},
			want: `case:yes content:/a\\/`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := zoektQueryString(c.q)
			if got != c.want {
				t.Fatalf("zoektQueryString(%+v) = %q, want %q", c.q, got, c.want)
			}
		})
	}
}

// Literal queries must remain unaffected by the regex-wrapping change.
func TestZoektQueryString_LiteralUnchanged(t *testing.T) {
	q := Query{Pattern: `file:"weird"`, Literal: true}
	want := `case:yes "file:\"weird\""`
	if got := zoektQueryString(q); got != want {
		t.Fatalf("zoektQueryString(%+v) = %q, want %q", q, got, want)
	}
}
