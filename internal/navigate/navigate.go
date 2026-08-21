// Package navigate is moedex's experimental LSP-precise navigation arm: the
// type-resolved go-to-definition / find-references / find-implementations that
// the syntactic symbol sidecar (ADR 0008) deliberately cannot answer.
//
// This package spikes all three ADR 0017 conditions for subsuming Serena:
//   - Condition 1 (real LSP semantics): LSP (gopls) driven out of process over
//     JSON-RPC returns type-resolved definition / references / implementation.
//   - Condition 2 (concurrency-safe under parallel lanes): Pool shares one
//     server per project root across lanes; the client is hardened for concurrent
//     use and the process lifetime is decoupled from any request's context.
//   - Condition 3 (live working-tree freshness): queries re-sync the working tree
//     via didChange, SetOverlay drives navigation over unsaved in-memory buffers,
//     and NotifyChanged invalidates files edited on disk.
//
// It is still a spike, not a finished Serena replacement: it covers the
// in-process single-host case for one language (Go/gopls). Cross-process or
// multi-host server sharing, editor-grade incremental (non-full-text) sync, and
// additional language servers are out of scope. See ADR 0017 for the full gate.
//
// Build boundary (locked, mirrors the dense arm, ADR 0007):
//   - The default build is pure-Go zero-dep. This package's types and interface
//     compile unconditionally, but the real client is behind `-tags lsp`.
//   - `-tags lsp` pulls in NO new go.mod dependencies: the JSON-RPC client is
//     written against the standard library (os/exec + encoding/json over a
//     Content-Length framed stdio pipe). gopls is an external binary the caller
//     supplies on PATH, not a linked dependency.
//   - Without the tag, NewLSP returns a clear "rebuild with -tags lsp" error.
//
// Coordinates: positions are 1-based (line, column) — the convention grep,
// editors, and humans use — with columns measured in BYTES, not UTF-16 code
// units. The LSP client negotiates the utf-8 positionEncoding capability with
// gopls so server columns are byte columns, keeping navigation aligned with
// moedex's byte-offset core (ADR 0002). The 0-based UTF-16 impedance that
// normally plagues LSP integrations is therefore handled at the protocol seam,
// not leaked into this API.
package navigate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Pos is a 1-based (line, column) cursor position inside a file. Column is a
// byte column within the line (see package doc). It is the input to every
// navigation query: "what is the symbol under this cursor, and where does it
// resolve / who references it / what implements it."
type Pos struct {
	File string // absolute or workspace-relative path to a source file
	Line int    // 1-based line number
	Col  int    // 1-based byte column within the line
}

func (p Pos) String() string { return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col) }

// Location is a resolved source range returned by a navigation query. Start and
// End are 1-based (line, byte-column); End is exclusive at the column granularity
// gopls reports. A zero End (both fields 0) means the server returned only a
// point, not a range.
type Location struct {
	File  string
	Start Pos
	End   Pos
}

func (l Location) String() string { return l.Start.String() }

// LocationQueryStatus describes what a language server actually told us about
// a position-based navigation request. It deliberately does not infer that an
// empty answer means the symbol is external: an initialized server can return
// null while it is still indexing, or simply because it cannot resolve the
// cursor position.
type LocationQueryStatus string

const (
	// LocationQueryResolved means the server returned one or more locations.
	LocationQueryResolved LocationQueryStatus = "resolved"
	// LocationQueryReadyEmpty means the request completed successfully and the
	// server returned null or an empty location array. It is not proof that the
	// symbol lives outside the workspace.
	LocationQueryReadyEmpty LocationQueryStatus = "ready_empty"
	// LocationQueryUnsupported means the server returned JSON-RPC -32601 for
	// this method.
	LocationQueryUnsupported LocationQueryStatus = "unsupported"
	// LocationQueryUnavailable means the request could not produce an
	// authoritative answer because startup, transport, timeout, cancellation,
	// privacy, synchronization, or response decoding failed.
	LocationQueryUnavailable LocationQueryStatus = "unavailable"
)

// LocationQueryResult is the detailed result for definition, references, and
// implementation requests. Existing slice-returning methods remain available
// as compatibility wrappers; new accuracy-sensitive callers should use the
// Detailed variants so empty, unsupported, and failed requests are not
// conflated.
type LocationQueryResult struct {
	Status    LocationQueryStatus `json:"status"`
	Locations []Location          `json:"locations,omitempty"`
}

// DetailedNavigator is the status-preserving navigation surface. It is kept
// separate from Navigator so existing third-party implementations of the
// original interface continue to compile.
type DetailedNavigator interface {
	DefinitionDetailed(ctx context.Context, at Pos) (LocationQueryResult, error)
	ReferencesDetailed(ctx context.Context, at Pos, includeDecl bool) (LocationQueryResult, error)
	ImplementationsDetailed(ctx context.Context, at Pos) (LocationQueryResult, error)
}

// legacyLocations implements the compatibility contract shared by LSP and
// Pool: resolved locations pass through, successful empty/unsupported results
// remain (nil, nil), and unavailable results preserve their error.
func legacyLocations(result LocationQueryResult, err error) ([]Location, error) {
	if err != nil {
		return nil, err
	}
	return result.Locations, nil
}

// Symbol is a named, kind-tagged declaration returned by the name-based
// navigation tools (ADR 0018: workspace/symbol and textDocument/documentSymbol)
// — as opposed to the bare Location the position-based tools return. Kind is
// the LSP SymbolKind spelled out as text (e.g. "Function", "Struct",
// "Interface"), not the raw protocol integer.
type Symbol struct {
	Name string
	Kind string
	Loc  Location
}

// String is the ADR 0018 pinned text-result line format: name TAB kind TAB
// file:line:col. find_symbol and symbols_overview render one Symbol per line
// this way; Protostar's moedex-nav parser mirrors it.
func (s Symbol) String() string { return s.Name + "\t" + s.Kind + "\t" + s.Loc.Start.String() }

// Navigator is the type-resolved navigation surface. Every method takes a cursor
// Pos and returns resolved Locations across the whole workspace (cross-file,
// import-aware, overload- and inheritance-aware) — the semantics ADR 0008's
// syntactic sidecar cannot provide.
//
// Implementations must be safe to Close exactly once; calling any method after
// Close returns an error. The LSP implementation is safe for concurrent calls
// from many lanes (ADR 0017 Condition 2); Pool shares one such Navigator per
// project root across lanes.
type Navigator interface {
	// Definition resolves the symbol under the cursor to its declaration
	// site(s). Usually one location; can be several for embedded/aliased decls.
	Definition(ctx context.Context, at Pos) ([]Location, error)

	// References finds every reference to the symbol under the cursor across the
	// workspace. If includeDecl is true the declaration itself is included.
	References(ctx context.Context, at Pos, includeDecl bool) ([]Location, error)

	// Implementations finds the concrete implementations of the interface (or
	// interface method) under the cursor — the find_implementations role.
	Implementations(ctx context.Context, at Pos) ([]Location, error)

	// Close shuts the underlying language server down. Idempotent.
	Close() error
}

// moduleRoot walks up from a file to the nearest directory containing go.mod,
// falling back to the file's own directory. gopls resolves a workspace best from
// its module root; the Pool also keys one server per module root off this. Lives
// in the unguarded file so the Pool can route by file in either build arm.
func moduleRoot(file string) string {
	dir := file
	if fi, err := os.Stat(file); err != nil || !fi.IsDir() {
		dir = filepath.Dir(file)
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Dir(file)
		}
		dir = parent
	}
}
