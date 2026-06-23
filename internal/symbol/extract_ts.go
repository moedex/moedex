package symbol

import "regexp"

// TSExtractor extracts TypeScript definitions with a dependency-free regex/byte
// scan (no tsc, no tree-sitter, no cgo). Like CSharpExtractor it is best-effort
// and never errors. It covers the shapes that dominate the TurnCommerce Angular
// front end:
//
//   - class            -> Type   (decorators like @Injectable() are skipped over)
//   - interface        -> Type
//   - type alias       -> Type   (`type Foo = ...`)
//   - enum             -> Type
//   - top-level function -> Func
//   - exported const / arrow function -> Const
//   - class methods    -> Method (members named `name(...) {` inside a class body)
//
// Name offsets come from a captured submatch (FindAllSubmatchIndex), so
// NameStart/NameEnd slice the exact identifier from content.
//
// Body ranges use the shared bodyRange helper: brace-match the block after the
// declaration, or fall back to the declaration line for body-less members
// (type aliases, abstract/interface methods, exported const expressions).
type TSExtractor struct{}

var (
	// class/interface/enum: capture the name. `export`, `default`, `declare`,
	// `abstract` modifiers consumed loosely. Anchored at line start (after
	// whitespace) so member-level `class` in comments is less likely to match.
	tsClassRe = regexp.MustCompile(`(?m)^[ \t]*(?:export\s+)?(?:default\s+)?(?:declare\s+)?(?:abstract\s+)?(class|interface|enum)\s+([A-Za-z_$][A-Za-z0-9_$]*)`)

	// type alias: `type Foo = ...` -> Type.
	tsTypeAliasRe = regexp.MustCompile(`(?m)^[ \t]*(?:export\s+)?(?:declare\s+)?type\s+([A-Za-z_$][A-Za-z0-9_$]*)`)

	// top-level function declaration -> Func.
	tsFuncRe = regexp.MustCompile(`(?m)^[ \t]*(?:export\s+)?(?:default\s+)?(?:declare\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][A-Za-z0-9_$]*)`)

	// exported const/let/var (incl. arrow functions) -> Const/Var. Only the
	// exported, top-level forms are taken to avoid local-variable noise.
	tsConstRe = regexp.MustCompile(`(?m)^[ \t]*export\s+(const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)`)

	// class method: an identifier directly followed by ( and a ) { ... block,
	// indented (i.e. inside a class body). Modifiers (public/private/static/
	// async/get/set/readonly/override/abstract) consumed loosely. The trailing
	// lookahead-ish requirement of `(` plus the indent guard keeps it from
	// matching top-level calls. Filtered against keywords below.
	tsMethodRe = regexp.MustCompile(`(?m)^[ \t]+(?:(?:public|private|protected|static|async|readonly|override|abstract|get|set)\s+)*([A-Za-z_$][A-Za-z0-9_$]*)\s*(?:<[^>]*>)?\s*\(`)
)

// tsMethodSkip filters tokens tsMethodRe captures that are control-flow
// keywords or otherwise not method names (`if (`, `for (`, `constructor` is
// kept — it is a real member).
var tsMethodSkip = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "catch": true,
	"return": true, "do": true, "else": true, "function": true, "await": true,
	"super": true, "typeof": true, "in": true, "of": true,
}

// Extract scans TypeScript content for definitions. Always returns nil error.
func (TSExtractor) Extract(content []byte) ([]Symbol, error) {
	var syms []Symbol

	for _, m := range tsClassRe.FindAllSubmatchIndex(content, -1) {
		emitTS(&syms, content, m[4], m[5], m[0], m[1], Type)
	}
	for _, m := range tsTypeAliasRe.FindAllSubmatchIndex(content, -1) {
		emitTS(&syms, content, m[2], m[3], m[0], m[1], Type)
	}
	for _, m := range tsFuncRe.FindAllSubmatchIndex(content, -1) {
		emitTS(&syms, content, m[2], m[3], m[0], m[1], Func)
	}
	for _, m := range tsConstRe.FindAllSubmatchIndex(content, -1) {
		// m: [full kw0 kw1 name0 name1]
		kind := Const
		if string(content[m[2]:m[3]]) != "const" {
			kind = Var
		}
		emitTS(&syms, content, m[4], m[5], m[0], m[1], kind)
	}
	for _, m := range tsMethodRe.FindAllSubmatchIndex(content, -1) {
		ns, ne := m[2], m[3]
		if ns < 0 || ne < 0 {
			continue
		}
		if tsMethodSkip[string(content[ns:ne])] {
			continue
		}
		emitTS(&syms, content, ns, ne, m[0], m[1], Method)
	}

	return syms, nil
}

// emitTS appends a Symbol with name = content[ns:ne], a body range derived from
// bodyRange searching from matchEnd, and BodyStart widened to the match start
// (matchStart). Out-of-bounds offsets are dropped.
func emitTS(syms *[]Symbol, content []byte, ns, ne, matchStart, matchEnd int, kind Kind) {
	if ns < 0 || ne < 0 || ns >= ne || ne > len(content) {
		return
	}
	bs, be := bodyRange(content, matchEnd)
	if be <= ns {
		return
	}
	*syms = append(*syms, Symbol{
		Name:      string(content[ns:ne]),
		Kind:      kind,
		NameStart: ns,
		NameEnd:   ne,
		BodyStart: minInt(matchStart, bs),
		BodyEnd:   be,
	})
}
