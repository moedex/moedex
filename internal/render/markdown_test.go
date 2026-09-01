package render

import (
	"strings"
	"testing"
)

func TestMarkdownRendersContent(t *testing.T) {
	t.Parallel()
	got := Markdown("# Result\n\n`symbol`\n", 60)
	if !strings.Contains(got, "Result") || !strings.Contains(got, "symbol") {
		t.Fatalf("rendered markdown lost content: %q", got)
	}
}
