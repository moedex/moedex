package verify

import (
	"fmt"
	"strings"
	"testing"
)

// Keep the old whole-blob implementation as an independent differential oracle.
func wholeBlobCSharpUsingAt(content []byte, mask []region, start, end int) bool {
	if mask[start] != regionCode {
		return false
	}
	for _, match := range csUsingRE.FindAllIndex(content, -1) {
		if match[0] < len(mask) && mask[match[0]] == regionCode && start >= match[0] && end <= match[1] {
			return true
		}
	}
	return false
}

func TestCSharpUsingLineMatchesWholeBlob(t *testing.T) {
	cases := []string{
		"using Alpha.Beta;\nclass C { void Method() { Alpha(); } }\n",
		"\tglobal using static global::Alpha . Beta;\r\nusing Alias = Other.Type;\n",
		"using Alpha;\rusing Beta;\r\nusing Gamma;\r\n  using Delta; trailing\n",
		"class C {}\rusing Alpha;\rusing Beta;",
		"// using Fake;\n/* using Other;\nusing Inside; */\nusing Real;\n",
		"var text = @\"\nusing Quoted;\n\";\nusing Visible;\n",
		"using\nAlpha;\nusing Alpha\n.Beta;\nusing Alias\n= Alpha;\nusing static\nAlpha;\n",
		"using Alpha; using Beta;\nusing Alias=Alpha.Beta;\nusing global::Alpha;\n",
		"using ;\nusing 42;\nusing Alpha<Generic>;\nusing var item = Value;\n",
		"/* unfinished\nusing Alpha;\n",
		"using Alpha;\n\nusing Beta;",
		"\n\r\n\r",
	}
	for i, input := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			content := []byte(input)
			mask := classifyRegions(content, langCSharp)
			// Include zero-length and cross-line ranges, not only valid identifiers,
			// to pin down exact LF/CR boundary and regexp containment behavior.
			for start := 0; start < len(content); start++ {
				for end := start; end <= len(content); end++ {
					got, want := csharpUsingAt(content, mask, start, end), wholeBlobCSharpUsingAt(content, mask, start, end)
					if got != want {
						t.Fatalf("range [%d:%d] in %q: line=%v whole=%v", start, end, input, got, want)
					}
				}
			}
		})
	}
}

func BenchmarkCSharpUsingNoImports(b *testing.B) {
	content := []byte("class Fixture {\n" + strings.Repeat("void Method() { Target(); }\n", 10000) + "}\n")
	mask := classifyRegions(content, langCSharp)
	starts := make([]int, 0, 100)
	for offset := 0; len(starts) < 100; {
		next := strings.Index(string(content[offset:]), "Target")
		if next < 0 {
			b.Fatal("not enough call sites")
		}
		offset += next
		starts = append(starts, offset)
		offset += len("Target")
	}
	for _, tc := range []struct {
		name string
		fn   func([]byte, []region, int, int) bool
	}{{"whole_blob", wholeBlobCSharpUsingAt}, {"current_line", csharpUsingAt}} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				for _, start := range starts {
					if tc.fn(content, mask, start, start+6) {
						b.Fatal("unexpected import")
					}
				}
			}
		})
	}
}
