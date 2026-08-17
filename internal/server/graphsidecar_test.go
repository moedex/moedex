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

func TestBuildGraphSidecarPersistsVerifiedAdjacency(t *testing.T) {
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

	path, _, err := BuildGraphSidecar(dir)
	if err != nil {
		t.Fatalf("BuildGraphSidecar: %v", err)
	}
	if path != GraphSidecarPath(dir) {
		t.Fatalf("path = %q, want %q", path, GraphSidecarPath(dir))
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
	}
	if edges[0] != want {
		t.Fatalf("edge = %#v, want %#v", edges[0], want)
	}
	line, ok := edges[0].Evidence.SourceLine(callerContent)
	if !ok || string(line) != "func Caller() { Target() }" {
		t.Fatalf("evidence dereference = %q, %v, want caller source line", line, ok)
	}
}

func TestBuildGraphSidecarFromDedupedShards(t *testing.T) {
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

	path, _, err := BuildGraphSidecar(dir)
	if err != nil {
		t.Fatalf("BuildGraphSidecar deduped: %v", err)
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
