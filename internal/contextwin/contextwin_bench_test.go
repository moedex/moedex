package contextwin

import (
	"strconv"
	"strings"
	"testing"

	"moedex/internal/index"
	"moedex/internal/rank"
)

func BenchmarkBlobViewImportDominatedDeep(b *testing.B) {
	var src strings.Builder
	for i := 1; i <= 5000; i++ {
		if i >= 4000 && i <= 4010 {
			src.WriteString("using Product.Dependency;\n")
		} else {
			src.WriteString("implementation();\n")
		}
	}
	view := newBlobView([]byte(src.String()))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !view.importDominated(4000, 4010) {
			b.Fatal("deep import range was not classified")
		}
	}
}

func BenchmarkAssembleDeepCandidates(b *testing.B) {
	var src strings.Builder
	for i := 1; i <= 5000; i++ {
		src.WriteString("implementation_line_")
		src.WriteString(strconv.Itoa(i))
		src.WriteByte('\n')
	}
	content := src.String()
	ix := index.New()
	ix.AddFile("r", "deep.go", "/abs/deep.go", sha(content), []byte(content))
	files := []index.FileRef{ref("r", "deep.go", "/abs/deep.go")}
	results := make([]rank.RankedResult, 0, 100)
	for line := 3000; line < 5000; line += 20 {
		results = append(results, rank.RankedResult{
			Blob:      0,
			Files:     files,
			Score:     float64(line),
			LineSpans: []rank.LineSpan{span(line, line)},
		})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		win := Assemble(ix, results, Options{TokenBudget: 100000, ContextLines: 1})
		if len(win.Blocks) != len(results) {
			b.Fatalf("blocks = %d, want %d", len(win.Blocks), len(results))
		}
	}
}
