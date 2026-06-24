package symbol

// scan.go holds shared, dependency-free helpers used by the regex/byte-scanning
// extractors (C#, TypeScript). These languages have no stdlib parser, so we
// scan bytes directly. Two concerns are factored out here:
//
//   - matchBlock: given the byte offset of an opening '{', find the byte offset
//     just past its matching '}', skipping over string/char literals and
//     comments so braces inside them do not throw off the depth count.
//   - lineEnd: the fallback body end for declarations with no brace block
//     (interface/abstract methods, one-line members) — the end of the
//     declaration's own line.
//
// Both are best-effort. They never read outside content bounds and never panic.

// matchBlock returns the byte offset just past the '}' that matches the '{' at
// content[open]. It skips braces that appear inside string, char, and template
// literals and inside line/block comments. If the brace is never balanced
// (truncated/garbled content) it returns -1, and callers fall back to a
// line-range body.
//
// The lexer is deliberately small and shared by C# and TS. It handles:
//   - // line comments and /* */ block comments
//   - "double" and 'single' quoted strings with backslash escapes
//   - `template` literals (TS) — treated as plain strings; ${...} interpolation
//     braces inside them are NOT counted (a pragmatic simplification that is
//     safe because we only care about top-level declaration blocks)
//
// content[open] is assumed to be '{'; if it is not, matchBlock still works by
// counting from depth 0 at open.
func matchBlock(content []byte, open int) int {
	n := len(content)
	if open < 0 || open >= n {
		return -1
	}
	depth := 0
	i := open
	for i < n {
		c := content[i]
		switch c {
		case '/':
			if i+1 < n && content[i+1] == '/' {
				// Line comment: skip to end of line.
				i += 2
				for i < n && content[i] != '\n' {
					i++
				}
				continue
			}
			if i+1 < n && content[i+1] == '*' {
				// Block comment: skip to */.
				i += 2
				for i+1 < n && !(content[i] == '*' && content[i+1] == '/') {
					i++
				}
				i += 2
				continue
			}
			i++
		case '"', '\'', '`':
			i = skipString(content, i, c)
		case '{':
			depth++
			i++
		case '}':
			depth--
			i++
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return -1
}

// skipString returns the byte offset just past the closing quote of a string
// literal that opens at content[i] with delimiter quote. Backslash escapes are
// honored. If the string is unterminated it returns len(content).
func skipString(content []byte, i int, quote byte) int {
	n := len(content)
	i++ // past opening quote
	for i < n {
		c := content[i]
		if c == '\\' {
			i += 2
			continue
		}
		if c == quote {
			return i + 1
		}
		i++
	}
	return n
}

// lineEnd returns the byte offset just past the end of the line containing
// from (i.e. the index of the next '\n', or len(content)). Used as the
// fallback BodyEnd for body-less declarations.
func lineEnd(content []byte, from int) int {
	n := len(content)
	i := from
	for i < n && content[i] != '\n' {
		i++
	}
	return i
}

// literalMask returns a bitmap, one entry per byte of content, that is true for
// every byte that lies INSIDE a string/char/template literal or a line/block
// comment (including the delimiters themselves). It uses the same lexing rules
// as matchBlock, so the C# / TS reference passes can cheaply ask "is this
// candidate offset real code?" by checking mask[off] == false and avoid
// emitting references from inside literals and comments.
//
// The mask is allocated once per file and indexed in O(1); construction is a
// single linear scan. It is bounds-safe and never panics.
func literalMask(content []byte) []bool {
	n := len(content)
	mask := make([]bool, n)
	i := 0
	markRange := func(lo, hi int) {
		if lo < 0 {
			lo = 0
		}
		if hi > n {
			hi = n
		}
		for j := lo; j < hi; j++ {
			mask[j] = true
		}
	}
	for i < n {
		c := content[i]
		switch c {
		case '/':
			if i+1 < n && content[i+1] == '/' {
				start := i
				i += 2
				for i < n && content[i] != '\n' {
					i++
				}
				markRange(start, i)
				continue
			}
			if i+1 < n && content[i+1] == '*' {
				start := i
				i += 2
				for i+1 < n && !(content[i] == '*' && content[i+1] == '/') {
					i++
				}
				i += 2
				markRange(start, i)
				continue
			}
			i++
		case '"', '\'', '`':
			start := i
			end := skipString(content, i, c)
			markRange(start, end)
			i = end
		default:
			i++
		}
	}
	return mask
}
