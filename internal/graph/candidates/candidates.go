// Package candidates generates the recall-complete edge candidate list the graph
// layer's verification pass scores (phase 3 of docs/GRAPH-LAYER-PLAN.md).
//
// The bridge architecture is a classic two-phase IR split: a cheap filter that
// must never under-approximate, then a precision pass that assigns confidence.
// This package is the filter. It answers, for one symbol name:
//
//	"every position in the corpus that could be a use of a definition of this
//	 name, paired with every definition it could be a use of"
//
// and returns that as an Edge list. It resolves nothing: name-based lookup cannot
// tell which of five same-named definitions a call site targets, and a byte match
// cannot tell a call site from a word in a comment. Both ambiguities are handled
// by OVER-generating — every source is paired with every definition, and unclear
// evidence is tagged rather than dropped. Phase 4 promotes regex-confirmed uses
// to Pattern confidence and retains everything else at Candidate confidence, so
// a later verifier never has to recreate this corpus-wide work list.
//
// # Two arms, unioned
//
// Sources come from two arms, and the union is what makes the result complete:
//
//   - The corpus-wide byName index (phase 1, symbol.Corpus). Precise: an
//     extractor decided this position is a call/selector/construction. Incomplete:
//     only some languages have a RefExtractor, and none of them see comments,
//     strings, config files, or SQL.
//   - The positional trigram index. Complete for byte occurrences in EVERY
//     indexed blob regardless of language, which is exactly the recall the symbol
//     arm cannot reach — and the reason the plan calls this step a trigram
//     fan-out. Imprecise: a byte match is only a byte match.
//
// Where both arms find the same position the symbol arm's classification wins; a
// position only the trigram arm found is tagged TextOccurrence. See Type.
//
// # The one filter applied
//
// The trigram arm keeps only occurrences that stand alone as an identifier token
// (see identifierAt): "Add" inside "Address" is rejected. This is a NECESSARY
// condition for a name reference in every language moedex indexes — a use of Add
// is spelled Add, never as a fragment of a longer identifier — so it cannot drop
// a real reference, which is the same discipline the trigram->regex reduction
// follows in internal/query. Nothing else is filtered: comments, strings, and
// unrelated same-named symbols in other repos all survive as candidates.
//
// # Cost
//
// Per name the fan-out is (sources x definitions) edges, and both factors are
// corpus-wide. A common short name in a large corpus therefore generates a lot of
// candidates; that is inherent to recall-complete name-based generation, not a
// defect, and it is why Options gates a corpus-wide sweep to exported,
// trigram-length names by default. Preparation and generation are read-only and
// hold no locks; PreparedName lets an offline caller split one common name into
// bounded source ranges, so a high-frequency identifier cannot become a
// one-worker tail.
//
// The package is pure standard library, and — like the rest of the engine — never
// copies blob content: an Edge is a (shard, blob, byte offset) triple resolved
// against the shards it came from.
package candidates

import (
	"fmt"
	"sort"

	"moedex/internal/graph"
	"moedex/internal/index"
	"moedex/internal/symbol"
)

// Type classifies the EVIDENCE that produced a candidate, not the semantic edge
// type. Semantic typing (CALLS, IMPORTS, IMPLEMENTS, ...) is phase 4's job: it
// needs language context this pass deliberately does not consult. What a caller
// gets here is how much the position is already known to be a real use, which is
// what decides which verifier to spend on it and how cheap that verifier can be.
type Type uint8

const (
	// TypeUnknown is the zero value and is never emitted.
	TypeUnknown Type = iota
	// SymbolReference: the symbol layer classified the source position as a
	// non-declaring use — a call site, selector, or construction emitted by a
	// language extractor. The strongest candidate class: something already parsed
	// the language and decided this is a use.
	SymbolReference
	// TextOccurrence: the name occurs as a standalone identifier at the source
	// position, but the symbol layer does not classify it — an unextracted
	// language, a comment, a string literal, a config or SQL file. This is the
	// class the trigram arm exists to add, and the one phase 4 must verify hardest.
	TextOccurrence
	// SiblingDefinition: the source position is itself a DEFINITION of the same
	// name somewhere else. Not a use, and deliberately kept: an override, an
	// interface implementation, and a plain name collision across repos are
	// indistinguishable without type resolution, and the first two are real edges
	// (INHERITS/IMPLEMENTS) that phase 4 can confirm. A definition never pairs
	// with itself (see GenerateCandidates).
	SiblingDefinition
)

// String renders a Type for diagnostics and tests.
func (t Type) String() string {
	switch t {
	case SymbolReference:
		return "SymbolReference"
	case TextOccurrence:
		return "TextOccurrence"
	case SiblingDefinition:
		return "SiblingDefinition"
	default:
		return "Unknown"
	}
}

// Site is one end of a candidate edge: a byte range within a blob of a specific
// shard.
//
// Shard qualification is not decoration. A symbol index's blob IDs are
// SHARD-LOCAL (see internal/symbol/corpus.go), so blob 7 exists in every shard
// and means something different in each; an unqualified blob ID would silently
// fold distinct sites together. Start/End are byte offsets into that blob's
// content. Resolve a Site with (*Corpus).Blob or (*Corpus).Text.
//
// A Site is CONTENT-addressed within its shard, which is the graph win moedex
// gets for free: a vendored file committed to forty repos of one shard is a single
// blob, so a reference inside it is ONE site carrying forty FileRefs rather than
// forty duplicate edges. Across shards that content is a distinct blob per shard
// (a shard owns its blob table), so the same reference resolves to one site per
// shard — the shared blob SHA, reachable via (*Corpus).Blob, is what folds those
// back into a single content node.
type Site struct {
	Shard int    // shard ID, as assigned by symbol.Corpus.AddShard
	Blob  uint64 // blob ID within THAT shard's index
	Start int    // byte offset of the name token
	End   int    // byte offset just past the name token
}

// Edge is one candidate edge: a position that might use a definition, and the
// definition it might use.
//
// The direction is USE -> DEFINITION (caller to callee, importer to imported),
// so following Source->Target walks toward what code depends on and inverting it
// walks toward blast radius. Name is the name that generated the candidate; both
// ends are occurrences of it, which is the whole basis of the pairing and the
// reason the pairing is a candidate rather than a fact.
type Edge struct {
	Name       string
	Source     Site
	Target     Site
	Type       Type
	Confidence graph.ConfidenceTier
	Evidence   graph.Evidence

	// sourceBlob keeps the evidence bytes attached to the work item without
	// copying them. It is deliberately private: Source remains the stable graph
	// identity, while the pointer is only the transient phase-3 -> phase-4
	// handoff and must never be persisted.
	sourceBlob *index.Blob
}

// EvidenceBlob returns the blob containing the candidate's source occurrence.
// The returned blob is borrowed from the Corpus that generated the edge; callers
// must not mutate it, and that Corpus (including any mmap backing it) must outlive
// verification. A hand-built Edge has no attached evidence and returns nil.
//
// Keeping this resolver on the transient candidate lets phase 4 expose the
// natural Verify(candidates) API: a scored edge can be produced from the work
// list itself without making every caller carry a parallel shard table.
func (e Edge) EvidenceBlob() *index.Blob { return e.sourceBlob }

// CrossShard reports whether the edge spans two shards — the cross-repo edges
// that no per-shard index can find, and the ones Codegraph pays a full parse per
// repo to discover.
func (e Edge) CrossShard() bool { return e.Source.Shard != e.Target.Shard }

// Corpus is the read-only corpus view candidate generation fans out over: the
// corpus-wide byName index from phase 1, plus the per-shard content/trigram
// index that answers "at which byte offsets does this name occur".
//
// Both are needed and neither substitutes for the other: symbol.Corpus supplies
// the definitions (the targets) and the classified references, the trigram
// indices supply the recall. idxs[i] must be the content index of shard i as
// symbol.Corpus numbered it — the same sorted-shard-set order every server spine
// uses — because a ShardRef's blob ID is only meaningful against its own shard's
// index.
//
// A Corpus is immutable after NewCorpus and safe for concurrent generation. It
// borrows the indices rather than copying them, so they (and, for a served
// corpus, the mmap backing their content) must outlive it.
type Corpus struct {
	textOccurrences *textOccurrenceIndex
	syms            *symbol.Corpus
	idxs            []*index.Index
	// postings[i] records whether shard i's index can answer a trigram query at
	// all. A content-only index (index.Restore with nil postings — what
	// server.OpenSymbols builds, since symbol extraction never queries trigrams)
	// returns an empty posting list for every gram, which would look exactly like
	// "this name occurs nowhere" and silently lose the whole trigram arm. Probing
	// once at construction turns that silent recall loss into a content scan:
	// slower, identical answers. See hasPostings.
	postings []bool
}

// PreparedName is the reusable source/definition work set for one symbol name.
// Preparing once and generating bounded source batches avoids rebuilding the
// corpus-wide occurrence list for every batch of a high-frequency name.
type PreparedName struct {
	corpus           *Corpus
	name             string
	defs             []symbol.ShardRef
	srcs             []occurrence
	definitionCounts map[Site]int
}

// PrepareName resolves name's definitions and source occurrences once. It
// returns nil when name is empty, unknown, or has no candidate sources.
func PrepareName(c *Corpus, name string) *PreparedName {
	if c == nil || name == "" {
		return nil
	}
	defs := c.syms.Definitions(name)
	if len(defs) == 0 {
		return nil
	}
	srcs := c.occurrences(name)
	if len(srcs) == 0 {
		return nil
	}
	// The symbol arm is collected before the text arm, so occurrence collection
	// itself is not position ordered. Sort once here so independently generated
	// source batches concatenate to the exact whole-name order.
	sort.SliceStable(srcs, func(i, j int) bool {
		a, b := srcs[i], srcs[j]
		if a.site.Shard != b.site.Shard {
			return a.site.Shard < b.site.Shard
		}
		if a.site.Blob != b.site.Blob {
			return a.site.Blob < b.site.Blob
		}
		if a.site.Start != b.site.Start {
			return a.site.Start < b.site.Start
		}
		if a.site.End != b.site.End {
			return a.site.End < b.site.End
		}
		return a.typ < b.typ
	})
	counts := make(map[Site]int, len(defs))
	for _, d := range defs {
		counts[Site{Shard: d.Shard, Blob: d.Blob, Start: d.Start}]++
	}
	return &PreparedName{corpus: c, name: name, defs: defs, srcs: srcs, definitionCounts: counts}
}

// NumSources reports how many distinct source positions were found.
func (p *PreparedName) NumSources() int {
	if p == nil {
		return 0
	}
	return len(p.srcs)
}

// NumDefinitions reports how many definition targets each source may fan out to.
func (p *PreparedName) NumDefinitions() int {
	if p == nil {
		return 0
	}
	return len(p.defs)
}

// CrossShard reports whether any generated candidate can span two shards.
func (p *PreparedName) CrossShard() bool {
	if p == nil {
		return false
	}
	defShards := make(map[int]struct{}, len(p.defs))
	for _, d := range p.defs {
		defShards[d.Shard] = struct{}{}
	}
	for _, src := range p.srcs {
		if len(defShards) > 1 {
			return true
		}
		if _, same := defShards[src.site.Shard]; !same {
			return true
		}
	}
	return false
}

// SourceCandidates returns one evidence prototype per source, without target
// expansion. Target is deliberately unset: source-only verification may inspect
// these prototypes, but they must not be persisted as graph edges.
func (p *PreparedName) SourceCandidates() []Edge {
	if p == nil {
		return nil
	}
	out := make([]Edge, len(p.srcs))
	for i, src := range p.srcs {
		blob := p.corpus.Blob(src.site)
		var evidence graph.Evidence
		if blob != nil && src.site.Start >= 0 && src.site.End > src.site.Start {
			evidence = graph.Evidence{BlobSHA: blob.SHA, ByteOffset: uint64(src.site.Start), ByteLength: uint64(src.site.End - src.site.Start)}
		}
		out[i] = Edge{Name: p.name, Source: src.site, Type: src.typ, Confidence: graph.Candidate, Evidence: evidence, sourceBlob: blob}
	}
	return out
}

// DefinitionSite returns a target in the same position order used by Generate.
func (p *PreparedName) DefinitionSite(i int) Site {
	d := p.defs[i]
	return Site{Shard: d.Shard, Blob: d.Blob, Start: d.Start, End: d.End}
}

// NumTargets excludes every self-position pair, exactly as Generate does.
func (p *PreparedName) NumTargets(source Site) int {
	if p == nil {
		return 0
	}
	source.End = 0
	return len(p.defs) - p.definitionCounts[source]
}

// Generate returns candidates for the half-open source range [start,end). The
// range is clamped, making it safe for a scheduler to form the final short
// batch without special casing. Results retain the same deterministic order as
// GenerateCandidates.
func (p *PreparedName) Generate(start, end int) []Edge {
	if p == nil || start >= len(p.srcs) || end <= 0 || start >= end {
		return nil
	}
	if start < 0 {
		start = 0
	}
	if end > len(p.srcs) {
		end = len(p.srcs)
	}

	edges := make([]Edge, 0, (end-start)*len(p.defs))
	for _, src := range p.srcs[start:end] {
		sourceBlob := p.corpus.Blob(src.site)
		var evidence graph.Evidence
		if sourceBlob != nil && src.site.Start >= 0 && src.site.End > src.site.Start {
			evidence = graph.Evidence{
				BlobSHA:    sourceBlob.SHA,
				ByteOffset: uint64(src.site.Start),
				ByteLength: uint64(src.site.End - src.site.Start),
			}
		}
		for _, d := range p.defs {
			target := Site{Shard: d.Shard, Blob: d.Blob, Start: d.Start, End: d.End}
			if samePosition(src.site, target) {
				continue
			}
			edges = append(edges, Edge{
				Name:       p.name,
				Source:     src.site,
				Target:     target,
				Type:       src.typ,
				Confidence: graph.Candidate,
				Evidence:   evidence,
				sourceBlob: sourceBlob,
			})
		}
	}
	if len(edges) == 0 {
		return nil
	}
	sortEdges(edges)
	return edges
}

// NewCorpus pairs the merged cross-shard symbol index with the per-shard content
// indices it was merged from. idxs must have one entry per merged shard, in shard
// ID order; a nil entry is allowed (that shard contributes no trigram
// occurrences, only whatever the symbol arm knows) so a caller whose shard set
// includes an unloadable shard can still generate over the rest.
func NewCorpus(syms *symbol.Corpus, idxs ...*index.Index) (*Corpus, error) {
	if syms == nil {
		return nil, fmt.Errorf("candidates: nil symbol corpus")
	}
	if len(idxs) != syms.NumShards() {
		return nil, fmt.Errorf("candidates: got %d content indices for %d merged shards", len(idxs), syms.NumShards())
	}
	c := &Corpus{syms: syms, idxs: idxs, postings: make([]bool, len(idxs))}
	for i, ix := range idxs {
		c.postings[i] = hasPostings(ix)
	}
	return c, nil
}

// NumShards reports how many shards candidate generation fans out over.
func (c *Corpus) NumShards() int { return len(c.idxs) }

// ShardName returns the opaque identity of shard (its shard file basename,
// typically), or "" for an out-of-range ID.
func (c *Corpus) ShardName(shard int) string { return c.syms.ShardName(shard) }

// Scanning reports whether shard answers the trigram arm by scanning its blob
// content instead of by reading posting lists — because it carries no postings
// (see Corpus.postings). The answers are identical either way, the cost is not:
// this is the difference between a posting-list lookup and reading every byte of
// the shard, per name. It is exported so an offline sweep can say so out loud
// rather than just running slowly. A shard added with a nil index also reports
// true, having no content to query either way; an out-of-range shard reports
// false.
func (c *Corpus) Scanning(shard int) bool {
	if shard < 0 || shard >= len(c.postings) {
		return false
	}
	return !c.postings[shard]
}

// Symbols exposes the merged cross-shard symbol index, so a caller can widen a
// candidate into its enclosing definition (symbol.Corpus.Enclosing) or ask which
// shards define a name without generating anything.
func (c *Corpus) Symbols() *symbol.Corpus { return c.syms }

// Blob resolves a Site to the blob holding it, or nil when the shard or blob is
// unknown. The blob's content is the shard's own (for a served corpus, a
// zero-copy view of the shared content mmap) — do not mutate it.
func (c *Corpus) Blob(s Site) *index.Blob {
	if s.Shard < 0 || s.Shard >= len(c.idxs) || c.idxs[s.Shard] == nil {
		return nil
	}
	return c.idxs[s.Shard].Blob(s.Blob)
}

// Text returns the bytes a Site names — the name token itself — or nil when the
// site cannot be resolved or its range falls outside the blob. It is the
// cheapest way for a verifier (or a test) to confirm an offset points where it
// claims to.
func (c *Corpus) Text(s Site) []byte {
	b := c.Blob(s)
	if b == nil || s.Start < 0 || s.End < s.Start || s.End > len(b.Content) {
		return nil
	}
	return b.Content[s.Start:s.End]
}

// GenerateCandidates returns every candidate edge for name: each corpus-wide
// occurrence of the name, paired with each definition of it, in deterministic
// (source, target, type) order.
//
// It returns nil when the corpus knows no DEFINITION of name — with no target
// there is no edge to draw, only an unresolved reference — and nil for the empty
// name. Every returned edge satisfies:
//
//   - Source and Target are distinct positions. A definition is not a use of
//     itself, so the definition's own occurrence never targets itself; it does
//     still pair with the OTHER definitions of the name (SiblingDefinition).
//   - Target is always a definition occurrence. Reference-to-reference pairs are
//     not edges, they are two uses of the same unknown thing.
//   - Both ends' Start/End bracket the name token, so Text(site) is the name.
//
// Pairing every source with every definition is the deliberate over-generation:
// name-based lookup cannot say which same-named definition a use resolves to, so
// the candidate set contains all of them and phase 4 records that uncertainty as
// Candidate confidence. A name with D definitions and S occurrences therefore
// yields up to D*S edges.
func GenerateCandidates(c *Corpus, name string) []Edge {
	p := PrepareName(c, name)
	return p.Generate(0, p.NumSources())
}

// samePosition reports whether two sites name the same position, comparing
// Shard/Blob/Start only: End is the discovering arm's view of where the token
// stops, and a disagreement there does not make two positions.
func samePosition(a, b Site) bool {
	return a.Shard == b.Shard && a.Blob == b.Blob && a.Start == b.Start
}

// sortEdges imposes a deterministic order — source position, then target
// position, then evidence type — so a candidate list is reproducible across runs
// despite the map iteration inside the symbol merge and the occurrence dedup.
func sortEdges(edges []Edge) {
	sort.SliceStable(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.Source.Shard != b.Source.Shard {
			return a.Source.Shard < b.Source.Shard
		}
		if a.Source.Blob != b.Source.Blob {
			return a.Source.Blob < b.Source.Blob
		}
		if a.Source.Start != b.Source.Start {
			return a.Source.Start < b.Source.Start
		}
		if a.Target.Shard != b.Target.Shard {
			return a.Target.Shard < b.Target.Shard
		}
		if a.Target.Blob != b.Target.Blob {
			return a.Target.Blob < b.Target.Blob
		}
		if a.Target.Start != b.Target.Start {
			return a.Target.Start < b.Target.Start
		}
		return a.Type < b.Type
	})
}
