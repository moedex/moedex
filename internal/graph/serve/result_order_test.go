package graphserve

import (
	"fmt"
	"math/rand"
	"path"
	"reflect"
	"sort"
	"strings"
	"testing"

	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
)

// Frozen pre-optimization implementation is an independent ordering and
// location-union oracle, including every existing evidence and location tie.
func (s *graphSnapshot) legacyMakeResult(tool, query string, depth int, confidence map[diskgraph.Key]graph.ConfidenceTier, distances map[diskgraph.Key]int, roots []diskgraph.Key, relations map[string]graphRelation) GraphQueryResult {
	result := GraphQueryResult{
		TotalIsExact: true,
		Tool:         tool,
		Query:        query,
		Depth:        depth,
		Nodes:        make([]GraphNode, 0, len(confidence)),
		Edges:        make([]GraphEdge, 0, len(relations)),
	}
	for key, tier := range confidence {
		meta := s.nodes[key]
		locations := meta.Locations
		if locations == nil {
			locations = s.locationsForKey(key)
		}
		if locations == nil {
			locations = []GraphLocation{}
		}
		result.Nodes = append(result.Nodes, GraphNode{
			ID:           keyID(key),
			Symbol:       meta.Symbol,
			Kind:         meta.Kind,
			BlobSHA:      key.BlobSHA,
			SymbolOffset: key.SymbolOffset,
			Confidence:   graph.ConfidenceOf(tier),
			Hops:         distances[key],
			Locations:    locations,
		})
	}
	for _, rel := range relations {
		result.Edges = append(result.Edges, GraphEdge{
			Source:     keyID(rel.Source),
			Target:     keyID(rel.Target),
			Type:       rel.Type.String(),
			Confidence: graph.ConfidenceOf(rel.Confidence),
			Evidence:   rel.Evidence,
			Similarity: rel.Similarity,
		})
	}
	rootLocations := s.legacyLocationsForKeys(roots)
	sort.Slice(result.Nodes, func(i, j int) bool {
		a, b := result.Nodes[i], result.Nodes[j]
		if a.Hops != b.Hops {
			return a.Hops < b.Hops
		}
		if a.Confidence.Score != b.Confidence.Score {
			return a.Confidence.Score > b.Confidence.Score
		}
		aSame, aDepth := legacyGraphLocationProximity(a.Locations, rootLocations)
		bSame, bDepth := legacyGraphLocationProximity(b.Locations, rootLocations)
		if aSame != bSame {
			return aSame
		}
		if aDepth != bDepth {
			return aDepth > bDepth
		}
		if a.Symbol != b.Symbol {
			return a.Symbol < b.Symbol
		}
		return a.ID < b.ID
	})
	nodeRank := make(map[string]int, len(result.Nodes))
	for i, node := range result.Nodes {
		nodeRank[node.ID] = i
	}
	sort.Slice(result.Edges, func(i, j int) bool {
		a, b := result.Edges[i], result.Edges[j]
		aDepth := max(nodeRank[a.Source], nodeRank[a.Target])
		bDepth := max(nodeRank[b.Source], nodeRank[b.Target])
		if aDepth != bDepth {
			return aDepth < bDepth
		}
		if a.Confidence.Score != b.Confidence.Score {
			return a.Confidence.Score > b.Confidence.Score
		}
		if nodeRank[a.Source] != nodeRank[b.Source] {
			return nodeRank[a.Source] < nodeRank[b.Source]
		}
		if nodeRank[a.Target] != nodeRank[b.Target] {
			return nodeRank[a.Target] < nodeRank[b.Target]
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		return evidenceLess(a.Evidence, b.Evidence)
	})
	return result
}

func (s *graphSnapshot) legacyLocationsForKeys(keys []diskgraph.Key) []GraphLocation {
	var out []GraphLocation
	for _, key := range keys {
		locations := s.nodes[key].Locations
		if locations == nil {
			locations = s.locationsForKey(key)
		}
		out = mergeLocations(out, locations)
	}
	return out
}

func resultFixture(nodes, rootCount int, seed int64) (*graphSnapshot, map[diskgraph.Key]graph.ConfidenceTier, map[diskgraph.Key]int, []diskgraph.Key, map[string]graphRelation) {
	random := rand.New(rand.NewSource(seed))
	s := &graphSnapshot{nodes: make(map[diskgraph.Key]nodeMetadata)}
	confidence := make(map[diskgraph.Key]graph.ConfidenceTier)
	distances := make(map[diskgraph.Key]int)
	var roots []diskgraph.Key
	keys := make([]diskgraph.Key, nodes)
	for i := range keys {
		key := diskgraph.Key{BlobSHA: fmt.Sprintf("blob-%05d", i), SymbolOffset: uint64(i * 10)}
		keys[i] = key
		loc := GraphLocation{Repo: "roslyn", Path: fmt.Sprintf("src/Compilers/Area%d/Sub%d/File%d.cs", random.Intn(32), random.Intn(8), i), AbsPath: fmt.Sprintf("/corpus/file%d.cs", i), Line: 1 + i%17, BlobSHA: key.BlobSHA}
		locations := []GraphLocation{loc}
		if i%7 == 0 {
			copy := loc
			copy.Repo = "other"
			copy.Path = "src/Shared/Helper.cs"
			locations = append(locations, copy)
		}
		s.nodes[key] = nodeMetadata{Symbol: fmt.Sprintf("Symbol%d", i%11), Kind: "Method", Locations: locations}
		confidence[key] = graph.Pattern
		distances[key] = 1
		if i < rootCount {
			roots = append(roots, key)
			distances[key] = 0
		}
	}
	relations := make(map[string]graphRelation)
	for i, key := range keys {
		if nodes == 0 {
			break
		}
		target := keys[(i+1)%nodes]
		relations[fmt.Sprint(i)] = graphRelation{Source: key, Target: target, Type: diskgraph.EdgeCalls, Confidence: graph.Pattern, Evidence: graph.Evidence{BlobSHA: key.BlobSHA, ByteOffset: uint64(i), ByteLength: 3}}
	}
	return s, confidence, distances, roots, relations
}

func BenchmarkGraphMakeResultManyRoots(b *testing.B) {
	s, confidence, distances, roots, relations := resultFixture(768, 128, 128)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		result := s.makeResult("trace_calls", "Create", 1, confidence, distances, roots, relations)
		if len(result.Nodes) != 768 {
			b.Fatal("missing nodes")
		}
	}
}

func TestGraphMakeResultMatchesLegacyOrder(t *testing.T) {
	for seed := int64(0); seed < 12; seed++ {
		s, confidence, distances, roots, relations := resultFixture(45, int(seed)%20, seed)
		// Multiple confidence/hop tiers must still take precedence over proximity.
		for key := range confidence {
			if key.SymbolOffset%30 == 0 {
				confidence[key] = graph.Verified
			}
			if key.SymbolOffset%50 == 0 {
				distances[key] = 2
			}
		}
		got := s.makeResult("trace_calls", "Create", 2, confidence, distances, roots, relations)
		want := s.legacyMakeResult("trace_calls", "Create", 2, confidence, distances, roots, relations)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("result ordering changed for seed %d", seed)
		}
	}
}

func legacyGraphLocationProximity(locations, anchors []GraphLocation) (sameRepo bool, prefixDepth int) {
	for _, loc := range locations {
		for _, anchor := range anchors {
			if loc.Repo != "" && loc.Repo == anchor.Repo {
				sameRepo = true
			}
			if depth := legacyDirectoryPrefixDepth(loc.Path, anchor.Path); depth > prefixDepth {
				prefixDepth = depth
			}
		}
	}
	return
}
func legacyDirectoryPrefixDepth(left, right string) int {
	leftDir, rightDir := path.Dir(strings.TrimPrefix(left, "./")), path.Dir(strings.TrimPrefix(right, "./"))
	if leftDir == "." || rightDir == "." {
		return 0
	}
	lp, rp := strings.Split(leftDir, "/"), strings.Split(rightDir, "/")
	depth := 0
	for depth < len(lp) && depth < len(rp) && lp[depth] == rp[depth] {
		depth++
	}
	return depth
}

func TestGraphProximityMatchesLegacyPathSemantics(t *testing.T) {
	paths := []string{"", ".", "./", "..", "../File.cs", "a", "a/", "/", "//", "/a", "/a/b.cs", "././a/b.cs", "a//b///File.cs", "a/../b/File.cs", "src/Code/A.cs", "src/Code/Deep/B.cs", "src/CodeElse/File.cs", "src/Other/C.cs", "Ünicode/子/File.cs", "\\windows\\file.cs", "a/\xff/File.cs"}
	var all []GraphLocation
	for i, p := range paths {
		repo := "repo"
		if i%3 == 0 {
			repo = ""
		} else if i%3 == 1 {
			repo = "other"
		}
		all = append(all, GraphLocation{Repo: repo, Path: p})
	}
	anchors := [][]GraphLocation{nil, {}, all, all[:len(all)/2], all[len(all)/2:]}
	for _, loc := range all {
		anchors = append(anchors, []GraphLocation{loc})
	}
	for _, roots := range anchors {
		index := newGraphProximity(roots)
		for i, loc := range all {
			for _, locations := range [][]GraphLocation{nil, {}, {loc}, {loc, all[(i+7)%len(all)]}} {
				got := index.rank(locations)
				same, depth := legacyGraphLocationProximity(locations, roots)
				if got.sameRepo != same || got.prefixDepth != depth {
					t.Fatalf("locations=%+v roots=%+v got%+v want%v/%d", locations, roots, got, same, depth)
				}
			}
		}
	}
}

func TestGraphRootLocationUnionPreservesIdentityAndFirstWinner(t *testing.T) {
	a, b, missing := diskgraph.Key{BlobSHA: "a"}, diskgraph.Key{BlobSHA: "b"}, diskgraph.Key{BlobSHA: "missing"}
	first := GraphLocation{Repo: "repo", Path: "src/F.cs", AbsPath: "/root/src/F.cs", Line: 3, BlobSHA: "first"}
	duplicate := first
	duplicate.BlobSHA = "later"
	// Preserve the historical concatenated identity even for unusual separator
	// bytes. The ordering remains by separate fields, not that identity string.
	unusual := GraphLocation{Repo: "a\x00b", Path: "c", Line: 1, BlobSHA: "winner"}
	collision := GraphLocation{Repo: "a", Path: "b\x00c", Line: 1, BlobSHA: "discarded"}
	s := &graphSnapshot{nodes: map[diskgraph.Key]nodeMetadata{
		a: {Locations: []GraphLocation{first, unusual, first}},
		b: {Locations: []GraphLocation{duplicate, collision, {Repo: "repo", Path: "src/F.cs", AbsPath: "/root/src/F.cs", Line: 4, BlobSHA: "next-line"}}},
	}}
	for _, keys := range [][]diskgraph.Key{nil, {}, {missing}, {a}, {b}, {a, b}, {b, a}, {a, b, a, missing}, {missing, a, b}} {
		got, want := s.locationsForKeys(keys), s.legacyLocationsForKeys(keys)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("keys%v got%+v want%+v", keys, got, want)
		}
	}
}

func BenchmarkGraphMakeResultLegacyManyRoots(b *testing.B) {
	s, confidence, distances, roots, relations := resultFixture(768, 128, 128)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		result := s.legacyMakeResult("trace_calls", "Create", 1, confidence, distances, roots, relations)
		if len(result.Nodes) != 768 {
			b.Fatal("missing nodes")
		}
	}
}
