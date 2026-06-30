package symbol

import "regexp"

// CSharpExtractor extracts C# type and method definitions with a dependency-free
// regex/byte scan (no Roslyn, no cgo, no third-party). It is best-effort: it
// favors precision on the common shapes seen in the TurnCommerce corpus
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

// Extract scans C# content for type and method definitions. Always returns nil
// error.
func (CSharpExtractor) Extract(content []byte) ([]Symbol, error) {
	var syms []Symbol

	// typeNameStart records the NameStart byte of every emitted type, so the
	// method pass can skip a declaration that has already been claimed as a type
	// (e.g. `public record CertId(...)` reads like a method to csMethodRe).
	typeNameStart := map[int]bool{}

	mask := literalMask(content)

	// Types: class/interface/struct/enum/record -> Type.
	for _, m := range csTypeRe.FindAllSubmatchIndex(content, -1) {
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
	for _, m := range csMethodRe.FindAllSubmatchIndex(content, -1) {
		ns, ne := m[2], m[3]
		if ns < 0 || ne < 0 {
			continue
		}
		name := string(content[ns:ne])
		if csControlKeywords[name] || typeNameStart[ns] {
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

	return syms, nil
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
	defs, _ := e.Extract(content)
	return csReferencesFromDefs(content, defs), nil
}

// ExtractDefsRefs implements DefsRefsExtractor: it scans content for
// definitions ONCE (via Extract) and derives references from that same defs
// slice, instead of the two independent csTypeRe/csMethodRe scans that calling
// Extract then ExtractRefs separately would perform (ExtractRefs re-running
// Extract internally to get defSites).
func (e CSharpExtractor) ExtractDefsRefs(content []byte) ([]Symbol, []Occurrence, error) {
	defs, _ := e.Extract(content)
	return defs, csReferencesFromDefs(content, defs), nil
}

// csReferencesFromDefs scans content for reference occurrences, excluding any
// offset that coincides with a NameStart in defs (the declaration sites).
// Shared by ExtractRefs and ExtractDefsRefs so both derive references from a
// single set of already-computed definitions rather than recomputing them.
func csReferencesFromDefs(content []byte, defs []Symbol) []Occurrence {
	defSites := map[int]bool{}
	for _, s := range defs {
		defSites[s.NameStart] = true
	}

	mask := literalMask(content)
	var occs []Occurrence
	for _, m := range csCallRe.FindAllSubmatchIndex(content, -1) {
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
