package diskgraph

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"moedex/internal/graph"
)

func TestRoundTripMmapRecoversEdgesExactly(t *testing.T) {
	b := NewBuilder()
	first := Key{BlobSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SymbolOffset: 17}
	firstEdges := []Edge{
		{Type: EdgeCalls, TargetBlob: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", TargetOffset: 91, Confidence: graph.Pattern, Evidence: graph.Evidence{BlobSHA: first.BlobSHA, ByteOffset: 43, ByteLength: 12}},
		{Type: EdgeCandidate, TargetBlob: "cccccccccccccccccccccccccccccccccccccccc", TargetOffset: 1 << 40, Confidence: graph.Candidate, Evidence: graph.Evidence{BlobSHA: first.BlobSHA, ByteOffset: 1<<39 + 7, ByteLength: 3}},
		{Type: EdgeSimilarTo, TargetBlob: "dddddddddddddddddddddddddddddddddddddddd", TargetOffset: 23, Confidence: graph.Candidate, Evidence: graph.Evidence{BlobSHA: first.BlobSHA, ByteOffset: 17, ByteLength: 8}, Similarity: 0.812345},
	}
	if err := b.AddEdges(first, firstEdges...); err != nil {
		t.Fatal(err)
	}
	second := Key{BlobSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SymbolOffset: 2}
	secondEdges := []Edge{
		{Type: EdgeImports, TargetBlob: "dddddddddddddddddddddddddddddddddddddddd", TargetOffset: 0, Confidence: graph.Proven, Evidence: graph.Evidence{BlobSHA: second.BlobSHA, ByteOffset: 2, ByteLength: 8}},
	}
	if err := b.AddEdge(second, secondEdges[0]); err != nil {
		t.Fatal(err)
	}
	third := Key{BlobSHA: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", SymbolOffset: 0}
	thirdEdges := []Edge{
		{Type: EdgeType(1 << 20), TargetBlob: first.BlobSHA, TargetOffset: first.SymbolOffset, Confidence: graph.Verified, Evidence: graph.Evidence{BlobSHA: third.BlobSHA, ByteOffset: 0, ByteLength: 1}},
	}
	if err := b.AddEdge(third, thirdEdges[0]); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "test.graph")
	if err := b.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(raw[8:12]); got != 3 {
		t.Fatalf("graph format version = %d, want 3", got)
	}
	g, err := Open(path)
	if err != nil {
		t.Fatalf("open mmap: %v", err)
	}
	if got, want := g.NumNodes(), 3; got != want {
		t.Fatalf("NumNodes = %d, want %d", got, want)
	}
	if got, want := g.NumEdges(), 5; got != want {
		t.Fatalf("NumEdges = %d, want %d", got, want)
	}
	gotFirst := g.Edges(first)
	if !reflect.DeepEqual(gotFirst, firstEdges) {
		t.Fatalf("first edges:\n got  %#v\n want %#v", gotFirst, firstEdges)
	}
	if got := g.Load(second.BlobSHA, second.SymbolOffset); !reflect.DeepEqual(got, secondEdges) {
		t.Fatalf("second edges:\n got  %#v\n want %#v", got, secondEdges)
	}
	if got := g.Edges(third); !reflect.DeepEqual(got, thirdEdges) {
		t.Fatalf("third edges:\n got  %#v\n want %#v", got, thirdEdges)
	}
	if got := g.Edges(Key{BlobSHA: first.BlobSHA, SymbolOffset: 999}); got != nil {
		t.Fatalf("missing key returned %#v", got)
	}
	wantKeys := []Key{second, first, third}
	if got := g.Keys(); !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("Keys = %#v, want %#v", got, wantKeys)
	}

	// EachEdge is the reverse-traversal sweep: it must visit exactly what
	// Keys()+Edges() would, in the same order, without allocating per node.
	type visit struct {
		source Key
		edge   Edge
	}
	var swept []visit
	g.EachEdge(func(source Key, edge Edge) bool {
		swept = append(swept, visit{source, edge})
		return true
	})
	var want []visit
	for _, key := range wantKeys {
		for _, edge := range g.Edges(key) {
			want = append(want, visit{key, edge})
		}
	}
	if !reflect.DeepEqual(swept, want) {
		t.Fatalf("EachEdge sweep:\n got  %#v\n want %#v", swept, want)
	}
	allocs := testing.AllocsPerRun(20, func() {
		g.EachEdge(func(Key, Edge) bool { return true })
	})
	if allocs != 0 {
		t.Errorf("EachEdge allocated %.1f time(s) per sweep, want 0", allocs)
	}
	var stopped int
	g.EachEdge(func(Key, Edge) bool {
		stopped++
		return stopped < 2
	})
	if stopped != 2 {
		t.Errorf("EachEdge visited %d edge(s) after the walker stopped at 2", stopped)
	}
	if err := g.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := g.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	// Returned edges own their SHA strings rather than aliasing the unmapped file.
	if gotFirst[0].TargetBlob != firstEdges[0].TargetBlob {
		t.Fatalf("edge changed after unmap: %#v", gotFirst[0])
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load alias: %v", err)
	}
	defer loaded.Close()
	if got := loaded.Edges(first); !reflect.DeepEqual(got, firstEdges) {
		t.Fatalf("Load alias edges = %#v, want %#v", got, firstEdges)
	}
}

func TestRoundTripRetainsIsolatedNode(t *testing.T) {
	b := NewBuilder()
	isolated := Key{BlobSHA: "isolated-sha", SymbolOffset: 17}
	if err := b.AddNode(isolated); err != nil {
		t.Fatal(err)
	}
	if err := b.AddNode(isolated); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "isolated.graph")
	if err := b.Save(path); err != nil {
		t.Fatal(err)
	}
	g, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if got := g.Keys(); !reflect.DeepEqual(got, []Key{isolated}) {
		t.Fatalf("Keys = %#v, want isolated node", got)
	}
	if got := g.Edges(isolated); len(got) != 0 {
		t.Errorf("isolated adjacency = %#v, want empty", got)
	}
	if g.NumNodes() != 1 || g.NumEdges() != 0 {
		t.Errorf("counts = %d nodes, %d edges, want 1 and 0", g.NumNodes(), g.NumEdges())
	}
}

func TestBuilderAddOrUpgradeEdgePromotesWithoutDuplicating(t *testing.T) {
	b := NewBuilder()
	source := Key{BlobSHA: "source", SymbolOffset: 7}
	pattern := Edge{
		Type:         EdgeCalls,
		TargetBlob:   "target",
		TargetOffset: 11,
		Confidence:   graph.Pattern,
		Evidence:     graph.Evidence{BlobSHA: "source", ByteOffset: 23, ByteLength: 2},
	}
	if err := b.AddEdge(source, pattern); err != nil {
		t.Fatal(err)
	}
	proven := pattern
	proven.Confidence = graph.Proven
	added, err := b.AddOrUpgradeEdge(source, proven)
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Fatal("confidence promotion appended a duplicate relationship")
	}
	if got := b.NumEdges(); got != 1 {
		t.Fatalf("NumEdges = %d, want one upgraded record", got)
	}
	if got := b.adjacency[source]; !reflect.DeepEqual(got, []Edge{proven}) {
		t.Fatalf("upgraded adjacency = %#v, want %#v", got, []Edge{proven})
	}
	weaker := pattern
	weaker.Confidence = graph.Candidate
	if added, err := b.AddOrUpgradeEdge(source, weaker); err != nil || added {
		t.Fatalf("weaker duplicate = added %v, err %v", added, err)
	}
	if got := b.adjacency[source][0].Confidence; got != graph.Proven {
		t.Fatalf("weaker duplicate demoted confidence to %s", got)
	}
}

func TestRoundTripEmptyGraph(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.graph")
	if err := NewBuilder().Save(path); err != nil {
		t.Fatal(err)
	}
	g, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if g.NumNodes() != 0 || g.NumEdges() != 0 || g.Keys() != nil {
		t.Fatalf("empty graph = nodes %d edges %d keys %#v", g.NumNodes(), g.NumEdges(), g.Keys())
	}
}

func TestBuilderRejectsMissingBlobIdentity(t *testing.T) {
	b := NewBuilder()
	if err := b.Add("", 0, Edge{TargetBlob: "target"}); err == nil {
		t.Fatal("empty source SHA accepted")
	}
	if err := b.Add("source", 0, Edge{}); err == nil {
		t.Fatal("empty target SHA accepted")
	}
	validEvidence := graph.Evidence{BlobSHA: "source", ByteLength: 1}
	if err := b.Add("source", 0, Edge{TargetBlob: "target", Confidence: graph.ConfidenceTier(99), Evidence: validEvidence}); err == nil {
		t.Fatal("invalid confidence tier accepted")
	}
	if err := b.Add("source", 0, Edge{TargetBlob: "target", Confidence: graph.Pattern}); err == nil {
		t.Fatal("missing evidence accepted")
	}
}

func TestRoundTripNamesAndGenerations(t *testing.T) {
	b := NewBuilder()
	b.SetGeneration(7)
	source := Key{BlobSHA: "aaaa", SymbolOffset: 4}
	edges := []Edge{
		{Type: EdgeCalls, TargetBlob: "bbbb", TargetOffset: 1, Confidence: graph.Pattern, Evidence: graph.Evidence{BlobSHA: "aaaa", ByteOffset: 2, ByteLength: 1}, Name: "Zeta", Generation: 7},
		{Type: EdgeImports, TargetBlob: "cccc", TargetOffset: 3, Confidence: graph.Candidate, Evidence: graph.Evidence{BlobSHA: "aaaa", ByteOffset: 4, ByteLength: 1}, Name: "Alpha", Generation: 3},
		{Type: EdgeReferences, TargetBlob: "bbbb", TargetOffset: 5, Confidence: graph.Proven, Evidence: graph.Evidence{BlobSHA: "aaaa", ByteOffset: 6, ByteLength: 1}, Name: "Zeta", Generation: 1},
	}
	if err := b.AddEdges(source, edges...); err != nil {
		t.Fatal(err)
	}
	if got, want := b.Generation(), uint64(7); got != want {
		t.Fatalf("builder generation = %d, want %d", got, want)
	}

	path := filepath.Join(t.TempDir(), "named.graph")
	if err := b.Save(path); err != nil {
		t.Fatal(err)
	}
	g, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	if got, want := g.Generation(), uint64(7); got != want {
		t.Fatalf("file generation = %d, want %d", got, want)
	}
	if got := g.Edges(source); !reflect.DeepEqual(got, edges) {
		t.Fatalf("edges:\n got  %#v\n want %#v", got, edges)
	}
	if got, want := g.Names(), []string{"Alpha", "Zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names = %#v, want %#v", got, want)
	}

	var walked []Edge
	g.EachEdge(func(key Key, edge Edge) bool {
		if key != source {
			t.Fatalf("EachEdge key = %#v, want %#v", key, source)
		}
		walked = append(walked, edge)
		return true
	})
	if !reflect.DeepEqual(walked, edges) {
		t.Fatalf("EachEdge:\n got  %#v\n want %#v", walked, edges)
	}

	count := 0
	g.EachEdge(func(Key, Edge) bool { count++; return false })
	if count != 1 {
		t.Fatalf("EachEdge visited %d edge(s) after returning false, want 1", count)
	}
}

func TestRoundTripCorpusRoster(t *testing.T) {
	b := NewBuilder()
	if err := b.Add("aaaa", 0, Edge{TargetBlob: "bbbb", Confidence: graph.Candidate, Evidence: graph.Evidence{BlobSHA: "aaaa", ByteOffset: 0, ByteLength: 1}, Name: "Used"}); err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{"bbbb", "aaaa", "edgeless", "aaaa"} {
		b.AddCorpusEntry(sha)
	}
	b.AddCorpusEntry("")
	if got, want := b.NumCorpusEntries(), 3; got != want {
		t.Fatalf("builder roster = %d, want %d", got, want)
	}

	path := filepath.Join(t.TempDir(), "roster.graph")
	if err := b.Save(path); err != nil {
		t.Fatal(err)
	}
	g, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	if got, want := g.NumCorpusEntries(), 3; got != want {
		t.Fatalf("roster = %d blob(s), want %d", got, want)
	}
	var sorted []string
	g.EachCorpusEntry(func(sha string) bool { sorted = append(sorted, sha); return true })
	if want := []string{"aaaa", "bbbb", "edgeless"}; !reflect.DeepEqual(sorted, want) {
		t.Fatalf("EachCorpusEntry = %#v, want %#v", sorted, want)
	}
	set := g.CorpusEntrySet()
	if len(set) != 3 {
		t.Fatalf("CorpusEntrySet = %#v", set)
	}
	if _, ok := set["edgeless"]; !ok {
		t.Fatalf("edge-free blob missing from the roster: %#v", set)
	}
	bare := NewBuilder()
	barePath := filepath.Join(t.TempDir(), "bare.graph")
	if err := bare.Save(barePath); err != nil {
		t.Fatal(err)
	}
	bg, err := Open(barePath)
	if err != nil {
		t.Fatal(err)
	}
	defer bg.Close()
	if bg.NumCorpusEntries() != 0 || len(bg.CorpusEntrySet()) != 0 {
		t.Fatalf("empty roster = %d / %#v", bg.NumCorpusEntries(), bg.CorpusEntrySet())
	}
	if got, want := bg.Generation(), FirstGeneration; got != want {
		t.Fatalf("default generation = %d, want %d", got, want)
	}
}

func TestOpenRejectsForeignFormats(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.graph")
	b := NewBuilder()
	if err := b.Add("aaaa", 0, Edge{TargetBlob: "bbbb", Confidence: graph.Candidate, Evidence: graph.Evidence{BlobSHA: "aaaa", ByteOffset: 0, ByteLength: 1}, Name: "N"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(valid); err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(valid)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		mutate  func(b []byte)
		wantErr string
	}{
		{"old magic", func(b []byte) { copy(b[0:8], "MDXGRF00") }, "bad magic"},
		{"future version", func(b []byte) { binary.LittleEndian.PutUint32(b[8:12], 99) }, "unsupported version"},
		{"bad header size", func(b []byte) { binary.LittleEndian.PutUint32(b[12:16], 96) }, "unsupported header size"},
		{"shuffled sections", func(b []byte) { binary.LittleEndian.PutUint64(b[64:72], 0) }, "corrupt section offsets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			corrupt := append([]byte(nil), good...)
			tc.mutate(corrupt)
			path := filepath.Join(t.TempDir(), "corrupt.graph")
			if err := os.WriteFile(path, corrupt, 0o644); err != nil {
				t.Fatal(err)
			}
			g, err := Open(path)
			if err == nil {
				g.Close()
				t.Fatal("corrupt file opened cleanly")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestEdgeJSONUsesStructuredConfidenceAndEvidence(t *testing.T) {
	edge := Edge{
		Type:         EdgeCalls,
		TargetBlob:   "target-sha",
		TargetOffset: 7,
		Confidence:   graph.Verified,
		Evidence:     graph.Evidence{BlobSHA: "source-sha", ByteOffset: 11, ByteLength: 5},
	}
	body, err := json.Marshal(edge)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"confidence":{"tier":"Verified","score":0.85}`,
		`"evidence":{"blob_sha":"source-sha","byte_offset":11,"byte_length":5}`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("edge JSON %s does not contain %s", body, want)
		}
	}
}

func TestSimilarEdgeJSONKeepsTierAndCosineSeparate(t *testing.T) {
	edge := Edge{
		Type:         EdgeSimilarTo,
		TargetBlob:   "target-sha",
		TargetOffset: 7,
		Confidence:   graph.Candidate,
		Evidence:     graph.Evidence{BlobSHA: "source-sha", ByteOffset: 11, ByteLength: 5},
		Similarity:   0.6671,
	}
	body, err := json.Marshal(edge)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"confidence":{"tier":"Candidate","score":0.3}`,
		`"similarity":0.6671`,
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("similar edge JSON %s does not contain %s", body, want)
		}
	}
}
