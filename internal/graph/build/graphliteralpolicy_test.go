package graphbuild

import (
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
)

func TestRefreshGraphMigratesRawLiteralVerificationPolicy(t *testing.T) {
	dir := t.TempDir()
	content := "public class Real {\npublic void ProcessOrder() {}\npublic void Run() {\nvar text = \"\"\"\"\nprefix \"\"\"\nProcessOrder(); // quoted\n\"\"\"\";\nProcessOrder(); // real\n}\n}\n"
	writeGraphShards(t, dir, []graphFile{{repo: "app", path: "fixture.cs", content: content}}, 1)
	s, err := openGraphSweep(dir)
	if err != nil {
		t.Fatal(err)
	}
	builder := diskgraph.NewBuilder()
	for _, token := range s.identity {
		if !strings.Contains(token, "\x00"+graphBindingPolicy+"\x00") {
			t.Fatalf("roster omits verification policy: %q", token)
		}
		builder.AddCorpusEntry(strings.Replace(token, "\x00"+graphBindingPolicy+"\x00", "\x00scoped-bindings-v1\x00", 1))
	}
	s.Close()
	sha := diskstore.GitBlobSHA1([]byte(content))
	quoted := uint64(strings.Index(content, "ProcessOrder(); // quoted"))
	if err := builder.AddEdge(diskgraph.Key{BlobSHA: sha, SymbolOffset: quoted}, diskgraph.Edge{
		Type: diskgraph.EdgeCalls, TargetBlob: sha, TargetOffset: uint64(strings.Index(content, "ProcessOrder")), Name: "ProcessOrder", Confidence: graph.Pattern, Generation: 1,
		Evidence: graph.Evidence{BlobSHA: sha, ByteOffset: quoted, ByteLength: uint64(len("ProcessOrder"))},
	}); err != nil {
		t.Fatal(err)
	}
	if err := builder.Save(GraphPath(dir)); err != nil {
		t.Fatal(err)
	}
	path, stats, err := RefreshGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Unchanged || stats.NamesRecomputed == 0 {
		t.Fatalf("old verifier policy was retained: %+v", stats)
	}
	real := uint64(strings.Index(content, "ProcessOrder(); // real"))
	foundReal := false
	for _, record := range readGraph(t, path) {
		if record.edge.Name != "ProcessOrder" || record.edge.Type != diskgraph.EdgeCalls {
			continue
		}
		if record.edge.Evidence.ByteOffset == quoted {
			t.Fatalf("quoted call survived migration: %+v", record)
		}
		if record.edge.Evidence.ByteOffset == real && record.edge.Confidence == graph.Pattern {
			foundReal = true
		}
	}
	if !foundReal {
		t.Fatal("real call was lost during migration")
	}
	_, again, err := RefreshGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Unchanged {
		t.Fatalf("current verifier policy did not settle: %+v", again)
	}
}
