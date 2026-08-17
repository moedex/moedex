package candidates

// set.go is the corpus-wide sweep and the in-memory store it fills — the handoff
// to phase 4's verification pass.
//
// Candidates are held in memory until phase 4 assigns a confidence tier. The
// phase-5 sidecar consumes those scored edges, not this unannotated work list;
// weak candidates are retained as Candidate-tier evidence rather than silently
// discarded.
//
// The store is keyed by name because that is how verification is shaped: a
// verifier is chosen per language and per symbol, and every candidate for one
// name shares that choice, so a name's candidates are the natural unit of work
// (and of concurrency) for the next phase.

import (
	"sort"
	"unicode"
	"unicode/utf8"

	"moedex/internal/trigram"
)

// Options gates a corpus-wide sweep. The zero value is the default policy:
// exported names of at least trigram length.
type Options struct {
	// Include reports whether a name should be fanned out over. nil means
	// Exported — the plan's "for each exported definition", since a name no other
	// repo can reference cannot carry a cross-repo edge. Phase 2's framework
	// classifiers supply their own predicate to sweep a specific node set, and
	// func(string) bool { return true } sweeps everything.
	Include func(name string) bool

	// MinNameLen is the shortest name (in bytes) the sweep will spend on. 0 means
	// trigram.N. A name shorter than a trigram has no gram to look up, so its
	// fan-out degenerates to a full content scan of every shard — affordable for
	// one lookup, not for a corpus-wide sweep. GenerateCandidates still answers
	// such a name correctly on request; this only bounds the sweep. Set to 1 to
	// include them.
	MinNameLen int
}

// resolve fills in the zero value's defaults.
func (o Options) resolve() (func(string) bool, int) {
	include := o.Include
	if include == nil {
		include = Exported
	}
	minLen := o.MinNameLen
	if minLen <= 0 {
		minLen = trigram.N
	}
	return include, minLen
}

// Exported reports whether name looks like an exported/public identifier: its
// first rune is an upper-case letter.
//
// This is a cross-language CONVENTION, not a visibility analysis. It is exact for
// Go (it is the language rule, matching go/token.IsExported) and a good proxy for
// the PascalCase public surface of C#, TypeScript, and SQL, which is what the
// corpus is mostly made of. It cannot see a C# `private` on a PascalCase member,
// so it over-admits there — the safe direction for a recall-complete pass, and a
// distinction the symbol layer does not currently record. Callers who need real
// visibility supply their own Options.Include.
func Exported(name string) bool {
	if name == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}

// Sweep records what a corpus-wide sweep covered, so an empty result is
// attributable: "nothing in this corpus references anything" and "the filter
// excluded every name" are very different answers and must not look alike.
type Sweep struct {
	Names     int // distinct names in the corpus-wide byName index
	Generated int // names the filter admitted and fanned out over
	Excluded  int // names the filter excluded (Include / MinNameLen)
}

// Set is the in-memory candidate store: the edge list plus a by-name index over
// it. It is not safe for concurrent mutation; it is read-only once a sweep
// returns it.
type Set struct {
	edges  []Edge
	byName map[string][]int // name -> positions in edges
	sweep  Sweep
}

// NewSet returns an empty candidate store.
func NewSet() *Set {
	return &Set{byName: map[string][]int{}}
}

// Add appends edges to the store and indexes them by name. Adding no edges is a
// no-op, so a caller can pass a GenerateCandidates result straight through
// without checking it.
func (s *Set) Add(edges ...Edge) {
	if s.byName == nil {
		s.byName = map[string][]int{}
	}
	for _, e := range edges {
		s.byName[e.Name] = append(s.byName[e.Name], len(s.edges))
		s.edges = append(s.edges, e)
	}
}

// Len reports how many candidate edges the store holds.
func (s *Set) Len() int { return len(s.edges) }

// Edges returns every candidate edge in insertion order, without copying. A
// GenerateAll sweep inserts name-sorted, and each name's edges are already in
// (source, target, type) order, so a swept Set iterates deterministically.
func (s *Set) Edges() []Edge { return s.edges }

// Names returns the sorted names that have at least one candidate edge — the
// work list for the verification pass.
func (s *Set) Names() []string {
	if len(s.byName) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.byName))
	for n := range s.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ForName returns the candidate edges generated for name, in insertion order, or
// nil when the store holds none. The result is a fresh slice; the Edges it holds
// are copies, so a verifier may annotate them freely.
func (s *Set) ForName(name string) []Edge {
	idx := s.byName[name]
	if len(idx) == 0 {
		return nil
	}
	out := make([]Edge, 0, len(idx))
	for _, i := range idx {
		out = append(out, s.edges[i])
	}
	return out
}

// Sweep reports the coverage of the sweep that filled this store (the zero value
// for a hand-built one).
func (s *Set) Sweep() Sweep { return s.sweep }

// GenerateAll sweeps the whole corpus: it fans out over every name the
// corpus-wide index knows and that opts admits, and returns the accumulated
// candidate store. Names are visited in sorted order, so the store is
// reproducible even though the underlying name index iterates in map order.
//
// A name the filter admits but that no shard DEFINES contributes no edges (there
// is no target to point at) and costs only the definition lookup — the fan-out
// never runs for it. The returned Set's Sweep explains what was covered and what
// the filter left out.
//
// This is the offline half of the graph build (phase 5 runs it alongside the
// index refresh), not a request-path operation: the sweep is proportional to the
// corpus's distinct exported names times their occurrences. Generation per name is
// read-only and independent, so a caller that needs it faster can fan out over
// Names concurrently against the same Corpus.
func GenerateAll(c *Corpus, opts Options) *Set {
	set := NewSet()
	if c == nil {
		return set
	}
	include, minLen := opts.resolve()

	// Collect-then-sort rather than generating inside EachName: map order varies
	// per run, and a candidate list that reorders between runs cannot be diffed,
	// cached, or resumed. This holds one string slice of admitted names — the
	// names themselves are the merged index's own strings, so no bytes are copied.
	var names []string
	c.syms.EachName(func(name string) bool {
		set.sweep.Names++
		if len(name) < minLen || !include(name) {
			set.sweep.Excluded++
			return true
		}
		names = append(names, name)
		return true
	})
	sort.Strings(names)

	for _, name := range names {
		set.sweep.Generated++
		set.Add(GenerateCandidates(c, name)...)
	}
	return set
}
