package searchservice

import (
	"strings"
	"testing"

	"moedex/internal/contextwin"
)

func TestFormatMarkdownIncludesContextAndGraphDegradation(t *testing.T) {
	t.Parallel()
	got := FormatMarkdown(Result{
		Query: "refund handler",
		Window: contextwin.ContextWindow{
			TokenEstimate: 42,
			Blocks: []contextwin.ContextBlock{{
				Repo: "payments", RelPath: "refund.go", StartLine: 10, EndLine: 12,
				Text: "func Refund() {}\n", Score: 1.25, Lexical: 0.8,
			}},
		},
	})
	for _, want := range []string{"# Moe search: refund handler", "`payments/refund.go:10-12`", "```go", "Graph sidecar unavailable"} {
		if !strings.Contains(got, want) {
			t.Fatalf("markdown missing %q:\n%s", want, got)
		}
	}
}

func TestLanguageFor(t *testing.T) {
	t.Parallel()
	if got := languageFor("app.tsx"); got != "tsx" {
		t.Fatalf("languageFor(app.tsx) = %q", got)
	}
	if got := languageFor("LICENSE"); got != "text" {
		t.Fatalf("languageFor(LICENSE) = %q", got)
	}
}
