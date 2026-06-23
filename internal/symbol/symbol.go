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
// No find-refs, no go-to-def, no symbol-name ranking arm — those are later
// slices. This package is pure data plus the Go extractor and a sidecar codec.
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

// Index maps a blob (by its index.Index blob ID) to its symbols. Each blob's
// slice is kept sorted by BodyStart so Enclosing can scan deterministically and
// resolve nesting (the innermost covering range).
type Index struct {
	byBlob map[uint64][]Symbol
}

// NewIndex returns an empty symbol index.
func NewIndex() *Index {
	return &Index{byBlob: map[uint64][]Symbol{}}
}

// Set installs syms as the symbols for blob, sorting them by BodyStart (ties
// broken by the wider range first, so an outer symbol sorts before a symbol it
// encloses that happens to share a BodyStart). A nil/empty slice clears the blob.
func (ix *Index) Set(blob uint64, syms []Symbol) {
	if len(syms) == 0 {
		delete(ix.byBlob, blob)
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
}

// Symbols returns the symbols for blob (sorted by BodyStart), or nil.
func (ix *Index) Symbols(blob uint64) []Symbol {
	return ix.byBlob[blob]
}

// NumBlobs returns how many blobs have at least one symbol.
func (ix *Index) NumBlobs() int { return len(ix.byBlob) }

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
