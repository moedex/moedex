package symbol

import (
	"bytes"
	"regexp"
	"strings"
)

var (
	csTypeAtStart   = regexp.MustCompile(`\A` + strings.TrimPrefix(csTypeRe.String(), `(?m)^`))
	csMethodAtStart = regexp.MustCompile(`\A` + strings.TrimPrefix(csMethodRe.String(), `(?m)^`))
)

// csDeclarationMatches avoids starting the regexp VM on impossible lines. The
// original expressions still choose every capture. Windows include every byte
// a matching prefix could consume, including multiline modifiers and generics.
// Ambiguous long windows fall back for the whole input, preserving both linear
// work and the original nonoverlap decisions (even for subsequently masked or
// rejected declarations).
func csDeclarationMatches(content []byte, method bool) [][]int {
	matches, _ := csDeclarationMatchesBounded(content, method)
	return matches
}

func csDeclarationMatchesBounded(content []byte, method bool) ([][]int, bool) {
	original, anchored := csTypeRe, csTypeAtStart
	if method {
		original, anchored = csMethodRe, csMethodAtStart
	}
	var matches [][]int
	end, work := 0, 0
	for start := 0; start < len(content); {
		next := len(content)
		if n := bytes.IndexByte(content[start:], '\n'); n >= 0 {
			next = start + n + 1
		}
		if start >= end {
			head := start
			for head < len(content) && (content[head] == ' ' || content[head] == '\t') {
				head++
			}
			if head < len(content) && csDeclarationHead(content[head:], method) {
				limit, ok := csDeclarationWindow(content, head, method)
				// Keep the work guard effective even for very large 32-bit
				// inputs: overflow must fall back, never reset the budget.
				if !ok || limit-head > int(^uint(0)>>1)-work {
					return original.FindAllSubmatchIndex(content, -1), true
				}
				work += limit - head
				if work/8 > len(content) {
					return original.FindAllSubmatchIndex(content, -1), true
				}
				if m := anchored.FindSubmatchIndex(content[start:limit]); m != nil {
					for i := range m {
						if m[i] >= 0 {
							m[i] += start
						}
					}
					matches = append(matches, m)
					end = m[1]
				}
			}
		}
		start = next
	}
	return matches, false
}

func csDeclarationHead(content []byte, method bool) bool {
	b := content[0]
	if !(b == '_' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z') {
		return false
	}
	if method {
		return true
	}
	end := 1
	for end < len(content) && csIdentByte(content[end]) {
		end++
	}
	switch string(content[:end]) {
	case "public", "private", "protected", "internal", "static", "sealed", "abstract", "partial", "unsafe", "new", "readonly", "class", "interface", "struct", "enum", "record":
		return true
	}
	return false
}

func csDeclarationWindow(content []byte, start int, method bool) (int, bool) {
	const maxWindow = 4096
	angle := false
	for i := start; i < len(content); i++ {
		if i-start == maxWindow {
			return i, false
		}
		b := content[i]
		if method && angle {
			if b == '>' {
				angle = false
			}
			continue
		}
		if csIdentByte(b) || b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == '\f' {
			continue
		}
		if method {
			switch b {
			case '<':
				angle = true
				continue
			case '>', ',', '.', '[', ']', '?':
				continue
			}
		}
		// A '(' outside a possible generic suffix terminates a method;
		// all other bytes here are impossible before the end of a match.
		return i + 1, true
	}
	return len(content), true
}
