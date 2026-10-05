package symbol

import "regexp"

// TSExtractor extracts TypeScript definitions with a dependency-free regex/byte
// scan (no tsc, no tree-sitter, no cgo). Like CSharpExtractor it is best-effort
// and never errors. It covers the shapes that dominate the configured Angular
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

	// class method: an identifier directly followed by a parameter list,
	// indented (i.e. inside a class or object-literal body). Modifiers
	// (public/private/static/async/get/set/readonly/override/abstract) consumed
	// loosely. Filtered against keywords below, and — crucially — against
	// tsDeclFollowsParams: the indent alone does NOT distinguish a declaration
	// from a call statement.
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

// tsDeclFollowsParams reports whether the parameter list opening at content[open]
// (the '(' a tsMethodRe match ends on) is followed by a member DECLARATION
// rather than by the tail of a CALL expression.
//
// tsMethodRe's only structural guard was the leading indent, which a call
// statement satisfies just as well as a class member — so every bare indented
// call read as a method definition. On the configured corpus that made the
// test-framework and RxJS globals the most-"defined" symbols anywhere:
// `expect(` 7,801 definitions, `it(` 5,151, plus describe/beforeEach/mergeMap/map.
//
// A `)` alone cannot settle it (`foo(a: number);` is a legal interface signature
// and `foo(a);` is a call), so we match the parameter list to its closing paren
// and look at the next significant byte:
//
//	handle(e: Event) {        -> '{'  declaration (body follows)
//	ngOnInit(): void {        -> ':'  declaration (return type follows)
//	abstract load(id): Thing; -> ':'  declaration (signature with a return type)
//	expect(total).to.equal(2) -> '.'  call (method chain)
//	it('x', () => { ... });   -> ';'  call
//	map(x => x * 2),          -> ','  call (an operator in a pipe)
//
// Accepting only '{' and ':' means a return-type-less signature
// (`foo(a: number);`) is no longer extracted. That is the deliberate trade: it is
// byte-for-byte indistinguishable from a call, and this extractor's contract is
// to emit fewer symbols rather than wrong ones. An unbalanced parameter list also
// returns false — declining to guess beats fabricating a definition.
//
// Measured on the corpus (491 repos): 24,809 spurious definitions removed, every
// framework global down to zero, while real members hold (`ngOnInit` 463,
// `constructor` 1,363). The trade cost 137 `constructor` entries, all of them
// TS OVERLOAD SIGNATURES (`constructor(); constructor(value: string);`) stacked
// above their implementation — and the implementation ends in '{', so it is still
// extracted and the member is still defined at its real site.
func tsDeclFollowsParams(content []byte, open int) bool {
	end := matchParen(content, open)
	if end < 0 {
		return false
	}
	i := skipSpaceAndComments(content, end)
	if i >= len(content) {
		return false
	}
	return content[i] == '{' || content[i] == ':'
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
		// The match ends on the '(' that opens the parameter list, so m[1]-1 is
		// that paren. A call statement gets rejected here; see tsDeclFollowsParams.
		if !tsDeclFollowsParams(content, m[1]-1) {
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
	be := bodyRange(content, matchEnd)
	if be <= ns {
		return
	}
	*syms = append(*syms, Symbol{
		Name:      string(content[ns:ne]),
		Kind:      kind,
		NameStart: ns,
		NameEnd:   ne,
		BodyStart: matchStart,
		BodyEnd:   be,
	})
}
