package verify

import "testing"

func TestVerifyCSharpRawDelimiters(t *testing.T) {
	for _, tc := range []struct {
		name, literal string
	}{
		{"four_quotes", "\"\"\"\"\nprefix \"\"\"\nProcessOrder(); // quoted\n\"\"\"\""},
		{"five_quotes", "\"\"\"\"\"\nprefix \"\"\"\"\nProcessOrder(); // quoted\n\"\"\"\"\""},
		{"interpolated_raw", "$$\"\"\"\"\nprefix \"\"\"\nProcessOrder(); // quoted\n\"\"\"\""},
		{"raw_three_quotes", "\"\"\"\nprefix \"\"\nProcessOrder(); // quoted\n\"\"\""},
		{"raw_opening_whitespace", "\"\"\"\" \t\r\nprefix \"\"\"\r\nProcessOrder(); // quoted\r\n\"\"\"\""},
		{"single_line_raw", "\"\"\"\"prefix \"\"\" ProcessOrder()\"\"\"\""},
		{"ordinary", "\"ProcessOrder()\""},
		{"verbatim", "@\"prefix \"\"\nProcessOrder(); // quoted\nC:\\\""},
		{"interpolated_verbatim", "$@\"prefix \"\"\nProcessOrder(); // quoted\nC:\\\""},
		{"verbatim_interpolated", "@$\"prefix \"\"\nProcessOrder(); // quoted\nC:\\\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := buildCandidates(t, fixture{repo: "caller", path: "Caller.cs", src: "var text = " + tc.literal + ";\nProcessOrder(); // real\n"})
			got := Verify(input)
			if len(got) != 2 {
				t.Fatalf("got %d candidates, want literal and real occurrences", len(got))
			}
			for _, edge := range got {
				wantTier, wantKind := Candidate, Unverified
				if edgeLine(edge.Edge) == "ProcessOrder(); // real" {
					wantTier, wantKind = Pattern, Call
				}
				if edge.Confidence != wantTier || edge.Kind != wantKind {
					t.Errorf("%q scored (%s, %s), want (%s, %s)", edgeLine(edge.Edge), edge.Confidence, edge.Kind, wantTier, wantKind)
				}
			}
		})
	}
}

func TestVerifyCSharpUnterminatedSingleLineRawRecovery(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		input := buildCandidates(t, fixture{repo: "caller", path: "Caller.cs", src: "var text = \"\"\"oops ProcessOrder()" + newline + "ProcessOrder(); // real\n"})
		assertScores(t, input, map[string]struct {
			tier Tier
			kind ReferenceKind
		}{
			"var text = \"\"\"oops ProcessOrder()": {Candidate, Unverified},
			"ProcessOrder(); // real":              {Pattern, Call},
		})
	}
}
