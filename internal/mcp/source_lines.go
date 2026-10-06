package mcp

import (
	"fmt"
	"strings"
)

// FormatSourceLines renders citation aids for the human-readable MCP fallback.
// The structured source must remain untouched: these prefixes are not source
// bytes and must never be used for hashing or compiler offsets.
func FormatSourceLines(text string, start, end int) string {
	if text == "" || start < 1 || end < start {
		return text
	}
	lines := strings.SplitAfter(text, "\n")
	// The empty segment after a terminal LF is not a physical source line,
	// even if a custom provider's coordinates include it.
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > end-start+1 {
		// A custom provider supplied inconsistent coordinates. Preserve its text
		// rather than inventing line numbers or silently dropping source.
		return text
	}
	var b strings.Builder
	for i, line := range lines {
		if strings.HasSuffix(line, "\n") {
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		}
		fmt.Fprintf(&b, "%d | %s\n", start+i, line)
	}
	return b.String()
}
