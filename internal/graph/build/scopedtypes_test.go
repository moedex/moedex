package graphbuild

import (
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
)

func TestTypeBindingConfidenceFromSource(t *testing.T) {
	const source = "namespace App;\npublic class Consumer : IBase\n{\n public void Configure() { services.AddScoped<IBase, Worker>(); }\n}\n"
	base := []graphFile{
		{repo: "app", path: "consumer.cs", content: source},
		{repo: "app", path: "base.cs", content: "namespace App;\npublic interface IBase { }\n"},
		{repo: "app", path: "worker.cs", content: "namespace App;\npublic class Worker { }\n"},
	}
	foreign := []graphFile{
		{repo: "other", path: "base.cs", content: "namespace Other;\npublic interface IBase { }\n"},
		{repo: "other", path: "worker.cs", content: "namespace Other;\npublic class Worker { }\n"},
	}
	typescript := graphFile{repo: "app", path: "ui.ts", content: "export interface IBase { }\nexport class Worker { }\n"}
	tests := []struct {
		name         string
		files        []graphFile
		allCandidate bool
	}{
		{name: "unique local syntax", files: base},
		{name: "cross repository distractors", files: append(append([]graphFile{}, base...), foreign...)},
		{name: "foreign only", files: append([]graphFile{base[0]}, foreign...), allCandidate: true},
		{name: "other language only", files: []graphFile{base[0], typescript}, allCandidate: true},
		{name: "other language distractor", files: append(append([]graphFile{}, base...), typescript)},
		{name: "namespace ambiguity", files: append(append([]graphFile{}, base...),
			graphFile{repo: "app", path: "other-base.cs", content: foreign[0].content},
			graphFile{repo: "app", path: "other-worker.cs", content: foreign[1].content}), allCandidate: true},
		{name: "shared source across repositories", files: append(append([]graphFile{}, base...), graphFile{repo: "other", path: "consumer.cs", content: source}), allCandidate: true},
		{name: "shared source across paths", files: append(append([]graphFile{}, base...), graphFile{repo: "app", path: "copy/consumer.cs", content: source}), allCandidate: true},
		{name: "shared targets across repositories", files: append(append([]graphFile{}, base...),
			graphFile{repo: "other", path: "base.cs", content: base[1].content},
			graphFile{repo: "other", path: "worker.cs", content: base[2].content}), allCandidate: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeGraphShards(t, dir, tc.files, 1)
			path, _, err := BuildGraph(dir)
			if err != nil {
				t.Fatal(err)
			}
			g, err := diskgraph.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			localBase := diskstore.GitBlobSHA1([]byte(base[1].content))
			localWorker := diskstore.GitBlobSHA1([]byte(base[2].content))
			consumer := diskstore.GitBlobSHA1([]byte(source))
			counts := make(map[string]int)
			g.EachEdge(func(key diskgraph.Key, edge diskgraph.Edge) bool {
				lane := ""
				switch {
				case edge.Type == diskgraph.EdgeInjects:
					lane = "injects"
				case edge.Type == diskgraph.EdgeImplements && key.BlobSHA == consumer:
					lane = "hierarchy"
				case edge.Type == diskgraph.EdgeImplements:
					lane = "di implements"
				default:
					return true
				}
				counts[lane]++
				want := graph.Candidate
				if !tc.allCandidate && (edge.TargetBlob == localBase || edge.TargetBlob == localWorker) && (key.BlobSHA == consumer || key.BlobSHA == localWorker) {
					want = graph.Pattern
				}
				if edge.Confidence != want {
					t.Errorf("%s %v -> %s: tier %s, want %s", lane, key, edge.TargetBlob, edge.Confidence, want)
				}
				if !edge.Evidence.Valid() {
					t.Errorf("%s lacks evidence", lane)
				}
				return true
			})
			for _, lane := range []string{"hierarchy", "injects", "di implements"} {
				if counts[lane] == 0 {
					t.Errorf("source extraction emitted no %s edges", lane)
				}
			}
			_, stats, err := RefreshGraph(dir)
			if err != nil {
				t.Fatal(err)
			}
			if !stats.Unchanged {
				t.Errorf("unchanged candidate graph rebuilt: %+v", stats)
			}
		})
	}
}

func TestTypeBindingRejectsNonTypeDefinitions(t *testing.T) {
	dir := t.TempDir()
	files := []graphFile{
		{repo: "app", path: "consumer.cs", content: "public class Consumer : MissingType { }\n"},
		{repo: "app", path: "other.cs", content: "public class Other\n{\n public void MissingType() { }\n}\n"},
	}
	writeGraphShards(t, dir, files, 1)
	path, _, err := BuildGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range readGraph(t, path) {
		if record.edge.Type == diskgraph.EdgeExtends || record.edge.Type == diskgraph.EdgeImplements {
			t.Fatalf("method mistaken for base type: %+v", record)
		}
	}
}

func TestTopLevelAmbiguousDIRefreshIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	files := []graphFile{
		{repo: "app", path: "startup.cs", content: "services.AddScoped<IBase, Worker>();\n"},
		{repo: "other", path: "types.cs", content: "public interface IBase { }\npublic class Worker { }\n"},
	}
	writeGraphShards(t, dir, files, 1)
	path, _, err := BuildGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range readGraph(t, path) {
		if r.edge.Type == diskgraph.EdgeInjects {
			found = true
			if r.edge.Confidence != graph.Candidate || r.key.SymbolOffset != 0 {
				t.Fatalf("top-level DI = %+v", r)
			}
		}
	}
	if !found {
		t.Fatal("missing diagnostic DI candidate")
	}
	_, stats, err := RefreshGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Unchanged {
		t.Fatalf("diagnostic candidate forced rebuild: %+v", stats)
	}
}

func TestRefreshGraphTracksTypeBindingContexts(t *testing.T) {
	for _, change := range []string{"add repository", "add path", "move repository"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			files := []graphFile{
				{repo: "app", path: "derived.cs", content: "public class Derived : Base { }\n"},
				{repo: "app", path: "base.cs", content: "public class Base { }\n"},
			}
			writeGraphShards(t, dir, files, 1)
			if _, _, err := BuildGraph(dir); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "add repository":
				files = append(files, graphFile{repo: "other", path: "derived.cs", content: files[0].content})
			case "add path":
				files = append(files, graphFile{repo: "app", path: "copy.cs", content: files[0].content})
			case "move repository":
				files[0].repo = "other"
			}
			writeGraphShards(t, dir, files, 1)
			path, stats, err := RefreshGraph(dir)
			if err != nil {
				t.Fatal(err)
			}
			if stats.Unchanged {
				t.Fatal("context change was not detected")
			}
			found := false
			for _, r := range readGraph(t, path) {
				if r.edge.Type == diskgraph.EdgeExtends {
					found = true
					if r.edge.Confidence != graph.Candidate {
						t.Fatalf("context change retained tier %s", r.edge.Confidence)
					}
				}
			}
			if !found {
				t.Fatal("missing diagnostic hierarchy candidate")
			}
			full := copyShards(t, dir)
			fullPath, _, err := BuildGraph(full)
			if err != nil {
				t.Fatal(err)
			}
			requireSameGraph(t, "context refresh parity", withoutGenerations(readGraph(t, path)), withoutGenerations(readGraph(t, fullPath)))
			_, stats, err = RefreshGraph(dir)
			if err != nil {
				t.Fatal(err)
			}
			if !stats.Unchanged {
				t.Fatal("second refresh was not a no-op")
			}
		})
	}
}

func TestRefreshGraphMigratesLegacyTypeBindingPolicy(t *testing.T) {
	dir := t.TempDir()
	files := []graphFile{
		{repo: "app", path: "derived.cs", content: "public class Derived : Base { }\n"},
		{repo: "app", path: "base.cs", content: "public class Base { }\n"},
	}
	writeGraphShards(t, dir, files, 1)
	builder := diskgraph.NewBuilder()
	sha := func(i int) string { return diskstore.GitBlobSHA1([]byte(files[i].content)) }
	for i := range files {
		builder.AddCorpusEntry(sha(i) + "\x00.cs")
	}
	source := diskgraph.Key{BlobSHA: sha(0), SymbolOffset: uint64(strings.Index(files[0].content, "Derived"))}
	if err := builder.AddEdge(source, diskgraph.Edge{
		Type: diskgraph.EdgeExtends, TargetBlob: sha(1), TargetOffset: uint64(strings.Index(files[1].content, "Base")), Confidence: graph.Proven,
		Evidence: graph.Evidence{BlobSHA: sha(0), ByteOffset: uint64(strings.Index(files[0].content, "Base")), ByteLength: 4},
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
	if stats.Unchanged {
		t.Fatal("legacy policy graph was reused")
	}
	found := false
	for _, r := range readGraph(t, path) {
		if r.edge.Type == diskgraph.EdgeExtends {
			found = true
			if r.edge.Confidence != graph.Pattern {
				t.Fatalf("legacy confidence survived: %s", r.edge.Confidence)
			}
		}
	}
	if !found {
		t.Fatal("hierarchy lost during migration")
	}
	_, stats, err = RefreshGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Unchanged {
		t.Fatal("migrated graph did not reach no-op state")
	}
}
