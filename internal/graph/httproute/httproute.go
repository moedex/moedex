// Package httproute discovers cross-service HTTP edges: it matches the URLs an
// outbound client call asks for against the route templates handlers declare,
// across every repo in the corpus, and emits HTTP_CALLS edges (phase 10 of
// docs/GRAPH-LAYER-PLAN.md).
//
// This is the edge type no intra-repo analysis can find. A call to
// "api/orders/{id}" and the [HttpGet("api/orders/{id}")] that serves it are, by
// construction, in DIFFERENT repositories with no symbol, type, or package in
// common — the only thing they share is a string that describes a path. Name
// resolution has nothing to resolve, which is why phases 3 and 4 cannot reach
// these edges and why the URL itself has to become the join key.
//
// # Two sides, found the same way
//
// Both halves are recognized by the phase-2 discipline: a literal framework
// marker ("[HttpGet", "HandleFunc", ".get(", "GetAsync") turns into a sound
// trigram candidate-blob query, and a language-specific regexp runs only over
// those blobs as the precision gate. Handlers come from ASP.NET routing
// attributes and minimal-API maps, Go's net/http and the common third-party
// routers, Express-style registrations, and Flask/FastAPI decorators. Calls come
// from HttpClient, net/http, fetch/axios, and requests/httpx. See markers and
// the recognizers in extract.go.
//
// # Matching, and what the confidence means
//
// A client URL and a route template are both parsed into segment sequences in
// which every placeholder spelling — "{id}", ":id", "<int:id>", "${id}", "%d" —
// collapses to the same thing, because the whole point is to compare a C#
// template against an Express one. Two templates then match if they align
// segment for segment. Alignment quality is distinct from binding confidence:
//
//   - Exact -> at most Pattern. The templates are structurally identical:
//     equal arity, literals matching literals and placeholders matching
//     placeholders, position for position. Service identity remains unknown.
//   - Parameterized -> Pattern (0.6). They align only because a placeholder
//     absorbed something spelled differently — a concrete "42" binding to
//     "{id}", a catch-all swallowing a tail, an unresolved base address dropped
//     from the front of the call. Real evidence, but inferred.
//
// Alignment is bounded by two soundness rules. A match must include at least
// one literal segment agreeing with a literal segment, so an all-placeholder
// URL cannot match the entire corpus; and a call's verb must be compatible with
// the handler's, which rejects only a PROVEN mismatch (a GET can never reach a
// POST-only handler) and treats every unread verb as compatible.
//
// # What this does not do
//
// Matching is by path shape, not by service identity: a call to "/api/orders/5"
// matches every handler in the corpus that serves that shape, and if three
// services expose it, all three are edges. Resolving WHICH one a configured base
// address points at needs deployment config this package deliberately does not
// read. Competing destinations and shared content contexts are diagnostic
// Candidate edges; a unique route-only match is Pattern, never runtime proof.
//
// Known gaps, stated rather than hidden: router group prefixes (gin's
// router.Group, chi's Route) are not folded into their children, so those
// handlers are recognized at their declared fragment; Django's path() and
// ColdFusion are not recognized at all.
package httproute

import (
	"regexp"
	"sort"

	"moedex/internal/graph"
	graphverify "moedex/internal/graph/verify"
	"moedex/internal/index"
	"moedex/internal/query"
	"moedex/internal/symbol"
)

// Tier maps a match quality onto the graph layer's shared confidence ladder.
func (q Quality) Tier() graphverify.Tier {
	switch q {
	case Exact, Parameterized:
		return graphverify.Pattern
	default:
		return graphverify.Candidate
	}
}

// Edge is one HTTP_CALLS relationship: an outbound call site that can reach a
// route handler. The direction is CALLER -> HANDLER, matching the use ->
// definition direction phase 3 uses, so following it walks toward what a service
// depends on and inverting it walks toward blast radius.
type Edge struct {
	Call       Endpoint
	Handler    Endpoint
	Quality    Quality
	Confidence graph.ConfidenceTier
}

// CrossShard reports whether the two ends live in different shards.
func (e Edge) CrossShard() bool { return e.Call.Shard != e.Handler.Shard }

// CrossRepo reports whether the two ends live in different repositories — the
// cross-service edges that are the point of the pass.
func (e Edge) CrossRepo() bool {
	return e.Call.Repo != e.Handler.Repo || e.CrossShard()
}

// Shard is one indexed shard participating in the sweep: its content/trigram
// index and the symbol index built over it. Symbols may be nil, in which case
// endpoints carry no enclosing definition.
type Shard struct {
	Name    string
	Index   *index.Index
	Symbols *symbol.Index
}

// Corpus is the read-only multi-shard view the sweep runs over. Shard IDs are
// positions in the slice, matching the convention the rest of the graph layer
// uses. It borrows the indices rather than copying them, so they (and any mmap
// backing their content) must outlive it.
type Corpus struct {
	shards []Shard
}

// NewCorpus returns a corpus over shards, in shard-ID order.
func NewCorpus(shards ...Shard) *Corpus { return &Corpus{shards: shards} }

// NumShards reports how many shards the sweep fans out over.
func (c *Corpus) NumShards() int { return len(c.shards) }

// ShardName returns the opaque identity of a shard, or "" when out of range.
func (c *Corpus) ShardName(shard int) string {
	if shard < 0 || shard >= len(c.shards) {
		return ""
	}
	return c.shards[shard].Name
}

// Blob resolves an endpoint to the blob holding its evidence, or nil when the
// shard or blob is unknown. The content is the shard's own — do not mutate it.
func (c *Corpus) Blob(e Endpoint) *index.Blob {
	if e.Shard < 0 || e.Shard >= len(c.shards) || c.shards[e.Shard].Index == nil {
		return nil
	}
	return c.shards[e.Shard].Index.Blob(e.Blob)
}

// Report accounts for the sweep. CandidateBlobs is the trigram fan-out BEFORE
// regexp confirmation and ScannedBlobs how many of those were in a recognized
// language, so the filter's selectivity is reported rather than assumed — and a
// small edge count is never mistaken for a small corpus.
type Report struct {
	Shards         int
	CandidateBlobs int
	ScannedBlobs   int
	Handlers       int
	Calls          int
	Edges          int
	Exact          int
	Parameterized  int
	CrossRepo      int
	MatchedCalls   int

	// Comparisons is how many (call, handler) pairs the matcher actually
	// examined. Against the Calls x Handlers product it is the bucket index's
	// measured selectivity — reported rather than assumed, since a corpus whose
	// routes all share a literal degenerates toward the full product.
	Comparisons int
}

// UnmatchedCalls reports how many client calls found no handler anywhere in the
// corpus — usually a service whose server side is outside the indexed set.
func (r Report) UnmatchedCalls() int { return r.Calls - r.MatchedCalls }

// Result is a complete sweep: what was found on both sides, the edges between
// them, and the accounting.
type Result struct {
	Endpoints Endpoints
	Edges     []Edge
	Report    Report
}

// Build runs the whole pass: extract both sides of every shard, then match them
// across the corpus.
func Build(c *Corpus) Result {
	endpoints, report := Extract(c)
	edges, comparisons := matchEndpoints(endpoints)

	report.Edges = len(edges)
	report.Comparisons = comparisons
	matched := map[siteKey]struct{}{}
	for _, e := range edges {
		switch e.Quality {
		case Exact:
			report.Exact++
		case Parameterized:
			report.Parameterized++
		}
		if e.CrossRepo() {
			report.CrossRepo++
		}
		matched[siteOf(e.Call)] = struct{}{}
	}
	report.MatchedCalls = len(matched)
	return Result{Endpoints: endpoints, Edges: edges, Report: report}
}

// Extract finds every route declaration and outbound call in the corpus.
func Extract(c *Corpus) (Endpoints, Report) {
	var (
		out    Endpoints
		report Report
	)
	if c == nil {
		return out, report
	}
	report.Shards = len(c.shards)
	for id, sh := range c.shards {
		if sh.Index == nil {
			continue
		}
		blobs := markerCandidates(sh.Index)
		report.CandidateBlobs += len(blobs)
		for _, blob := range blobs {
			if extractBlob(id, sh.Index, sh.Symbols, blob, &out) {
				report.ScannedBlobs++
			}
		}
	}
	report.Handlers = len(out.Handlers)
	report.Calls = len(out.Calls)
	markSharedContexts(c, &out)
	sortEndpoints(out.Handlers)
	sortEndpoints(out.Calls)
	return out, report
}

// Content-addressed graph nodes merge identical bytes. A route or call whose
// blob occurs in several repository/path contexts cannot have one unambiguous
// service binding, even when extraction chose only the first file location.
func markSharedContexts(c *Corpus, endpoints *Endpoints) {
	type location struct{ repo, path string }
	contexts := map[string]map[location]bool{}
	for _, side := range [][]Endpoint{endpoints.Calls, endpoints.Handlers} {
		for _, endpoint := range side {
			if blob := c.Blob(endpoint); blob != nil {
				contexts[blob.SHA] = nil
			}
		}
	}
	if len(contexts) == 0 {
		return
	}
	for _, shard := range c.shards {
		if shard.Index == nil {
			continue
		}
		for id := uint64(0); id < uint64(shard.Index.NumBlobs()); id++ {
			blob := shard.Index.Blob(id)
			if blob == nil {
				continue
			}
			if _, relevant := contexts[blob.SHA]; !relevant {
				continue
			}
			if contexts[blob.SHA] == nil {
				contexts[blob.SHA] = map[location]bool{}
			}
			for _, file := range blob.Files {
				contexts[blob.SHA][location{file.Repo, file.RelPath}] = true
			}
		}
	}
	for _, side := range [][]Endpoint{endpoints.Calls, endpoints.Handlers} {
		for i := range side {
			blob := c.Blob(side[i])
			if blob == nil || blob.SHA == "" || len(contexts[blob.SHA]) != 1 || side[i].Repo == "" || side[i].Path == "" {
				side[i].AmbiguousContext = true
			}
		}
	}
}

// MatchEndpoints pairs every call with every handler it could reach, in
// deterministic (call site, handler site) order.
//
// The comparison is not the naive cross product. A match requires at least one
// literal segment to agree with a literal segment (see minLiteralAgreement), so
// a handler that shares NO literal segment text with a call provably cannot
// match it. Bucketing handlers by their literal segment texts and probing only
// the buckets a call's own literals name is therefore lossless — the same
// necessary-condition discipline the trigram index applies to regex search —
// and it keeps a corpus-wide sweep off an O(calls x handlers) product.
func MatchEndpoints(e Endpoints) []Edge {
	edges, _ := matchEndpoints(e)
	return edges
}

// matchEndpoints also returns how many (call, handler) pairs were actually
// compared, which Report surfaces: the bucket index is sound but not
// necessarily selective — a corpus where every route contains "api" degenerates
// toward the full product, and that should be visible in the build output
// rather than merely felt as a slow build.
func matchEndpoints(e Endpoints) ([]Edge, int) {
	if len(e.Calls) == 0 || len(e.Handlers) == 0 {
		return nil, 0
	}

	byLiteral := make(map[string][]int, len(e.Handlers))
	for i, h := range e.Handlers {
		for _, text := range literalTexts(h.Template) {
			byLiteral[text] = append(byLiteral[text], i)
		}
	}

	best := map[edgeKey]Edge{}
	comparisons := 0
	var probe []int
	for _, call := range e.Calls {
		probe = probe[:0]
		seen := map[int]bool{}
		for _, text := range literalTexts(call.Template) {
			for _, i := range byLiteral[text] {
				if !seen[i] {
					seen[i] = true
					probe = append(probe, i)
				}
			}
		}
		for _, i := range probe {
			comparisons++
			handler := e.Handlers[i]
			if !call.Method.Compatible(handler.Method) {
				continue
			}
			quality := MatchTemplates(call.Template, handler.Template)
			if quality == NoMatch {
				continue
			}
			key := edgeKey{call: siteOf(call), callMethod: call.Method, handler: siteOf(handler)}
			// One declaration can be recognized more than once (a controller
			// action carrying both [HttpGet] and [Route], a Flask route with
			// several verbs). Keep the strongest grade for the pair.
			if prev, ok := best[key]; ok {
				if prev.Quality > quality || (prev.Quality == quality && !lessEndpoint(handler, prev.Handler)) {
					continue
				}
			}
			best[key] = Edge{
				Call:       call,
				Handler:    handler,
				Quality:    quality,
				Confidence: quality.Tier(),
			}
		}
	}

	// Count distinct compatible destinations per call site and verb. Several
	// route declarations for one handler are not competing services; different
	// handlers (including those in one repository) are unresolved alternatives.
	type requestKey struct {
		site   siteKey
		method Method
	}
	type destinationKey struct {
		repo, path string
		offset     int
		fallback   siteKey
	}
	destinations := map[requestKey]map[destinationKey]bool{}
	for _, edge := range best {
		request := requestKey{siteOf(edge.Call), edge.Call.Method}
		handler := edge.Handler
		destination := destinationKey{repo: handler.Repo, path: handler.Path, offset: handler.SymbolStart}
		if destination.offset < 0 {
			destination.offset = handler.Start
		}
		if handler.Repo == "" || handler.Path == "" {
			destination.fallback = siteOf(handler)
		}
		if destinations[request] == nil {
			destinations[request] = map[destinationKey]bool{}
		}
		destinations[request][destination] = true
	}
	edges := make([]Edge, 0, len(best))
	for _, edge := range best {
		if len(destinations[requestKey{siteOf(edge.Call), edge.Call.Method}]) > 1 || edge.Call.AmbiguousContext || edge.Handler.AmbiguousContext {
			edge.Confidence = graph.Candidate
		}
		edges = append(edges, edge)
	}
	sortEdges(edges)
	return edges, comparisons
}

// literalTexts returns the distinct literal segment texts of a template — the
// join keys the handler bucket index is built on.
func literalTexts(t Template) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range t.Segments {
		if s.Kind != SegLiteral || s.Text == "" || seen[s.Text] {
			continue
		}
		seen[s.Text] = true
		out = append(out, s.Text)
	}
	return out
}

type siteKey struct {
	shard int
	blob  uint64
	start int
}

type edgeKey struct {
	call, handler siteKey
	callMethod    Method
}

func siteOf(e Endpoint) siteKey {
	return siteKey{shard: e.Shard, blob: e.Blob, start: e.Start}
}

func sortEndpoints(eps []Endpoint) {
	sort.SliceStable(eps, func(i, j int) bool { return lessEndpoint(eps[i], eps[j]) })
}

func lessEndpoint(a, b Endpoint) bool {
	if as, bs := siteOf(a), siteOf(b); as != bs {
		return lessSite(as, bs)
	}
	if a.Method != b.Method {
		return a.Method < b.Method
	}
	if a.Raw != b.Raw {
		return a.Raw < b.Raw
	}
	return a.Framework < b.Framework
}

func sortEdges(edges []Edge) {
	sort.SliceStable(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if lessEndpoint(a.Call, b.Call) || lessEndpoint(b.Call, a.Call) {
			return lessEndpoint(a.Call, b.Call)
		}
		return lessEndpoint(a.Handler, b.Handler)
	})
}

func lessSite(a, b siteKey) bool {
	if a.shard != b.shard {
		return a.shard < b.shard
	}
	if a.blob != b.blob {
		return a.blob < b.blob
	}
	return a.start < b.start
}

// markerCandidates unions the sound trigram-query candidates for every
// framework marker. query.FromRegexp may deliberately return ALL for a short or
// selectively-unindexed marker; the language regexps still carry precision in
// that case.
func markerCandidates(ix *index.Index) []uint64 {
	if ix == nil {
		return nil
	}
	seen := map[uint64]bool{}
	for _, marker := range markers {
		q, err := query.FromRegexp(regexp.QuoteMeta(marker))
		if err != nil {
			continue
		}
		for _, id := range q.Eval(ix) {
			seen[id] = true
		}
	}
	out := make([]uint64, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
