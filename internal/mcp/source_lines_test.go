package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"moedex/internal/contextwin"
)

func TestSourceLineCitations(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		start, end int
		want       string
	}{
		{"registration after blank", "\nAddScoped<I, C>();\n", 61, 62, "61 | \n62 | AddScoped<I, C>();\n"},
		{"terminal LF is not an extra line", "x\n", 4, 5, "4 | x\n"},
		{"terminal CRLF is not an extra line", "x\r\n", 4, 5, "4 | x\n"},
		{"unterminated final line", "x", 4, 4, "4 | x\n"},
		{"physical blank final line", "x\n\n", 4, 5, "4 | x\n5 | \n"},
		{"standalone CR is source text", "x\r", 4, 4, "4 | x\r\n"},
		{"CRLF and unicode", "α\r\n\r\nβ", 7, 9, "7 | α\n8 | \n9 | β\n"},
		{"clipped last line", "first\npar", 20, 25, "20 | first\n21 | par\n"},
		{"empty file", "", 1, 1, ""},
		{"empty file has no citations", "", 0, 0, ""},
		{"unknown coordinates", "x\n", 0, 0, "x\n"},
		{"inconsistent provider", "x\ny", 1, 1, "x\ny"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatSourceLines(tc.text, tc.start, tc.end); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSearchCitationsPreserveStructuredSource(t *testing.T) {
	const source = "\r\nservices.AddScoped<I, C>();\r\n"
	s := NewServer(&fakeSearcher{win: contextwin.ContextWindow{
		Blocks: []contextwin.ContextBlock{{Repo: "sample", RelPath: "Program.cs", StartLine: 61, EndLine: 62, Text: source}},
	}})
	for _, format := range []string{"text", "structured"} {
		raw, _ := json.Marshal(map[string]any{"name": "search_context", "arguments": map[string]any{"query": "AddScoped", "format": format}})
		result, err := s.callTool(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		block := result["structuredContent"].(structuredWindow).Blocks[0]
		if block.Text != source || block.StartLine != 61 || block.EndLine != 62 {
			t.Fatalf("structured source changed: %+v", block)
		}
		if format == "text" {
			text := result["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
			if !strings.Contains(text, "61 | \n62 | services.AddScoped<I, C>();\n") || strings.Contains(text, "63 |") {
				t.Fatalf("incorrect citation lines: %q", text)
			}
		}
	}
}
