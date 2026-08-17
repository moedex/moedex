package diskgraph

import (
	"math"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRoundTripMmapRecoversEdgesExactly(t *testing.T) {
	b := NewBuilder()
	first := Key{BlobSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SymbolOffset: 17}
	firstEdges := []Edge{
		{Type: EdgeCalls, TargetBlob: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", TargetOffset: 91, Confidence: 0.6, EvidenceOffset: 43},
		{Type: EdgeCandidate, TargetBlob: "cccccccccccccccccccccccccccccccccccccccc", TargetOffset: 1 << 40, Confidence: math.SmallestNonzeroFloat64, EvidenceOffset: 1<<39 + 7},
	}
	if err := b.AddEdges(first, firstEdges...); err != nil {
		t.Fatal(err)
	}
	second := Key{BlobSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SymbolOffset: 2}
	secondEdges := []Edge{
		{Type: EdgeImports, TargetBlob: "dddddddddddddddddddddddddddddddddddddddd", TargetOffset: 0, Confidence: 1, EvidenceOffset: 2},
	}
	if err := b.AddEdge(second, secondEdges[0]); err != nil {
		t.Fatal(err)
	}
	third := Key{BlobSHA: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", SymbolOffset: 0}
	thirdEdges := []Edge{
		{Type: EdgeType(1 << 20), TargetBlob: first.BlobSHA, TargetOffset: first.SymbolOffset, Confidence: math.Inf(1), EvidenceOffset: 0},
	}
	if err := b.AddEdge(third, thirdEdges[0]); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "test.graph")
	if err := b.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	g, err := Open(path)
	if err != nil {
		t.Fatalf("open mmap: %v", err)
	}
	if got, want := g.NumNodes(), 3; got != want {
		t.Fatalf("NumNodes = %d, want %d", got, want)
	}
	if got, want := g.NumEdges(), 4; got != want {
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
}
