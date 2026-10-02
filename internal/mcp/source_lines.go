package mcp

import (
	"fmt"
	"strings"
)

// FormatSourceLines renders citation aids for the human-readable MCP fallback.
// The structured source must remain untouched: these prefixes are not source
// bytes and must never be used for hashing or compiler offsets.
func FormatSourceLines(text string, start, end int) string {
	if start < 1 || end < start {
		return text
	}
	lines := strings.Split(text, "\n")
	// Search blocks may include the final line terminator without counting the
	// next empty line. read_source may explicitly include that empty line.
	if len(lines)-1 == end-start+1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > end-start+1 {
		// A custom provider supplied inconsistent coordinates. Preserve its text
		// rather than inventing line numbers or silently dropping source.
		return text
	}
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%d | %s\n", start+i, strings.TrimSuffix(line, "\r"))
	}
	return b.String()
}
