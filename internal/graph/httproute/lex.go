package httproute

import (
	"path/filepath"
	"sort"
	"strings"

	"moedex/internal/index"
)

// language selects the extractors and the literal syntax used for a blob.
type language uint8

const (
	langUnknown language = iota
	langCSharp
	langGo
	langJS
	langPython
)

// String renders a language for diagnostics and tests.
func (l language) String() string {
	switch l {
	case langCSharp:
		return "csharp"
	case langGo:
		return "go"
	case langJS:
		return "js"
	case langPython:
		return "python"
	default:
		return "unknown"
	}
}

func languageOfPath(path string) language {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cs":
		return langCSharp
	case ".go":
		return langGo
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts":
		return langJS
	case ".py":
		return langPython
	default:
		return langUnknown
	}
}

// blobLanguage picks the language for a blob from the first of its paths with a
// recognized extension. Content dedup means one blob can be committed under
// several paths; identical bytes have one syntax, so the first recognized
// extension decides.
func blobLanguage(b *index.Blob) language {
	if b == nil {
		return langUnknown
	}
	for _, f := range b.Files {
		if lang := languageOfPath(f.RelPath); lang != langUnknown {
			return lang
		}
	}
	return langUnknown
}

// span is a half-open byte range within a blob.
type span struct{ Start, End int }

// strLit is one string literal found in the source. Start/End bracket the whole
// literal INCLUDING its prefix and delimiters, so the reported evidence offset
// points at the literal as written; Inner is its text content, with the
// escapes that matter to a URL resolved.
type strLit struct {
	Start, End int
	Inner      string
}

// lexed is the per-blob scan result: where the comments are, so framework
// syntax quoted in prose classifies nothing, and where the string literals are,
// because in this pass the URL *is* a string literal and cannot simply be
// masked away like it is in internal/classify.
type lexed struct {
	comments []span
	strings  []strLit
}

// inComment reports whether off falls inside a comment.
func (l *lexed) inComment(off int) bool {
	i := sort.Search(len(l.comments), func(i int) bool { return l.comments[i].End > off })
	return i < len(l.comments) && off >= l.comments[i].Start
}

// inLiteral reports whether off falls inside a string literal.
func (l *lexed) inLiteral(off int) bool {
	_, ok := literalAt(l, off)
	return ok
}

// masked reports whether off falls in a comment or a string literal — the two
// places where framework-looking text registers nothing. A route attribute
// quoted in a const, or a client call shown in a doc comment, must classify
// nothing, exactly as in internal/classify.
func (l *lexed) masked(off int) bool { return l.inComment(off) || l.inLiteral(off) }

// literalsIn returns the string literals wholly contained in [lo, hi), in
// source order.
func (l *lexed) literalsIn(lo, hi int) []strLit {
	i := sort.Search(len(l.strings), func(i int) bool { return l.strings[i].Start >= lo })
	var out []strLit
	for ; i < len(l.strings) && l.strings[i].End <= hi; i++ {
		out = append(out, l.strings[i])
	}
	return out
}

// lex scans a blob once for comments and string literals.
//
// The two must be found together rather than by two independent passes: a
// comment introducer is ordinary text inside a string ("http://svc/api" is not
// a comment), and a quote is ordinary text inside a comment. Getting that wrong
// would silently swallow every absolute URL in the corpus.
func lex(content []byte, lang language) lexed {
	var out lexed
	for i := 0; i < len(content); {
		c := content[i]
		switch {
		case lang == langPython && c == '#':
			start := i
			for i < len(content) && content[i] != '\n' {
				i++
			}
			out.comments = append(out.comments, span{start, i})

		case lang != langPython && c == '/' && i+1 < len(content) && content[i+1] == '/':
			start := i
			i += 2
			for i < len(content) && content[i] != '\n' {
				i++
			}
			out.comments = append(out.comments, span{start, i})

		case lang != langPython && c == '/' && i+1 < len(content) && content[i+1] == '*':
			start := i
			i += 2
			for i+1 < len(content) && !(content[i] == '*' && content[i+1] == '/') {
				i++
			}
			if i+1 < len(content) {
				i += 2
			} else {
				i = len(content)
			}
			out.comments = append(out.comments, span{start, i})

		case c == '"' || c == '\'' || (c == '`' && (lang == langGo || lang == langJS)):
			lit, next := readString(content, i, lang)
			if next <= i {
				i++
				continue
			}
			if lit != nil {
				out.strings = append(out.strings, *lit)
			}
			i = next

		default:
			i++
		}
	}
	return out
}

// readString reads the literal whose opening delimiter is at quote and returns
// it with the offset just past its close. A nil literal with a valid next
// offset means "this quote does not open a literal we can read" — an
// apostrophe in prose, or an unterminated single-line string — which is
// deliberately not fatal: the scan resumes one byte later rather than running
// away to end of file.
func readString(content []byte, quote int, lang language) (*strLit, int) {
	delim := content[quote]
	start := literalStart(content, quote, lang)

	// Triple-quoted forms: Python's """/''' and C#'s raw string literals.
	if delim != '`' && quote+2 < len(content) && content[quote+1] == delim && content[quote+2] == delim &&
		(lang == langPython || lang == langCSharp) {
		i := quote + 3
		for i+2 < len(content) && !(content[i] == delim && content[i+1] == delim && content[i+2] == delim) {
			i++
		}
		if i+2 >= len(content) {
			return nil, len(content)
		}
		return &strLit{Start: start, End: i + 3, Inner: string(content[quote+3 : i])}, i + 3
	}

	raw := delim == '`' || isVerbatim(content, quote, lang) || isRawPrefixed(content, quote, lang)
	multiline := delim == '`' || raw

	var inner strings.Builder
	for i := quote + 1; i < len(content); {
		switch {
		case content[i] == '\n' && !multiline:
			// An unterminated single-line literal is far more likely to be an
			// apostrophe than a string; do not consume the rest of the file.
			return nil, quote + 1

		case raw && delim == '"' && lang == langCSharp && content[i] == '"':
			// A verbatim C# string escapes a quote by doubling it.
			if i+1 < len(content) && content[i+1] == '"' {
				inner.WriteByte('"')
				i += 2
				continue
			}
			return &strLit{Start: start, End: i + 1, Inner: inner.String()}, i + 1

		case !raw && content[i] == '\\' && i+1 < len(content):
			switch content[i+1] {
			case '\\', '"', '\'', '/':
				inner.WriteByte(content[i+1])
			default:
				inner.WriteByte(content[i])
				inner.WriteByte(content[i+1])
			}
			i += 2

		case content[i] == delim:
			return &strLit{Start: start, End: i + 1, Inner: inner.String()}, i + 1

		default:
			inner.WriteByte(content[i])
			i++
		}
	}
	return nil, len(content)
}

// literalStart backs up over the prefix characters that belong to the literal —
// C#'s $ and @, Python's f/r/b/u — so the reported byte range covers the token
// a reader would select.
func literalStart(content []byte, quote int, lang language) int {
	start := quote
	for start > 0 {
		c := content[start-1]
		ok := false
		switch lang {
		case langCSharp:
			ok = c == '$' || c == '@'
		case langPython:
			ok = c == 'f' || c == 'F' || c == 'r' || c == 'R' || c == 'b' || c == 'B' || c == 'u' || c == 'U'
		}
		if !ok || quote-start >= 2 {
			break
		}
		start--
	}
	return start
}

func isVerbatim(content []byte, quote int, lang language) bool {
	if lang != langCSharp || content[quote] != '"' {
		return false
	}
	for i := quote - 1; i >= 0 && quote-i <= 2; i-- {
		if content[i] == '@' {
			return true
		}
		if content[i] != '$' {
			return false
		}
	}
	return false
}

func isRawPrefixed(content []byte, quote int, lang language) bool {
	if lang != langPython {
		return false
	}
	for i := quote - 1; i >= 0 && quote-i <= 2; i-- {
		if content[i] == 'r' || content[i] == 'R' {
			return true
		}
		if !isPythonStringPrefix(content[i]) {
			return false
		}
	}
	return false
}

func isPythonStringPrefix(c byte) bool {
	switch c {
	case 'f', 'F', 'r', 'R', 'b', 'B', 'u', 'U':
		return true
	}
	return false
}

// argList returns the byte range of the argument list whose '(' is at open —
// the range strictly inside the parentheses — tracking nesting and skipping
// over string literals so a paren inside a URL cannot close it early. It
// reports false for an unbalanced list.
func argList(content []byte, open int, lang language, lx *lexed) (span, bool) {
	if open >= len(content) || content[open] != '(' {
		return span{}, false
	}
	depth := 0
	for i := open; i < len(content) && i-open < maxArgListBytes; {
		if lit, ok := literalAt(lx, i); ok {
			i = lit.End
			continue
		}
		switch content[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return span{Start: open + 1, End: i}, true
			}
		}
		i++
	}
	return span{}, false
}

// attrEnd returns the offset just past the ']' closing the attribute list whose
// '[' is at open, or -1 when it is unterminated. Nested brackets and quoted
// arguments are both handled, so "[Route(\"api/[controller]\")]" ends where a
// reader would say it ends rather than at the token inside the string.
func attrEnd(content []byte, open int, lx *lexed) int {
	depth := 0
	for i := open; i < len(content) && i-open < maxArgListBytes; {
		if lit, ok := literalAt(lx, i); ok {
			i = lit.End
			continue
		}
		switch content[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
		i++
	}
	return -1
}

// maxArgListBytes bounds how far argument scanning will read past a call token,
// so a malformed or minified file cannot turn one candidate into a whole-blob
// scan. Route registrations and client calls are orders of magnitude shorter.
const maxArgListBytes = 4096

func literalAt(lx *lexed, off int) (strLit, bool) {
	i := sort.Search(len(lx.strings), func(i int) bool { return lx.strings[i].End > off })
	if i < len(lx.strings) && off >= lx.strings[i].Start {
		return lx.strings[i], true
	}
	return strLit{}, false
}

// topLevelArgs counts the comma-separated arguments of an argument list,
// ignoring commas nested inside parentheses, brackets, braces, or generics.
func topLevelArgs(content []byte, args span, lx *lexed) int {
	if args.End <= args.Start {
		return 0
	}
	count, depth := 1, 0
	for i := args.Start; i < args.End; {
		if lit, ok := literalAt(lx, i); ok {
			i = lit.End
			continue
		}
		switch content[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				count++
			}
		}
		i++
	}
	return count
}

// secondArg returns the byte range of the second top-level argument — the
// handler of a route registration — trimmed of surrounding whitespace.
func secondArg(content []byte, args span, lx *lexed) (span, bool) {
	start := -1
	depth := 0
	for i := args.Start; i < args.End; {
		if lit, ok := literalAt(lx, i); ok {
			i = lit.End
			continue
		}
		switch content[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				if start < 0 {
					start = i + 1
				} else {
					return trimSpan(content, span{start, i}), true
				}
			}
		}
		i++
	}
	if start < 0 {
		return span{}, false
	}
	return trimSpan(content, span{start, args.End}), true
}

func trimSpan(content []byte, s span) span {
	for s.Start < s.End && isSpaceByte(content[s.Start]) {
		s.Start++
	}
	for s.End > s.Start && isSpaceByte(content[s.End-1]) {
		s.End--
	}
	return s
}

// secondArgByte returns the first non-space byte of the second top-level
// argument, or 0 when there is not one.
func secondArgByte(content []byte, args span, lx *lexed) byte {
	arg, ok := secondArg(content, args, lx)
	if !ok || arg.End <= arg.Start {
		return 0
	}
	return content[arg.Start]
}
