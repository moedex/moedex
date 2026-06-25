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

	// off maps a token.Pos to a byte offset into content. token.NoPos -> -1.
	off := func(p token.Pos) int {
		if !p.IsValid() {
			return -1
		}
		return fset.Position(p).Offset
	}

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

	return syms, nil
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
	off := func(p token.Pos) int {
		if !p.IsValid() {
			return -1
		}
		return fset.Position(p).Offset
	}

	// Pass 1: collect the byte offsets of every DECLARING ident so the reference
	// pass can exclude them. ast.Ident.Pos() is stable per node, so a set of
	// declaring NameStart offsets is sufficient to filter def sites.
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

	// Pass 2: collect references. Each emitted occurrence is keyed by NameStart
	// offset so we de-duplicate and never emit a declaring site.
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

	return occs, nil
}

// Build runs ext over every blob in ix and returns a symbol Index. Blobs whose
// content does not parse (ext returns an error) simply get no symbols — they are
// skipped, never fatal. Blobs that parse but yield no symbols are also skipped.
func Build(ix *index.Index, ext Extractor) *Index {
	out := NewIndex()
	if ix == nil || ext == nil {
		return out
	}
	refExt, hasRefs := ext.(RefExtractor)
	for id := uint64(0); id < uint64(ix.NumBlobs()); id++ {
		blob := ix.Blob(id)
		if blob == nil {
			continue
		}
		syms, err := ext.Extract(blob.Content)
		if err != nil || len(syms) == 0 {
			continue
		}
		out.Set(id, syms)
		if hasRefs {
			if occs, rerr := refExt.ExtractRefs(blob.Content); rerr == nil && len(occs) > 0 {
				out.SetRefs(id, occs)
			}
		}
	}
	return out
}
