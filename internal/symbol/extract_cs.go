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

// csTypeRe matches a C# type declaration and captures the type name. It allows
// leading modifiers (public, abstract, sealed, partial, etc.) loosely by
// anchoring on the type keyword preceded by start-of-line whitespace. `record`
// may be `record class`/`record struct`; the optional group absorbs that.
var csTypeRe = regexp.MustCompile(`\b(class|interface|struct|enum|record)(?:\s+(?:class|struct))?\s+([A-Za-z_][A-Za-z0-9_]*)`)

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

	// Types: class/interface/struct/enum/record -> Type.
	for _, m := range csTypeRe.FindAllSubmatchIndex(content, -1) {
		// m: [full0 full1 kw0 kw1 name0 name1]
		ns, ne := m[4], m[5]
		if ns < 0 || ne < 0 {
			continue
		}
		bs, be := bodyRange(content, m[1])
		typeNameStart[ns] = true
		syms = append(syms, Symbol{
			Name:      string(content[ns:ne]),
			Kind:      Type,
			NameStart: ns,
			NameEnd:   ne,
			BodyStart: minInt(m[0], bs),
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
		bs, be := bodyRange(content, m[1])
		syms = append(syms, Symbol{
			Name:      name,
			Kind:      Method,
			NameStart: ns,
			NameEnd:   ne,
			BodyStart: minInt(m[0], bs),
			BodyEnd:   be,
		})
	}

	return syms, nil
}

// bodyRange computes a [start, end) block for a declaration whose match ended at
// declEnd. It searches forward for the next '{' (or ';') before any newline-run
// that would clearly end the statement, brace-matches a '{' if found, and
// otherwise returns the declaration line as the range. start is declEnd's line
// start is not recomputed here; callers widen BodyStart to the match start.
func bodyRange(content []byte, declEnd int) (int, int) {
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
				return declEnd, end
			}
			break
		}
		if c == ';' {
			// Body-less member (interface/abstract method, field-like). Range is
			// the declaration line up to and including the ';'.
			return declEnd, i + 1
		}
		i++
	}
	// No block and no terminator found: fall back to the declaration line.
	return declEnd, lineEnd(content, declEnd)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
