package verify

// classifyRegions marks every source byte as code, string, or comment. It is a
// deliberately small lexical pass, not a parser: enough to ensure a call-shaped
// substring in prose or a literal cannot earn Pattern confidence. Ambiguous or
// newer syntax may over-mask code, which safely leaves the edge at Candidate.
func classifyRegions(content []byte, lang language) []region {
	out := make([]region, len(content))
	for i := 0; i < len(content); {
		// Line and block comments shared by Go, C#, and most C-family files.
		if i+1 < len(content) && content[i] == '/' && content[i+1] == '/' {
			i = markLine(content, out, i, regionComment)
			continue
		}
		if i+1 < len(content) && content[i] == '/' && content[i+1] == '*' {
			i = markBlock(content, out, i, []byte("*/"), regionComment)
			continue
		}
		// Hash comments apply to the general verifier (Python, shell, config),
		// but not to Go or C# where # can be source syntax.
		if lang == langGeneral && content[i] == '#' {
			i = markLine(content, out, i, regionComment)
			continue
		}
		// HTML/XML comments are common in otherwise-general files.
		if lang == langGeneral && hasPrefix(content, i, "<!--") {
			i = markBlock(content, out, i, []byte("-->"), regionComment)
			continue
		}

		if lang == langCSharp {
			// Verbatim and interpolated-verbatim C# strings escape quotes by
			// doubling them rather than with backslashes.
			switch {
			case hasPrefix(content, i, "$@\"") || hasPrefix(content, i, "@$\""):
				i = markVerbatimString(content, out, i, 3)
				continue
			case hasPrefix(content, i, "@\""):
				i = markVerbatimString(content, out, i, 2)
				continue
			case hasPrefix(content, i, "\"\"\""):
				i = markCSharpRawString(content, out, i)
				continue
			}
		}

		// Triple-quoted literals cover Python and other general files. Treat
		// the whole body as a string; interpolation holes intentionally remain
		// Candidate at this cheap tier.
		if hasPrefix(content, i, "\"\"\"") || hasPrefix(content, i, "'''") {
			delim := content[i : i+3]
			i = markBlock(content, out, i, delim, regionString)
			continue
		}
		if content[i] == '"' || content[i] == '\'' || content[i] == '`' {
			i = markQuoted(content, out, i, content[i])
			continue
		}
		i++
	}
	return out
}

// markCSharpRawString recognizes the complete opening quote run. A shorter
// quote run inside a raw string is content, so it must not expose test-program
// calls to Pattern verification. Interpolation holes stay masked at this tier.
func markCSharpRawString(content []byte, out []region, start int) int {
	end := start
	for end < len(content) && content[end] == '"' {
		end++
	}
	quotes := end - start
	first := end
	for first < len(content) && (content[first] == ' ' || content[first] == '\t' || content[first] == '\v' || content[first] == '\f') {
		first++
	}
	multiline := first < len(content) && (content[first] == '\r' || content[first] == '\n')
	for end < len(content) {
		if !multiline && (content[end] == '\r' || content[end] == '\n') {
			break // recover malformed single-line literals at their newline
		}
		if content[end] != '"' {
			end++
			continue
		}
		quoteStart := end
		for end < len(content) && content[end] == '"' {
			end++
		}
		if end-quoteStart >= quotes {
			break
		}
	}
	for i := start; i < end; i++ {
		out[i] = regionString
	}
	return end
}

func markLine(content []byte, out []region, start int, value region) int {
	i := start
	for i < len(content) && content[i] != '\n' {
		out[i] = value
		i++
	}
	return i
}

func markBlock(content []byte, out []region, start int, end []byte, value region) int {
	i := start
	// Move beyond the opening delimiter before looking for the closing one. For
	// symmetric triple quotes this prevents the opener from closing itself.
	open := 2
	if value == regionString {
		open = len(end)
	} else if hasPrefix(content, start, "<!--") {
		open = 4
	}
	for j := start; j < len(content) && j < start+open; j++ {
		out[j] = value
	}
	i = start + open
	for i < len(content) {
		out[i] = value
		if hasBytes(content, i, end) {
			for j := 1; j < len(end) && i+j < len(content); j++ {
				out[i+j] = value
			}
			return i + len(end)
		}
		i++
	}
	return i
}

func markQuoted(content []byte, out []region, start int, quote byte) int {
	i := start
	for i < len(content) {
		out[i] = regionString
		if i > start && content[i] == quote {
			return i + 1
		}
		if content[i] == '\\' && quote != '`' && i+1 < len(content) {
			out[i+1] = regionString
			i += 2
			continue
		}
		i++
	}
	return i
}

func markVerbatimString(content []byte, out []region, start, prefix int) int {
	for j := start; j < start+prefix && j < len(content); j++ {
		out[j] = regionString
	}
	i := start + prefix
	for i < len(content) {
		out[i] = regionString
		if content[i] != '"' {
			i++
			continue
		}
		if i+1 < len(content) && content[i+1] == '"' {
			out[i+1] = regionString
			i += 2
			continue
		}
		return i + 1
	}
	return i
}

func hasPrefix(content []byte, at int, s string) bool {
	return at >= 0 && at+len(s) <= len(content) && string(content[at:at+len(s)]) == s
}

func hasBytes(content []byte, at int, s []byte) bool {
	if at < 0 || at+len(s) > len(content) {
		return false
	}
	for i := range s {
		if content[at+i] != s[i] {
			return false
		}
	}
	return true
}
