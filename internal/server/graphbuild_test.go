package server

import (
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
)

func TestBuildGraphPersistsVerifiedAdjacency(t *testing.T) {
	dir := t.TempDir()
	targetContent := []byte("package target\n\nfunc Target() {}\n")
	callerContent := []byte("package caller\n\nfunc Caller() { Target() }\n")
	targetSHA := diskstore.GitBlobSHA1(targetContent)
	callerSHA := diskstore.GitBlobSHA1(callerContent)

	target := index.New()
	target.AddFile("target", "target.go", "/target/target.go", targetSHA, targetContent)
	if err := diskstore.Save(target, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}
	caller := index.New()
	caller.AddFile("caller", "caller.go", "/caller/caller.go", callerSHA, callerContent)
	if err := diskstore.Save(caller, filepath.Join(dir, "shard-0001.idx")); err != nil {
		t.Fatal(err)
	}

	path, _, err := BuildGraph(dir)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	if path != GraphPath(dir) {
		t.Fatalf("path = %q, want %q", path, GraphPath(dir))
	}
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatalf("open graph: %v", err)
	}
	defer g.Close()

	callerOffset := uint64(strings.Index(string(callerContent), "Caller"))
	evidenceOffset := uint64(strings.LastIndex(string(callerContent), "Target"))
	targetOffset := uint64(strings.Index(string(targetContent), "Target"))
	edges := g.Load(callerSHA, callerOffset)
	if len(edges) != 1 {
		t.Fatalf("Caller adjacency = %#v, want one edge", edges)
	}
	want := diskgraph.Edge{
		Type:         diskgraph.EdgeCalls,
		TargetBlob:   targetSHA,
		TargetOffset: targetOffset,
		Confidence:   graph.Pattern,
		Evidence: graph.Evidence{
			BlobSHA:    callerSHA,
			ByteOffset: evidenceOffset,
			ByteLength: uint64(len("Target")),
		},
		Name:       "Target",
		Generation: diskgraph.FirstGeneration,
	}
	if edges[0] != want {
		t.Fatalf("edge = %#v, want %#v", edges[0], want)
	}
	if got := g.Generation(); got != diskgraph.FirstGeneration {
		t.Fatalf("graph generation = %d, want %d", got, diskgraph.FirstGeneration)
	}
	if got, want := g.NumCorpusEntries(), 2; got != want {
		t.Fatalf("corpus roster = %d blob(s), want %d", got, want)
	}
	line, ok := edges[0].Evidence.SourceLine(callerContent)
	if !ok || string(line) != "func Caller() { Target() }" {
		t.Fatalf("evidence dereference = %q, %v, want caller source line", line, ok)
	}
}

func TestBuildGraphWithOptionsUsesRequestedGeneration(t *testing.T) {
	dir := t.TempDir()
	content := []byte("package p\n\nfunc Target() {}\nfunc Caller() { Target() }\n")
	sha := diskstore.GitBlobSHA1(content)
	ix := index.New()
	ix.AddFile("repo", "main.go", "/repo/main.go", sha, content)
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}

	const generation = 7
	path, _, err := BuildGraphWithOptions(dir, GraphBuildOptions{Generation: generation})
	if err != nil {
		t.Fatalf("BuildGraphWithOptions: %v", err)
	}
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatalf("open graph: %v", err)
	}
	defer g.Close()
	if got := g.Generation(); got != generation {
		t.Fatalf("graph generation = %d, want %d", got, generation)
	}
	var edges int
	g.EachEdge(func(_ diskgraph.Key, edge diskgraph.Edge) bool {
		edges++
		if edge.Generation != generation {
			t.Errorf("edge generation = %d, want %d", edge.Generation, generation)
		}
		return true
	})
	if edges == 0 {
		t.Fatal("fixture produced no graph edges")
	}
}

func TestBuildGraphFromDedupedShards(t *testing.T) {
	dir := t.TempDir()
	targetContent := []byte("package target\n\nfunc Target() {}\n")
	callerContent := []byte("package caller\n\nfunc Caller() { Target() }\n")
	targetSHA := diskstore.GitBlobSHA1(targetContent)
	callerSHA := diskstore.GitBlobSHA1(callerContent)
	target := index.New()
	target.AddFile("target", "target.go", "/target/target.go", targetSHA, targetContent)
	caller := index.New()
	caller.AddFile("caller", "caller.go", "/caller/caller.go", callerSHA, callerContent)

	writer, err := diskstore.NewContentStoreWriter()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := diskstore.SaveDeduped(target, filepath.Join(dir, "shard-0000.idx"), writer); err != nil {
		t.Fatal(err)
	}
	if err := diskstore.SaveDeduped(caller, filepath.Join(dir, "shard-0001.idx"), writer); err != nil {
		t.Fatal(err)
	}
	if err := writer.Write(filepath.Join(dir, diskstore.ContentStoreName)); err != nil {
		t.Fatal(err)
	}

	path, _, err := BuildGraph(dir)
	if err != nil {
		t.Fatalf("BuildGraph deduped: %v", err)
	}
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	callerOffset := uint64(strings.Index(string(callerContent), "Caller"))
	edges := g.Load(callerSHA, callerOffset)
	if len(edges) != 1 || edges[0].TargetBlob != targetSHA || edges[0].Type != diskgraph.EdgeCalls {
		t.Fatalf("deduped Caller adjacency = %#v", edges)
	}
}

func TestBuildGraphSuppressesRawCandidateOccurrences(t *testing.T) {
	dir := t.TempDir()
	definition := []byte("package target\n\nfunc ProcessOrder() {}\n")
	noise := []byte("package notes\n\n// ProcessOrder() is mentioned only in a comment.\n")
	ix := index.New()
	ix.AddFile("target", "target.go", "/target/target.go", "definition-sha", definition)
	ix.AddFile("notes", "notes.go", "/notes/notes.go", "noise-sha", noise)
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}
	path, report, err := BuildGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts.SuppressedRawCandidates == 0 {
		t.Fatalf("build report did not record raw Candidate suppression: %+v", report.Counts)
	}
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	definitionKey := diskgraph.Key{BlobSHA: "definition-sha", SymbolOffset: uint64(strings.Index(string(definition), "ProcessOrder"))}
	foundDefinition := false
	for _, key := range g.Keys() {
		if key.BlobSHA == "noise-sha" {
			t.Fatalf("raw Candidate occurrence minted graph node %+v", key)
		}
		foundDefinition = foundDefinition || key == definitionKey
	}
	if !foundDefinition {
		t.Fatalf("suppression dropped definition target %+v", definitionKey)
	}
}
