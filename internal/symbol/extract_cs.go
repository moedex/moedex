package symbol

import "regexp"

// CSharpExtractor extracts C# type and method definitions with a dependency-free
// regex/byte scan (no Roslyn, no cgo, no third-party). It is best-effort: it
// favors precision on the common shapes seen in the configured corpus
// (public/internal classes, interfaces, structs, enums, records, and method
// declarations) over completeness. On anything it can't confidently match it
// emits fewer symbols rather than wrong ones, and it always returns a nil error.
//
// Name offsets: every pattern captures the declared identifier as a submatch,
// and FindAllSubmatchIndex gives the byte offsets of that submatch directly into
// content, so NameStart/NameEnd slice the exact identifier.
//
// Body ranges: from the match end we look for the next '{' on the same logical
// declaration; if found we brace-match it (matchBlock, which skips strings and
// comments). If there is no block (interface/abstract method, expression-bodied
// member ending in ';', or a record with no body) we fall back to the
// declaration line's byte range so BodyStart<=NameStart<BodyEnd still holds.
type CSharpExtractor struct{}

// csLiteralMask treats interpolated strings as literals, like the shared
// scanner, but recognizes C# verbatim and raw delimiters. In particular, quotes
// embedded in raw test-source strings must not expose declarations as code.
func csLiteralMask(content []byte) []bool {
	return literalMaskWithStringSkipper(content, csSkipString)
}

func csSkipString(content []byte, i int, quote byte) int {
	if quote != '"' {
		return skipString(content, i, quote)
	}
	n := len(content)
	verbatim := i > 0 && content[i-1] == '@' || i > 1 && content[i-1] == '$' && content[i-2] == '@'
	if verbatim {
		for j := i + 1; j < n; j++ {
			if content[j] == '"' {
				if j+1 < n && content[j+1] == '"' {
					j++ // doubled quotes are content; backslashes are ordinary bytes
					continue
				}
				return j + 1
			}
		}
		return n
	}
	end := i
	for end < n && content[end] == '"' {
		end++
	}
	quotes := end - i
	if quotes >= 3 {
		// Only whitespace followed by a newline makes this a multiline raw
		// literal. Recover an unterminated single-line literal at its newline,
		// rather than hiding all subsequent declarations through EOF.
		first := end
		for first < n && (content[first] == ' ' || content[first] == '\t' || content[first] == '\v' || content[first] == '\f') {
			first++
		}
		multiline := first < n && (content[first] == '\r' || content[first] == '\n')
		for j := end; j < n; {
			if !multiline && (content[j] == '\r' || content[j] == '\n') {
				return j
			}
			if content[j] != '"' {
				j++
				continue
			}
			start := j
			for j < n && content[j] == '"' {
				j++
			}
			if j-start >= quotes {
				return j
			}
		}
		return n
	}
	return skipString(content, i, quote)
}

// csTypeRe matches a C# type declaration and captures the type name. It is
// anchored at line start (after optional whitespace) and consumes leading
// modifiers (public, abstract, sealed, partial, etc.) loosely, like
// csMethodRe, so a `where T : class` constraint or `class`/`record`/...
// tokens appearing mid-line (e.g. in prose) never match. `record` may be
// `record class`/`record struct`; the optional group absorbs that. Matches
// starting inside a comment or string literal are additionally dropped via
// literalMask in Extract.
var csTypeRe = regexp.MustCompile(`(?m)^[ \t]*(?:(?:public|private|protected|internal|static|sealed|abstract|partial|unsafe|new|readonly)\s+)*(class|interface|struct|enum|record)(?:\s+(?:class|struct))?\s+([A-Za-z_][A-Za-z0-9_]*)`)

// csMethodRe matches a C# method declaration and captures the method name. It
// requires a return type token then Name( and is anchored at line start after
// whitespace. Access/modifier keywords are consumed loosely. The negative
// guards (excluding control-flow keywords as the "name") avoid matching
// `if (`, `while (`, etc. Constructors are matched too (return type == the
// type name reads as the return-type token), which is acceptable for ranking.
var csMethodRe = regexp.MustCompile(`(?m)^[ \t]*(?:(?:public|private|protected|internal|static|virtual|override|abstract|sealed|async|partial|extern|unsafe|new|readonly)\s+)*[A-Za-z_][A-Za-z0-9_<>,.\[\] ?]*?\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:<[^>]*>)?\s*\(`)

// csControlKeywords are tokens csMethodRe can falsely capture as a method name
// because `keyword (` looks like `Name(`. They are filtered out.
var csControlKeywords = map[string]bool{
	"if": true, "for": true, "foreach": true, "while": true, "switch": true,
	"catch": true, "using": true, "lock": true, "fixed": true, "return": true,
	"do": true, "else": true, "get": true, "set": true, "nameof": true,
	"typeof": true, "sizeof": true, "default": true, "new": true,
}

// csStatementKeywords are tokens that can appear in a STATEMENT but never
// anywhere in a member declaration's PREFIX (its modifiers plus return type).
// They filter the mirror-image false positive of csControlKeywords: that map
// rejects a keyword captured as the method NAME (`if (` reading as `Name(`),
// while this one rejects a statement whose head was mistaken for a return type.
//
// The return-type character class must admit a space — `Dictionary<string, int>
// Foo(` is a real declaration — and `\s+` matches newlines, so the slot happily
// swallows a multi-token statement head:
//
//	throw new ArgumentException("x");     -> type "throw new",       name ArgumentException
//	return new Widget(1);                -> type "return new",      name Widget
//	yield return new Thing(2);           -> type "yield return new", name Thing
//	await SendAsync(x);                  -> type "await",           name SendAsync
//	id ?? throw new ArgumentException(   -> type "id ?? throw new", name ArgumentException
//
// Each emitted a bogus Method definition AND suppressed the real reference
// (csReferencesFromDefs skips offsets a definition claimed). Measured on the
// configured corpus (491 repos), the shapes above accounted for 72,255
// spurious definitions — 29% of every definition in the corpus. Examples:
// ArgumentException reported 785 "definitions" across 88 repos (now 0, all 900
// occurrences being references), and a single generated file of
// `new WhoIsBatchCriteria(...)` lines reported 49,286 (now 2: the real class and
// its constructor).
//
// Every reserved word here is illegal as a C# identifier, so it can never be a
// real type name and rejecting it in any prefix position is safe; `await`,
// `var`, and `yield` are contextual rather than reserved, but none of them can
// appear in a declaration prefix either. `new` is deliberately NOT in this set —
// it is a legal modifier (member hiding: `public new void M()`) — and is handled
// positionally by csDeclPrefixOK.
var csStatementKeywords = map[string]bool{
	"throw": true, "return": true, "await": true, "yield": true, "var": true,
	"if": true, "else": true, "for": true, "foreach": true, "while": true,
	"do": true, "switch": true, "case": true, "lock": true, "using": true,
	"fixed": true, "try": true, "catch": true, "finally": true, "goto": true,
	"break": true, "continue": true, "base": true, "this": true,
	"checked": true, "unchecked": true, "typeof": true, "nameof": true,
	"sizeof": true, "default": true, "is": true, "as": true, "in": true,
	"out": true, "stackalloc": true,
}

// csDeclPrefixOK reports whether the bytes in [from, to) — everything a
// csMethodRe match consumed before the captured name, i.e. the modifiers and
// return type — read like a real declaration prefix rather than the head of a
// statement. It walks the identifier tokens and rejects on two grounds:
//
//   - any token is a statement keyword (see csStatementKeywords). Scanning EVERY
//     token, not just the first, is what catches a continuation line such as
//     `subjectId ?? throw new ArgumentException(`, where the statement keyword
//     sits mid-prefix behind an ordinary identifier.
//   - the LAST token is `new`, which means `new Name(` — an object creation.
//     `new` is a legal modifier, but it can never be the return TYPE, so a
//     prefix ending in it is never a declaration. `public new void M()` keeps
//     `void` as its last token and is unaffected. This also independently covers
//     every `... new Name(` shape above.
//
// A prefix whose tokens are all modifiers is accepted: that is how a constructor
// (`public Foo(int x)`, where `public` lands in the return-type slot) matches.
func csDeclPrefixOK(content []byte, from, to int) bool {
	if from < 0 || to > len(content) {
		return true // defensive: never drop a symbol over a bad offset
	}
	last := ""
	for i := from; i < to; {
		if !csIdentByte(content[i]) {
			i++
			continue
		}
		start := i
		for i < to && csIdentByte(content[i]) {
			i++
		}
		tok := string(content[start:i])
		if csStatementKeywords[tok] {
			return false
		}
		last = tok
	}
	return last != "new"
}

// csIdentByte reports whether b can appear in a C# identifier (ASCII subset —
// enough for keyword recognition, which is all csLeadingToken needs).
func csIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// Extract scans C# content for type and method definitions. Always returns nil
// error.
func (CSharpExtractor) Extract(content []byte) ([]Symbol, error) {
	return csDefinitions(content, csLiteralMask(content)), nil
}

func csDefinitions(content []byte, mask []bool) []Symbol {
	var syms []Symbol

	// typeNameStart records the NameStart byte of every emitted type, so the
	// method pass can skip a declaration that has already been claimed as a type
	// (e.g. `public record CertId(...)` reads like a method to csMethodRe).
	typeNameStart := map[int]bool{}

	// Types: class/interface/struct/enum/record -> Type.
	for _, m := range csDeclarationMatches(content, false) {
		// m: [full0 full1 kw0 kw1 name0 name1]
		ns, ne := m[4], m[5]
		if ns < 0 || ne < 0 {
			continue
		}
		if mask[ns] {
			continue // inside a string/char literal or a comment
		}
		be := bodyRange(content, m[1])
		typeNameStart[ns] = true
		syms = append(syms, Symbol{
			Name:      string(content[ns:ne]),
			Kind:      Type,
			NameStart: ns,
			NameEnd:   ne,
			BodyStart: m[0],
			BodyEnd:   be,
		})
	}

	// Methods: any Name( declaration at line start. Method (we don't distinguish
	// receiver-bearing methods from free functions in C#; all members live in a
	// type, so Method is the right Kind).
	for _, m := range csDeclarationMatches(content, true) {
		ns, ne := m[2], m[3]
		if ns < 0 || ne < 0 {
			continue
		}
		if mask[ns] {
			continue // inside a string/char literal or a comment
		}
		name := string(content[ns:ne])
		if csControlKeywords[name] || typeNameStart[ns] {
			continue
		}
		// A statement, not a declaration: what this matched as a "return type" is
		// really a statement head (`throw new` / `return new` / `await` / a bare
		// `new`). See csStatementKeywords and csDeclPrefixOK.
		if !csDeclPrefixOK(content, m[0], ns) {
			continue
		}
		be := bodyRange(content, m[1])
		syms = append(syms, Symbol{
			Name:      name,
			Kind:      Method,
			NameStart: ns,
			NameEnd:   ne,
			BodyStart: m[0],
			BodyEnd:   be,
		})
	}

	return syms
}

// csCallRe matches a call/construction site: an optional `new` keyword, then an
// identifier immediately followed by `(` (an optional generic argument list is
// allowed between the name and the paren). It captures the identifier. This is
// deliberately liberal — the literal/comment mask and the def-site exclusion in
// ExtractRefs prune the false positives that matter.
var csCallRe = regexp.MustCompile(`\b(?:(new)\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*(?:<[^<>;{}()]*>)?\s*\(`)

// ExtractRefs scans C# content for reference occurrences: method/constructor
// call sites (`Name(`) and `new` constructions (`new Type(`). It is best-effort
// and NAME-BASED (no semantic resolution). It excludes:
//
//   - any match inside a string/char literal or a comment (via literalMask);
//   - control-flow keywords that read like a call (`if (`, `while (`, ...);
//   - the identifier of a definition this same content declares (so a method's
//     own declaration site is not re-emitted as a reference, and a constructor
//     declaration `public Foo(` is not double-counted) — done by excluding
//     offsets that coincide with a Symbol.NameStart from Extract.
//
// Always returns a nil error.
func (e CSharpExtractor) ExtractRefs(content []byte) ([]Occurrence, error) {
	mask := csLiteralMask(content)
	defs := csDefinitions(content, mask)
	return csReferencesWithMask(content, defs, mask), nil
}

// ExtractDefsRefs implements DefsRefsExtractor: it scans content for
// definitions once and derives references from that same defs slice. The two
// passes also share one literal mask. Separate Extract and ExtractRefs calls
// retain their independent behavior.
func (e CSharpExtractor) ExtractDefsRefs(content []byte) ([]Symbol, []Occurrence, error) {
	mask := csLiteralMask(content)
	defs := csDefinitions(content, mask)
	return defs, csReferencesWithMask(content, defs, mask), nil
}

// csReferencesFromDefs scans content for reference occurrences, excluding any
// offset that coincides with a NameStart in defs (the declaration sites).
// Shared by ExtractRefs and ExtractDefsRefs so both derive references from a
// single set of already-computed definitions rather than recomputing them.
func csReferencesFromDefs(content []byte, defs []Symbol) []Occurrence {
	return csReferencesWithMask(content, defs, csLiteralMask(content))
}

func csReferencesWithMask(content []byte, defs []Symbol, mask []bool) []Occurrence {
	defSites := map[int]bool{}
	for _, s := range defs {
		defSites[s.NameStart] = true
	}

	var occs []Occurrence
	for m := range csCallMatches(content) {
		// m: [full0 full1 new0 new1 name0 name1]
		ns, ne := m[4], m[5]
		if ns < 0 || ne < 0 {
			continue
		}
		if mask[ns] {
			continue // inside a string/char literal or a comment
		}
		name := string(content[ns:ne])
		if csControlKeywords[name] {
			continue
		}
		if defSites[ns] {
			continue // this is the declaration site, not a use
		}
		// `new` construction -> Type reference; bare call -> Method reference.
		kind := Method
		if m[2] >= 0 {
			kind = Type
		}
		occs = append(occs, Occurrence{
			Name:  name,
			Kind:  kind,
			Role:  Reference,
			Start: ns,
			End:   ne,
		})
	}
	return occs
}

// bodyRange computes the end offset for a declaration whose match ended at
// declEnd. It searches forward for the next '{' (or ';'), brace-matches a '{'
// if found, and otherwise returns the declaration line end. Callers set
// BodyStart to their regex match start.
func bodyRange(content []byte, declEnd int) int {
	n := len(content)
	if declEnd < 0 {
		declEnd = 0
	}
	// Scan forward for the opening brace or a statement terminator. Allow a
	// bounded look so we don't run off into the next member; the brace for a
	// declaration normally follows within the same or next few lines.
	i := declEnd
	for i < n {
		c := content[i]
		if c == '{' {
			if end := matchBlock(content, i); end > i {
				return end
			}
			break
		}
		if c == ';' {
			// Body-less member (interface/abstract method, field-like). Range is
			// the declaration line up to and including the ';'.
			return i + 1
		}
		i++
	}
	// No block and no terminator found: fall back to the declaration line.
	return lineEnd(content, declEnd)
}
