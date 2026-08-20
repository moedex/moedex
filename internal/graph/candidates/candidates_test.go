package candidates

import (
	"fmt"
	"reflect"
	"sort"
	"testing"

	"moedex/internal/graph"
	"moedex/internal/index"
	"moedex/internal/symbol"
)

// The fixtures are one shard per repo, which is the shape that makes cross-shard
// generation testable at all: a shard-local index cannot answer any of the
// questions below, and every shard's only blob is blob 0, so a candidate that
// lost its shard qualification would fold three different repos into one site.
//
// ProcessOrder is the known symbol the suite tracks. It is DEFINED once (orders),
// called from Go (billing, where an extractor classifies the call), called from a
// language with no extractor (shipping, where only the trigram arm can see it),
// and near-missed by longer identifiers (nearby, which must contribute nothing).
const (
	ordersSrc = `package orders

// ProcessOrder is the single definition the candidates must target.
func ProcessOrder(id string) error { return nil }
`
	billingSrc = `package billing

func Charge(id string) error { return ProcessOrder(id) }
`
	// Python has no extractor (symbol.ExtractorForPath returns nil), so the symbol
	// layer records nothing here at all — this reference exists only as bytes.
	shippingSrc = `def dispatch(order_id):
    # hand off to the orders service
    return ProcessOrder(order_id)
`
	// Every occurrence of the name here is embedded in a longer identifier, so a
	// correct pass finds no source in this shard whatsoever.
	nearbySrc = `package nearby

// ProcessOrderRequest and preProcessOrder are different names entirely.
type ProcessOrderRequest struct{}

func preProcessOrder() {}
`
	// A vendored second definition of the same name, for the collision case.
	vendorSrc = `package vendor

func ProcessOrder(id string) error { return nil }
`
)

// fixture is one shard: a single file's content, indexed under its own repo.
type fixture struct {
	repo string
	path string
	src  string
}

var (
	orders   = fixture{"repo-orders", "orders.go", ordersSrc}
	billing  = fixture{"repo-billing", "billing.go", billingSrc}
	shipping = fixture{"repo-shipping", "dispatch.py", shippingSrc}
	nearby   = fixture{"repo-nearby", "nearby.go", nearbySrc}
	vendor   = fixture{"repo-vendor", "vendored.go", vendorSrc}
)

// build assembles a Corpus over one shard per fixture, in fixture order, so shard
// IDs match the fixtures' positions.
func build(t *testing.T, fs ...fixture) *Corpus {
	t.Helper()
	return buildWith(t, true, fs...)
}

// buildWith is build with control over whether the shards' content indices carry
// trigram postings. postings=false restores each index from its own snapshot with
// a nil posting map — the shape server.OpenSymbols produces, since symbol
// extraction never queries trigrams — which is the case the scan fallback exists
// for.
func buildWith(t *testing.T, postings bool, fs ...fixture) *Corpus {
	t.Helper()
	shards := make([]symbol.Shard, 0, len(fs))
	idxs := make([]*index.Index, 0, len(fs))
	for i, f := range fs {
		ix := index.New()
		ix.AddFile(f.repo, f.path, "/"+f.repo+"/"+f.path, fmt.Sprintf("sha-%02d", i), []byte(f.src))
		if !postings {
			ix = index.Restore(ix.Snapshot(), nil)
		}
		shards = append(shards, symbol.Shard{Name: fmt.Sprintf("%03d.idx", i), Index: symbol.BuildMulti(ix)})
		idxs = append(idxs, ix)
	}
	c, err := NewCorpus(symbol.Merge(shards...), idxs...)
	if err != nil {
		t.Fatalf("NewCorpus: %v", err)
	}
	return c
}

// sourceShards lists the ascending set of shard IDs the edges' SOURCES come from.
func sourceShards(edges []Edge) []int {
	seen := map[int]bool{}
	var out []int
	for _, e := range edges {
		if !seen[e.Source.Shard] {
			seen[e.Source.Shard] = true
			out = append(out, e.Source.Shard)
		}
	}
	sort.Ints(out)
	return out
}

// fromShard returns the edges whose source is in shard.
func fromShard(edges []Edge, shard int) []Edge {
	var out []Edge
	for _, e := range edges {
		if e.Source.Shard == shard {
			out = append(out, e)
		}
	}
	return out
}

func TestPreparedNameBatchesEqualWholeGeneration(t *testing.T) {
	c := build(t, orders, billing, shipping, vendor)
	want := GenerateCandidates(c, "ProcessOrder")
	p := PrepareName(c, "ProcessOrder")
	if p == nil {
		t.Fatal("PrepareName returned nil")
	}
	if p.NumDefinitions() != 2 {
		t.Fatalf("NumDefinitions = %d, want 2", p.NumDefinitions())
	}
	if !p.CrossShard() {
		t.Fatal("CrossShard = false, want true")
	}

	var got []Edge
	for start := 0; start < p.NumSources(); start++ {
		got = append(got, p.Generate(start, start+1)...)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("batched generation differs from whole generation:\n got %+v\nwant %+v", got, want)
	}
}

func TestPreparedNameRangeClampingAndNil(t *testing.T) {
	if got := PrepareName(nil, "ProcessOrder"); got != nil {
		t.Fatalf("PrepareName(nil) = %#v, want nil", got)
	}
	c := build(t, orders, billing)
	p := PrepareName(c, "ProcessOrder")
	if p == nil {
		t.Fatal("PrepareName returned nil")
	}
	if got, want := p.Generate(-10, p.NumSources()+10), GenerateCandidates(c, "ProcessOrder"); !reflect.DeepEqual(got, want) {
		t.Fatalf("clamped generation differs:\n got %+v\nwant %+v", got, want)
	}
	if got := p.Generate(p.NumSources(), p.NumSources()+1); got != nil {
		t.Fatalf("out-of-range Generate = %+v, want nil", got)
	}
}

// TestGenerateCandidates_FindsCrossShardReferences is the phase-3 proof: a symbol
// defined in one shard and referenced in two others yields candidate edges from
// every referencing shard to the definition — including from a shard whose
// language has no symbol extractor, which the byName index alone cannot see.
func TestGenerateCandidates_FindsCrossShardReferences(t *testing.T) {
	c := build(t, orders, billing, shipping)

	edges := GenerateCandidates(c, "ProcessOrder")
	if len(edges) == 0 {
		t.Fatal(`GenerateCandidates("ProcessOrder") = nil, want candidates from every referencing shard`)
	}

	// Every edge must be well-formed: named, targeting the one definition, with
	// both offsets landing exactly on the name token.
	def := c.Symbols().Definitions("ProcessOrder")
	if len(def) != 1 {
		t.Fatalf("fixture: %d definitions of ProcessOrder, want 1", len(def))
	}
	wantTarget := Site{Shard: def[0].Shard, Blob: def[0].Blob, Start: def[0].Start, End: def[0].End}
	for _, e := range edges {
		if e.Name != "ProcessOrder" {
			t.Errorf("edge %+v: Name = %q, want ProcessOrder", e, e.Name)
		}
		if e.Target != wantTarget {
			t.Errorf("edge %+v: Target = %+v, want the definition at %+v", e, e.Target, wantTarget)
		}
		if got := string(c.Text(e.Source)); got != "ProcessOrder" {
			t.Errorf("edge %+v: source bytes = %q, want the name token", e, got)
		}
		if got := string(c.Text(e.Target)); got != "ProcessOrder" {
			t.Errorf("edge %+v: target bytes = %q, want the name token", e, got)
		}
		if samePosition(e.Source, e.Target) {
			t.Errorf("edge %+v: definition points at itself", e)
		}
		if e.Type == TypeUnknown {
			t.Errorf("edge %+v: untyped candidate", e)
		}
		blob := c.Blob(e.Source)
		if blob == nil {
			t.Fatalf("edge %+v: source blob is missing", e)
		}
		if e.Confidence != graph.Candidate {
			t.Errorf("edge %+v: confidence = %s, want Candidate", e, e.Confidence)
		}
		if e.Evidence.BlobSHA != blob.SHA || e.Evidence.ByteOffset != uint64(e.Source.Start) || e.Evidence.ByteLength != uint64(e.Source.End-e.Source.Start) {
			t.Errorf("edge %+v: evidence = %+v, want source blob/span", e, e.Evidence)
		}
		if got, ok := e.Evidence.Bytes(blob.Content); !ok || string(got) != e.Name {
			t.Errorf("edge %+v: evidence bytes = %q, %v, want %q", e, got, ok, e.Name)
		}
	}

	// The Go call site (shard 1) and the Python one (shard 2) must BOTH be found,
	// and each must cross a shard boundary — the cross-repo edges no per-shard
	// index can produce.
	byShard := map[int][]Edge{1: fromShard(edges, 1), 2: fromShard(edges, 2)}
	for shard, want := range map[int]Type{1: SymbolReference, 2: TextOccurrence} {
		got := byShard[shard]
		if len(got) != 1 {
			t.Fatalf("shard %d (%s) contributed %d candidates, want 1: %+v", shard, c.ShardName(shard), len(got), got)
		}
		if got[0].Type != want {
			t.Errorf("shard %d candidate typed %v, want %v: %+v", shard, got[0].Type, want, got[0])
		}
		if !got[0].CrossShard() {
			t.Errorf("shard %d candidate is not cross-shard: %+v", shard, got[0])
		}
	}

	// Shard 1's classified reference proves the symbol arm; shard 2's proves the
	// trigram arm adds recall the symbol arm structurally cannot, since Python has
	// no extractor and contributes no names at all.
	if refs, want := c.Symbols().ReferencingShards("ProcessOrder"), []int{1}; !reflect.DeepEqual(refs, want) {
		t.Fatalf("fixture no longer isolates the arms: byName reports referencing shards %v, want only %v (Python has no extractor)", refs, want)
	}

	// The doc comment in shard 0 mentions the name, so the definition's own shard
	// contributes an unclassified source too. That is deliberate over-generation,
	// not a miss: phase 4 leaves it at Candidate confidence.
	if own := fromShard(edges, 0); len(own) != 1 || own[0].Type != TextOccurrence {
		t.Errorf("shard 0 sources = %+v, want the doc-comment mention as one TextOccurrence", own)
	}
}

// TestGenerateCandidates_IdentifierBoundary locks the one filter the pass applies:
// an occurrence embedded in a longer identifier is provably not a use of the name,
// and a shard containing only those contributes nothing.
func TestGenerateCandidates_IdentifierBoundary(t *testing.T) {
	c := build(t, orders, nearby)

	edges := GenerateCandidates(c, "ProcessOrder")
	if len(edges) == 0 {
		t.Fatal(`GenerateCandidates("ProcessOrder") = nil, want the definition shard's own mention`)
	}
	if got := fromShard(edges, 1); len(got) != 0 {
		t.Errorf("ProcessOrderRequest / preProcessOrder produced %d candidates, want 0: %+v", len(got), got)
	}
	if got := sourceShards(edges); !reflect.DeepEqual(got, []int{0}) {
		t.Errorf("source shards = %v, want only [0]", got)
	}

	// The reverse direction: the longer names are real symbols and generate their
	// own candidates, so the filter is rejecting fragments, not whole tokens.
	if got := GenerateCandidates(c, "ProcessOrderRequest"); len(got) == 0 {
		t.Error(`GenerateCandidates("ProcessOrderRequest") = nil, want its own candidates`)
	}
}

// TestGenerateCandidates_EveryDefinitionIsACandidate is the recall-completeness
// property under name collision: with the name defined in two repos, a call site
// cannot be resolved to one of them without type information, so it must yield a
// candidate to BOTH — and the two definitions must pair with each other as the
// override/implementation candidate that phase 4 can confirm or retain as
// uncertain.
func TestGenerateCandidates_EveryDefinitionIsACandidate(t *testing.T) {
	c := build(t, orders, billing, vendor)

	edges := GenerateCandidates(c, "ProcessOrder")
	call := fromShard(edges, 1)
	if len(call) != 2 {
		t.Fatalf("the billing call site produced %d candidates, want one per definition: %+v", len(call), call)
	}
	gotTargets := []int{call[0].Target.Shard, call[1].Target.Shard}
	sort.Ints(gotTargets)
	if !reflect.DeepEqual(gotTargets, []int{0, 2}) {
		t.Errorf("call site targets shards %v, want both definitions [0 2]", gotTargets)
	}
	for _, e := range call {
		if e.Type != SymbolReference {
			t.Errorf("call-site candidate typed %v, want SymbolReference: %+v", e.Type, e)
		}
	}

	// The two definitions cross-pair, in both directions, and neither targets
	// itself.
	var sibling [][2]int
	for _, e := range edges {
		if e.Type == SiblingDefinition {
			sibling = append(sibling, [2]int{e.Source.Shard, e.Target.Shard})
		}
	}
	sort.Slice(sibling, func(i, j int) bool { return sibling[i][0] < sibling[j][0] })
	if want := [][2]int{{0, 2}, {2, 0}}; !reflect.DeepEqual(sibling, want) {
		t.Errorf("sibling-definition pairs = %v, want %v", sibling, want)
	}
}

// TestGenerateCandidates_NoDefinitionNoEdge covers the two nil cases: a name
// nothing defines has no target to point at, and an unknown name is not an error.
func TestGenerateCandidates_NoDefinitionNoEdge(t *testing.T) {
	c := build(t, orders, billing, shipping)

	// dispatch is a real Python function, but Python has no extractor, so no shard
	// DEFINES the name — there is nothing for a candidate to target.
	if got := GenerateCandidates(c, "dispatch"); got != nil {
		t.Errorf(`GenerateCandidates("dispatch") = %+v, want nil (no definition anywhere)`, got)
	}
	if got := GenerateCandidates(c, "NoSuchSymbolAnywhere"); got != nil {
		t.Errorf("GenerateCandidates(unknown) = %+v, want nil", got)
	}
	if got := GenerateCandidates(c, ""); got != nil {
		t.Errorf("GenerateCandidates(\"\") = %+v, want nil", got)
	}
	if got := GenerateCandidates(nil, "ProcessOrder"); got != nil {
		t.Errorf("GenerateCandidates(nil corpus) = %+v, want nil", got)
	}
}

// TestGenerateCandidates_ScanFallbackIsRecallComplete is the guard on the
// silent-failure mode: an index restored without postings answers every trigram
// query with an empty list, which is indistinguishable from "this name occurs
// nowhere" and would drop the entire trigram arm without an error. The scan
// fallback must produce the IDENTICAL candidate list — and because shard 2's
// Python reference is visible only to that arm, this equality is what proves the
// fallback actually engaged.
func TestGenerateCandidates_ScanFallbackIsRecallComplete(t *testing.T) {
	fs := []fixture{orders, billing, shipping, nearby, vendor}
	withPostings := build(t, fs...)
	noPostings := buildWith(t, false, fs...)

	for i := 0; i < len(fs); i++ {
		if !noPostings.Scanning(i) {
			t.Fatalf("shard %d was probed as having postings; the fixture no longer exercises the fallback", i)
		}
		if withPostings.Scanning(i) {
			t.Fatalf("shard %d was probed as having no postings; the positional path is not being exercised", i)
		}
	}

	want := GenerateCandidates(withPostings, "ProcessOrder")
	got := GenerateCandidates(noPostings, "ProcessOrder")
	if len(want) == 0 {
		t.Fatal("fixture produced no candidates on the positional path")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scan fallback candidates differ from the positional path:\n got %+v\nwant %+v", got, want)
	}
}

// TestGenerateCandidates_ShortName covers the sub-trigram case: a name shorter
// than a gram has nothing to look up, so it must route to the content scan rather
// than to an empty answer. The Python caller is the load-bearing half — the symbol
// arm cannot see it, so only the scan can, which is what distinguishes "falls back"
// from "gives up".
func TestGenerateCandidates_ShortName(t *testing.T) {
	c := build(t,
		fixture{"repo-tiny", "tiny.go", "package tiny\n\nfunc Do() {}\n"},
		fixture{"repo-user", "user.go", "package user\n\nfunc Run() { Do() }\n"},
		fixture{"repo-py", "run.py", "def run():\n    return Do()\n"},
	)

	got := map[int]Type{}
	for _, e := range GenerateCandidates(c, "Do") {
		if !e.CrossShard() || e.Target.Shard != 0 {
			continue
		}
		if _, dup := got[e.Source.Shard]; dup {
			t.Errorf("shard %d contributed more than one candidate for Do", e.Source.Shard)
		}
		got[e.Source.Shard] = e.Type
		if name := string(c.Text(e.Source)); name != "Do" {
			t.Errorf("edge %+v: source bytes = %q, want Do", e, name)
		}
	}
	want := map[int]Type{1: SymbolReference, 2: TextOccurrence}
	if !reflect.DeepEqual(got, want) {
		t.Errorf(`GenerateCandidates("Do") cross-shard sources = %v, want %v`, got, want)
	}
}

// TestGenerateCandidates_Deterministic locks reproducibility. Both the merged
// name index and the occurrence dedup iterate maps, so an unsorted result would
// vary between runs and could not be diffed, cached, or resumed.
func TestGenerateCandidates_Deterministic(t *testing.T) {
	c := build(t, orders, billing, shipping, nearby, vendor)

	first := GenerateCandidates(c, "ProcessOrder")
	if len(first) == 0 {
		t.Fatal("no candidates to compare")
	}
	for i := 0; i < 8; i++ {
		if got := GenerateCandidates(c, "ProcessOrder"); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs:\n got %+v\nwant %+v", i, got, first)
		}
	}
}

// TestNewCorpus_ShardAlignment locks the one invariant a caller can get wrong: a
// content index per merged shard, in shard ID order. A misaligned list would
// resolve blob IDs against the wrong shard and silently attribute candidates to
// the wrong repo, so it must be an error and not a best effort.
func TestNewCorpus_ShardAlignment(t *testing.T) {
	ix := index.New()
	ix.AddFile("repo-orders", "orders.go", "/repo-orders/orders.go", "sha", []byte(ordersSrc))
	syms := symbol.Merge(
		symbol.Shard{Name: "000.idx", Index: symbol.BuildMulti(ix)},
		symbol.Shard{Name: "001.idx", Index: symbol.NewIndex()},
	)

	if _, err := NewCorpus(syms, ix); err == nil {
		t.Error("NewCorpus with 1 index for 2 shards returned no error")
	}
	if _, err := NewCorpus(nil, ix); err == nil {
		t.Error("NewCorpus with a nil symbol corpus returned no error")
	}

	// A shard with no loadable content index is allowed: it contributes no trigram
	// occurrences but still resolves whatever the symbol arm knows.
	c, err := NewCorpus(syms, ix, nil)
	if err != nil {
		t.Fatalf("NewCorpus with a nil shard index: %v", err)
	}
	if got := c.NumShards(); got != 2 {
		t.Errorf("NumShards = %d, want 2", got)
	}
	if got := c.ShardName(1); got != "001.idx" {
		t.Errorf("ShardName(1) = %q, want 001.idx", got)
	}
	if got := c.Blob(Site{Shard: 1}); got != nil {
		t.Errorf("Blob on a nil shard index = %+v, want nil", got)
	}
	if got := GenerateCandidates(c, "ProcessOrder"); len(got) == 0 {
		t.Error("a nil shard index suppressed candidates from the shards that do load")
	}
}

// TestCorpus_TextBounds covers Site resolution's refusal to read outside a blob:
// a verifier hands these offsets to a regex engine, and a silently truncated or
// panicking read is worse than an empty one.
func TestCorpus_TextBounds(t *testing.T) {
	c := build(t, orders)
	blob := c.Blob(Site{Shard: 0, Blob: 0})
	if blob == nil {
		t.Fatal("Blob(shard 0, blob 0) = nil")
	}
	cases := map[string]Site{
		"unknown shard":  {Shard: 9, Blob: 0, Start: 0, End: 1},
		"unknown blob":   {Shard: 0, Blob: 9, Start: 0, End: 1},
		"negative start": {Shard: 0, Blob: 0, Start: -1, End: 1},
		"inverted range": {Shard: 0, Blob: 0, Start: 5, End: 2},
		"past end":       {Shard: 0, Blob: 0, Start: 0, End: len(blob.Content) + 1},
	}
	for name, site := range cases {
		if got := c.Text(site); got != nil {
			t.Errorf("Text(%s) = %q, want nil", name, got)
		}
	}
}
