package symbol

import "iter"

// csCallMatches is the byte-scanning equivalent of csCallRe. In particular,
// matching happens before literal filtering, and a successful match consumes
// the opening parenthesis even when its caller subsequently discards it.
// Captures are [match start, match end, new start, new end, name start, name end].
func csCallMatches(content []byte) iter.Seq[[6]int] {
	return func(yield func([6]int) bool) {
		for start := 0; start < len(content); {
			if !csCallNameStart(content[start]) || start > 0 && csCallWord(content[start-1]) {
				start++
				continue
			}
			nameEnd := start + 1
			for nameEnd < len(content) && csCallWord(content[nameEnd]) {
				nameEnd++
			}
			// The optional construction prefix is greedy, but may be abandoned
			// if its following identifier/suffix cannot match (for example new().)
			if nameEnd-start == 3 && content[start] == 'n' && content[start+1] == 'e' && content[start+2] == 'w' {
				next := csCallSkipSpace(content, nameEnd)
				if next > nameEnd && next < len(content) && csCallNameStart(content[next]) {
					end := next + 1
					for end < len(content) && csCallWord(content[end]) {
						end++
					}
					if matchEnd := csCallSuffix(content, end); matchEnd >= 0 {
						if !yield([6]int{start, matchEnd, start, nameEnd, next, end}) {
							return
						}
						start = matchEnd
						continue
					}
				}
			}
			if end := csCallSuffix(content, nameEnd); end >= 0 {
				if !yield([6]int{start, end, -1, -1, start, nameEnd}) {
					return
				}
				start = end
			} else {
				start = nameEnd
			}
		}
	}
}

func csCallNameStart(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_'
}

// RE2's \b and \w are ASCII, including beside Unicode or invalid UTF-8.
func csCallWord(c byte) bool {
	return csCallNameStart(c) || c >= '0' && c <= '9'
}

func csCallSkipSpace(content []byte, pos int) int {
	for pos < len(content) {
		switch content[pos] {
		case ' ', '\t', '\n', '\r', '\f': // Go regexp \s deliberately excludes vertical tab.
			pos++
		default:
			return pos
		}
	}
	return pos
}

func csCallSuffix(content []byte, pos int) int {
	pos = csCallSkipSpace(content, pos)
	if pos < len(content) && content[pos] == '<' {
		pos++
		for pos < len(content) {
			switch content[pos] {
			case '>':
				pos = csCallSkipSpace(content, pos+1)
				if pos < len(content) && content[pos] == '(' {
					return pos + 1
				}
				return -1
			case '<', ';', '{', '}', '(', ')':
				return -1
			}
			pos++
		}
		return -1
	}
	if pos < len(content) && content[pos] == '(' {
		return pos + 1
	}
	return -1
}
