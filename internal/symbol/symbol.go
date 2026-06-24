// Package symbol holds moedex's syntactic symbol layer: per-blob definition
// ranges (functions, methods, types, consts, vars) used to scope context blocks
// to real symbol boundaries instead of the brace/indent heuristic in
// internal/contextwin.
//
// Slice 1 scope (Go only, syntactic only):
//
//   - Symbols are extracted out of a blob's content by an Extractor. The only
//     Extractor here is GoExtractor, built on the standard library go/parser +
//     go/ast + go/token — no cgo, no third-party deps, fully hermetic. Other
//     languages (via tree-sitter / SCIP) are deferred to later slices behind the
//     Extractor interface.
//   - Ranges are BYTE offsets into Blob.Content. moedex is byte-addressed since
//     slice 4, and go/token gives exact byte offsets via FileSet.Position().Offset,
//     so no line/column-to-byte conversion (and its multi-byte-rune hazards) is
//     needed. The hazard is pushed entirely into the FileSet, which already counts
//     bytes.
//
// Find-references (the redesign/deep-symbols slice) is SYNTACTIC and NAME-BASED:
// extractors that implement RefExtractor emit reference Occurrences (call sites,
// selectors, constructions) alongside definitions, and the Index builds a
// name -> []Ref inverted view so References(name) returns the definition(s) AND
// their usages within the indexed blobs. This is NOT semantic cross-file
// resolution (two distinct symbols sharing a name collide); true go-to-def /
// SCIP ingest stays out-of-process and deferred per research/symbol-layer.md.
// The Role/Occurrence model is forward-compatible with a SCIP ingest seam:
// SCIP's SymbolRole bitset maps onto Role and SCIP Occurrences map onto
// Occurrence.
//
// This package is pure data plus the language extractors and a sidecar codec —
// no cgo, no third-party dependency, fully hermetic.
package symbol

import "sort"

// Kind classifies a symbol's syntactic role.
type Kind uint8

const (
	// KindUnknown is the zero value; never emitted by GoExtractor.
	KindUnknown Kind = iota
	// Func is a top-level (or nested) function with no receiver.
	Func
	// Method is a function with a receiver.
	Method
	// Type is a type declaration (struct, interface, alias, ...).
	Type
	// Const is a constant declaration.
	Const
	// Var is a variable declaration.
	Var
)

// String renders a Kind for diagnostics and tests.
func (k Kind) String() string {
	switch k {
	case Func:
		return "Func"
	case Method:
		return "Method"
	case Type:
		return "Type"
	case Const:
		return "Const"
	case Var:
		return "Var"
	default:
		return "Unknown"
	}
}

// Symbol is one named definition within a blob. All offsets are BYTE offsets
// into Blob.Content.
type Symbol struct {
	Name      string // e.g. "Assemble"
	Kind      Kind
	NameStart int // byte offset of the name token (for name-match ranking)
	NameEnd   int // byte offset just past the name token
	BodyStart int // byte offset of the enclosing definition's first byte
	BodyEnd   int // byte offset just past the definition's last byte
}

// Role classifies a name occurrence as a definition or a reference. It mirrors
// (a subset of) SCIP's SymbolRole so a future SCIP ingest can map directly onto
// this model.
type Role uint8

const (
	// Definition is the declaring occurrence of a name (a func/type/method/...
	// declaration). Every Symbol contributes a Definition occurrence.
	Definition Role = iota
	// Reference is a non-declaring use of a name (a call site, selector,
	// constructor, etc.). References are NAME-BASED and SYNTACTIC: they are not
	// resolved across files, so distinct symbols sharing a name collide.
	Reference
)

// String renders a Role for diagnostics and tests.
func (r Role) String() string {
	switch r {
	case Definition:
		return "Definition"
	case Reference:
		return "Reference"
	default:
		return "Role?"
	}
}

// Occurrence is one name occurrence within a blob — either a Definition or a
// Reference. Unlike Symbol it carries no body range (a reference has no
// meaningful body), only the byte range of the name token itself. Start/End are
// BYTE offsets into Blob.Content.
type Occurrence struct {
	Name  string
	Kind  Kind
	Role  Role
	Start int // byte offset of the name token
	End   int // byte offset just past the name token
}

// Ref is a name occurrence located by blob — the unit References/Definitions
// return. Start/End are BYTE offsets into that blob's content.
type Ref struct {
	Blob  uint64
	Start int
	End   int
	Role  Role
}

// Index maps a blob (by its index.Index blob ID) to its symbols. Each blob's
// slice is kept sorted by BodyStart so Enclosing can scan deterministically and
// resolve nesting (the innermost covering range).
//
// refsByBlob holds the per-blob Reference occurrences emitted by RefExtractor
// implementations (definitions continue to live in byBlob as Symbols).
//
// byName is a name -> []Ref inverted view folding BOTH definition names (from
// byBlob) and reference occurrences (from refsByBlob). It is the backing store
// for References/Definitions and is rebuilt incrementally as blobs are Set /
// SetRefs.
type Index struct {
	byBlob     map[uint64][]Symbol
	refsByBlob map[uint64][]Occurrence
	byName     map[string][]Ref
}

// NewIndex returns an empty symbol index.
func NewIndex() *Index {
	return &Index{
		byBlob:     map[uint64][]Symbol{},
		refsByBlob: map[uint64][]Occurrence{},
		byName:     map[string][]Ref{},
	}
}

// Set installs syms as the symbols for blob, sorting them by BodyStart (ties
// broken by the wider range first, so an outer symbol sorts before a symbol it
// encloses that happens to share a BodyStart). A nil/empty slice clears the blob.
//
// Set also folds the definitions' names into the byName inverted view so
// References/Definitions surface them. References already recorded for this blob
// (via SetRefs) are preserved.
func (ix *Index) Set(blob uint64, syms []Symbol) {
	if len(syms) == 0 {
		delete(ix.byBlob, blob)
		ix.rebuildName(blob)
		return
	}
	cp := append([]Symbol(nil), syms...)
	sort.SliceStable(cp, func(i, j int) bool {
		if cp[i].BodyStart != cp[j].BodyStart {
			return cp[i].BodyStart < cp[j].BodyStart
		}
		// Same start: wider (outer) range first.
		return cp[i].BodyEnd > cp[j].BodyEnd
	})
	ix.byBlob[blob] = cp
	ix.rebuildName(blob)
}

// SetRefs installs occs as the reference occurrences for blob and refolds the
// byName inverted view for that blob. Only Reference-role occurrences are
// stored (definition occurrences are derived from byBlob's Symbols, so a caller
// passing Definition-role entries here would not corrupt anything but is
// redundant and filtered out). A nil/empty slice clears the blob's references.
func (ix *Index) SetRefs(blob uint64, occs []Occurrence) {
	refs := occs[:0:0]
	for _, o := range occs {
		if o.Role != Reference || o.Name == "" {
			continue
		}
		refs = append(refs, o)
	}
	if len(refs) == 0 {
		delete(ix.refsByBlob, blob)
	} else {
		sort.SliceStable(refs, func(i, j int) bool { return refs[i].Start < refs[j].Start })
		ix.refsByBlob[blob] = refs
	}
	ix.rebuildName(blob)
}

// rebuildName recomputes byName entries that involve blob: it first strips every
// existing byName Ref pointing at blob, then re-adds the blob's current
// definitions (from byBlob) and references (from refsByBlob). This keeps byName
// consistent under repeated Set/SetRefs without a full rebuild over all blobs.
func (ix *Index) rebuildName(blob uint64) {
	if ix.byName == nil {
		ix.byName = map[string][]Ref{}
	}
	// Drop existing entries for this blob.
	for name, refs := range ix.byName {
		kept := refs[:0]
		for _, r := range refs {
			if r.Blob != blob {
				kept = append(kept, r)
			}
		}
		if len(kept) == 0 {
			delete(ix.byName, name)
		} else {
			ix.byName[name] = kept
		}
	}
	// Re-add definitions.
	for _, s := range ix.byBlob[blob] {
		if s.Name == "" {
			continue // unnamed (func literals) carry no name to look up
		}
		ix.byName[s.Name] = append(ix.byName[s.Name], Ref{
			Blob:  blob,
			Start: s.NameStart,
			End:   s.NameEnd,
			Role:  Definition,
		})
	}
	// Re-add references.
	for _, o := range ix.refsByBlob[blob] {
		ix.byName[o.Name] = append(ix.byName[o.Name], Ref{
			Blob:  blob,
			Start: o.Start,
			End:   o.End,
			Role:  Reference,
		})
	}
}

// Symbols returns the symbols for blob (sorted by BodyStart), or nil.
func (ix *Index) Symbols(blob uint64) []Symbol {
	return ix.byBlob[blob]
}

// Refs returns the reference occurrences for blob (sorted by Start), or nil.
// Definitions are NOT included — use Symbols for those.
func (ix *Index) Refs(blob uint64) []Occurrence {
	return ix.refsByBlob[blob]
}

// NumBlobs returns how many blobs have at least one symbol.
func (ix *Index) NumBlobs() int { return len(ix.byBlob) }

// NumRefBlobs returns how many blobs carry at least one reference occurrence.
func (ix *Index) NumRefBlobs() int { return len(ix.refsByBlob) }

// sortRefs orders Refs by Blob, then Start, then Role (Definition before
// Reference) for a deterministic, def-first result.
func sortRefs(refs []Ref) {
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].Blob != refs[j].Blob {
			return refs[i].Blob < refs[j].Blob
		}
		if refs[i].Start != refs[j].Start {
			return refs[i].Start < refs[j].Start
		}
		return refs[i].Role < refs[j].Role
	})
}

// References returns every occurrence of name across the indexed blobs — both
// the definition(s) (Role=Definition) and the references (Role=Reference) —
// sorted by blob, then byte offset. It returns nil (not a panic) for an unknown
// name. The result is a fresh copy; mutating it does not affect the index.
//
// References are SYNTACTIC and NAME-BASED, so all symbols sharing this name
// collide — this is find-references-by-name, not resolved go-to-def.
func (ix *Index) References(name string) []Ref {
	src := ix.byName[name]
	if len(src) == 0 {
		return nil
	}
	out := append([]Ref(nil), src...)
	sortRefs(out)
	return out
}

// Definitions returns only the definition occurrences (Role=Definition) of name
// across the indexed blobs, sorted by blob then byte offset, or nil for an
// unknown name. The result is a fresh copy.
func (ix *Index) Definitions(name string) []Ref {
	src := ix.byName[name]
	if len(src) == 0 {
		return nil
	}
	out := make([]Ref, 0, len(src))
	for _, r := range src {
		if r.Role == Definition {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil
	}
	sortRefs(out)
	return out
}

// Enclosing returns the innermost symbol whose [BodyStart, BodyEnd) covers byte
// offset off, and true. When no symbol covers off it returns a zero Symbol and
// false. Innermost = the smallest covering range, so nested funcs/methods win
// over their enclosing symbol.
func (ix *Index) Enclosing(blob uint64, off int) (Symbol, bool) {
	syms := ix.byBlob[blob]
	if len(syms) == 0 {
		return Symbol{}, false
	}
	var best Symbol
	found := false
	for _, s := range syms {
		if off < s.BodyStart {
			// Sorted by BodyStart ascending: no later symbol can start at or
			// before off either, so we are done.
			break
		}
		if off < s.BodyEnd { // off in [BodyStart, BodyEnd)
			if !found || (s.BodyEnd-s.BodyStart) < (best.BodyEnd-best.BodyStart) {
				best = s
				found = true
			}
		}
	}
	return best, found
}

// EnclosingBytesFunc returns a closure suitable for
// contextwin.Options.EnclosingBytes: given a blob ID and a byte offset, it
// reports the innermost enclosing symbol's [startByte, endByte) and ok. The
// closure captures ix, so contextwin needs no import of this package.
func (ix *Index) EnclosingBytesFunc() func(blob uint64, byteOff int) (int, int, bool) {
	return func(blob uint64, byteOff int) (int, int, bool) {
		s, ok := ix.Enclosing(blob, byteOff)
		if !ok {
			return 0, 0, false
		}
		return s.BodyStart, s.BodyEnd, true
	}
}
