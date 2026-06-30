package symbol

import (
	"go/ast"
	"go/parser"
	"go/token"

	"moedex/internal/index"
)

// Extractor pulls symbols out of a blob's content. Implementations are
// language-specific; the only one in slice 1 is GoExtractor. An error means the
// content could not be parsed; callers (e.g. Build) treat that as "no symbols"
// and fall back to the brace/indent heuristic at scoping time.
type Extractor interface {
	Extract(content []byte) ([]Symbol, error)
}

// RefExtractor is an OPTIONAL capability: an Extractor that also emits reference
// occurrences (call sites, selectors, constructions). It is a separate
// interface so the base Extractor contract — and every extractor that does not
// implement references yet — is untouched. Callers (Build, BuildMulti)
// type-assert to RefExtractor and skip reference extraction when an extractor
// does not implement it. In the redesign/deep-symbols slice only GoExtractor
// (precise, via go/ast) and CSharpExtractor (best-effort, via byte scan)
// implement it.
type RefExtractor interface {
	Extractor
	// ExtractRefs returns the Reference-role occurrences in content. Definition
	// occurrences are NOT returned here (they come from Extract); a def site is
	// excluded so it is never double-counted as a reference. An error means the
	// content could not be parsed and is treated as "no references".
	ExtractRefs(content []byte) ([]Occurrence, error)
}

// DefsRefsExtractor is an OPTIONAL capability superseding RefExtractor: an
// extractor that computes definitions AND references together in a single
// parse/scan pass over content. Build and BuildMulti (via extractDefsRefs)
// prefer this over calling Extract then ExtractRefs separately, which would
// otherwise parse/scan the same content twice per blob (go/parser.ParseFile
// twice for Go; the C# regex scan twice, once inside Extract and once again
// inside ExtractRefs).
type DefsRefsExtractor interface {
	Extractor
	ExtractDefsRefs(content []byte) ([]Symbol, []Occurrence, error)
}

// GoExtractor extracts Go function/method/type/const/var definitions using the
// standard library go/parser. Byte offsets come from token.FileSet.Position,
// which counts bytes (not runes), so multi-byte unicode in the source maps
// correctly without any extra conversion.
type GoExtractor struct{}

// Extract parses content as a single Go source file and returns its top-level
// and nested declarations as Symbols. Nested function literals assigned in
// declarations are walked too, so Enclosing can resolve the innermost scope.
//
// On a parse error Extract returns (nil, err). go/parser with ParseComments
// still produces a partial AST on recoverable errors, but to keep the contract
// simple and let callers fall back cleanly, any error is surfaced and no partial
// result is returned.
func (GoExtractor) Extract(content []byte) ([]Symbol, error) {
	fset := token.NewFileSet()
	// ParseComments is required so decls carry their Doc comment groups, which the
	// doc-inclusive BodyStart rule below depends on. SkipObjectResolution keeps the
	// parse cheap (we never need scope/object info).
	file, err := parser.ParseFile(fset, "", content, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	return goSymbolsFromFile(file, goOffsetFunc(fset)), nil
}

// goOffsetFunc returns a closure mapping a token.Pos to a byte offset into the
// content fset was built from. token.NoPos -> -1.
func goOffsetFunc(fset *token.FileSet) func(token.Pos) int {
	return func(p token.Pos) int {
		if !p.IsValid() {
			return -1
		}
		return fset.Position(p).Offset
	}
}

// goSymbolsFromFile walks file's top-level decls and nested function literals
// to produce the same Symbols as Extract. Shared by Extract and
// ExtractDefsRefs so both build symbols from a single parsed *ast.File.
func goSymbolsFromFile(file *ast.File, off func(token.Pos) int) []Symbol {
	var syms []Symbol
	emit := func(name string, kind Kind, namePos token.Pos, nameLen int, bodyStart, bodyEnd token.Pos) {
		ns := off(namePos)
		bs := off(bodyStart)
		be := off(bodyEnd)
		if ns < 0 || bs < 0 || be < 0 {
			return
		}
		syms = append(syms, Symbol{
			Name:      name,
			Kind:      kind,
			NameStart: ns,
			NameEnd:   ns + nameLen,
			BodyStart: bs,
			BodyEnd:   be,
		})
	}

	// bodyStartWithDoc returns the position the symbol's range should begin at:
	// the doc comment's first byte when a non-nil doc comment group is present,
	// otherwise the declaration's own start (the func/type keyword). Including the
	// doc comment in [BodyStart, BodyEnd) lets Enclosing resolve an offset that
	// falls on a doc-comment line back to the documented symbol.
	bodyStartWithDoc := func(doc *ast.CommentGroup, declPos token.Pos) token.Pos {
		if doc != nil {
			return doc.Pos()
		}
		return declPos
	}

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			kind := Func
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = Method
			}
			// End() is the position just past the last token; for a func with a
			// body that is the closing brace + 1, for a body-less decl the
			// signature end. Either way it is the exclusive byte end we want.
			// BodyStart is widened to include the doc comment when present.
			emit(d.Name.Name, kind, d.Name.Pos(), len(d.Name.Name), bodyStartWithDoc(d.Doc, d.Pos()), d.End())

		case *ast.GenDecl:
			kind := genKind(d.Tok)
			if kind == KindUnknown {
				continue
			}
			// The doc comment lives on the GenDecl (e.g. above `type`/`const`/
			// `var`), so widen each spec's BodyStart to the GenDecl's doc when
			// present. This applies the doc to every spec of the group.
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					emit(s.Name.Name, Type, s.Name.Pos(), len(s.Name.Name), bodyStartWithDoc(d.Doc, s.Pos()), s.End())
				case *ast.ValueSpec:
					for _, n := range s.Names {
						emit(n.Name, kind, n.Pos(), len(n.Name), bodyStartWithDoc(d.Doc, s.Pos()), s.End())
					}
				}
			}
		}
	}

	// Nested function literals (e.g. closures assigned in a func body) so that
	// Enclosing returns the tightest scope. They carry no name; use the empty
	// string and Func kind.
	ast.Inspect(file, func(n ast.Node) bool {
		fl, ok := n.(*ast.FuncLit)
		if !ok {
			return true
		}
		bs := off(fl.Pos())
		be := off(fl.End())
		if bs < 0 || be < 0 {
			return true
		}
		syms = append(syms, Symbol{
			Name:      "",
			Kind:      Func,
			NameStart: bs,
			NameEnd:   bs,
			BodyStart: bs,
			BodyEnd:   be,
		})
		return true
	})

	return syms
}

func genKind(tok token.Token) Kind {
	switch tok {
	case token.TYPE:
		return Type
	case token.CONST:
		return Const
	case token.VAR:
		return Var
	default:
		return KindUnknown
	}
}

// ExtractRefs parses content as a single Go source file and returns the
// Reference-role occurrences in it: call targets (CallExpr.Fun idents),
// selector field/method names (SelectorExpr.Sel), and type idents in
// composite literals. Offsets are exact BYTE offsets via the same FileSet.
//
// Declaring idents are EXCLUDED: any *ast.Ident that is itself the name of a
// declaration (func name, receiver type, type/const/var name, parameter or
// local binding) is recorded during a first pass and skipped in the reference
// pass, so a definition site is never double-counted as a reference. This makes
// the result a clean set of USES of names within the file.
//
// This is single-file SYNTACTIC reference extraction — it does not resolve a
// name to a particular declaration across files. On a parse error it returns
// (nil, err) so callers fall back to "no references".
func (GoExtractor) ExtractRefs(content []byte) ([]Occurrence, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", content, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	off := goOffsetFunc(fset)
	return goReferencesFromFile(file, off, goDeclSites(file, off)), nil
}

// ExtractDefsRefs implements DefsRefsExtractor: it parses content ONCE and
// derives both the definitions (Extract's result) and the references
// (ExtractRefs's result) from that single *ast.File, instead of the two
// independent parser.ParseFile calls that calling Extract then ExtractRefs
// separately would perform.
func (GoExtractor) ExtractDefsRefs(content []byte) ([]Symbol, []Occurrence, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", content, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, err
	}
	off := goOffsetFunc(fset)
	syms := goSymbolsFromFile(file, off)
	occs := goReferencesFromFile(file, off, goDeclSites(file, off))
	return syms, occs, nil
}

// goDeclSites collects the byte offsets of every DECLARING ident in file so
// goReferencesFromFile can exclude them. ast.Ident.Pos() is stable per node,
// so a set of declaring offsets is sufficient to filter def sites.
func goDeclSites(file *ast.File, off func(token.Pos) int) map[int]bool {
	declSites := map[int]bool{}
	markDecl := func(id *ast.Ident) {
		if id == nil {
			return
		}
		if o := off(id.Pos()); o >= 0 {
			declSites[o] = true
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.FuncDecl:
			markDecl(d.Name)
		case *ast.TypeSpec:
			markDecl(d.Name)
		case *ast.ValueSpec:
			for _, id := range d.Names {
				markDecl(id)
			}
		case *ast.Field:
			// Field names cover struct fields, named func params/results, and
			// receiver names — all binding sites, not references.
			for _, id := range d.Names {
				markDecl(id)
			}
		case *ast.AssignStmt:
			// Short var declarations (:=) bind their LHS idents.
			if d.Tok == token.DEFINE {
				for _, lhs := range d.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						markDecl(id)
					}
				}
			}
		case *ast.LabeledStmt:
			markDecl(d.Label)
		}
		return true
	})
	return declSites
}

// goReferencesFromFile collects the Reference-role occurrences in file,
// excluding any offset present in declSites. Shared by ExtractRefs and
// ExtractDefsRefs so both derive references from a single parsed *ast.File.
func goReferencesFromFile(file *ast.File, off func(token.Pos) int, declSites map[int]bool) []Occurrence {
	// Each emitted occurrence is keyed by NameStart offset so we de-duplicate
	// and never emit a declaring site.
	emitted := map[int]bool{}
	var occs []Occurrence
	emit := func(id *ast.Ident, kind Kind) {
		if id == nil || id.Name == "" || id.Name == "_" {
			return
		}
		o := off(id.Pos())
		if o < 0 || declSites[o] || emitted[o] {
			return
		}
		emitted[o] = true
		occs = append(occs, Occurrence{
			Name:  id.Name,
			Kind:  kind,
			Role:  Reference,
			Start: o,
			End:   o + len(id.Name),
		})
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch e := n.(type) {
		case *ast.CallExpr:
			// The call target: f(...) -> Func ref; pkg.F(...) -> the .F selector
			// is handled by the SelectorExpr case, so only emit the bare-ident
			// callee here.
			if id, ok := e.Fun.(*ast.Ident); ok {
				emit(id, Func)
			}
		case *ast.SelectorExpr:
			// x.Field / pkg.Name / recv.Method -> the selected name is a
			// reference. The base expression is visited separately by Inspect.
			emit(e.Sel, Method)
		case *ast.CompositeLit:
			// T{...} and []T{...} type idents are references to the type.
			switch t := e.Type.(type) {
			case *ast.Ident:
				emit(t, Type)
			case *ast.ArrayType:
				if id, ok := t.Elt.(*ast.Ident); ok {
					emit(id, Type)
				}
			case *ast.MapType:
				if id, ok := t.Value.(*ast.Ident); ok {
					emit(id, Type)
				}
			}
		}
		return true
	})

	return occs
}

// Build runs ext over every blob in ix and returns a symbol Index. Blobs whose
// content does not parse (ext returns an error) simply get no symbols — they are
// skipped, never fatal. Blobs that parse but yield no symbols are also skipped.
func Build(ix *index.Index, ext Extractor) *Index {
	out := NewIndex()
	if ix == nil || ext == nil {
		return out
	}
	for id := uint64(0); id < uint64(ix.NumBlobs()); id++ {
		blob := ix.Blob(id)
		if blob == nil {
			continue
		}
		syms, occs, err := extractDefsRefs(ext, blob.Content)
		if err != nil || len(syms) == 0 {
			continue
		}
		out.Set(id, syms)
		if len(occs) > 0 {
			out.SetRefs(id, occs)
		}
	}
	return out
}

// extractDefsRefs runs ext over content and returns both definitions and
// references, preferring a single combined parse/scan pass (DefsRefsExtractor)
// when ext implements it, and falling back to the separate Extract +
// ExtractRefs calls otherwise (an extractor that only implements RefExtractor,
// or one that implements neither and so has no references at all). Build and
// BuildMulti share this so the "one combined call where possible" behavior —
// and any future per-language upgrade to DefsRefsExtractor — lives in one
// place.
func extractDefsRefs(ext Extractor, content []byte) ([]Symbol, []Occurrence, error) {
	if combined, ok := ext.(DefsRefsExtractor); ok {
		return combined.ExtractDefsRefs(content)
	}
	syms, err := ext.Extract(content)
	if err != nil {
		return nil, nil, err
	}
	if refExt, ok := ext.(RefExtractor); ok {
		if occs, rerr := refExt.ExtractRefs(content); rerr == nil {
			return syms, occs, nil
		}
	}
	return syms, nil, nil
}
