package graphbuild

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/graph"
	"moedex/internal/graph/candidates"
	"moedex/internal/graph/diskgraph"
	graphverify "moedex/internal/graph/verify"
	"moedex/internal/index"
	"moedex/internal/symbol"
)

// legacyExpandedEdges is the pre-optimization Cartesian-product path. Keep this
// independent of source preparation so the differential test catches changes in
// edge type, evidence, self filtering, ordering and both suppression counters.
func legacyExpandedEdges(s *graphSweep, name string, p *candidates.PreparedName, start, end int, generation uint64) []graphKeyEdge {
	var out []graphKeyEdge
	for _, sc := range graphverify.Verify(p.Generate(start, end)) {
		sourceBlob, targetBlob := s.corpus.Blob(sc.Source), s.corpus.Blob(sc.Target)
		if sc.Confidence == graphverify.Pattern && !s.blobSHAsShareRepository(sourceBlob.SHA, targetBlob.SHA) {
			s.suppressedCrossRepoPatterns.Add(1)
			continue
		}
		offset := uint64(sc.Source.Start)
		enclosing, ok := s.merged.Enclosing(sc.Source.Shard, sc.Source.Blob, sc.Source.Start)
		if ok {
			offset = uint64(enclosing.NameStart)
		}
		if sc.Confidence == graphverify.Candidate && !ok {
			s.suppressedRawCandidates.Add(1)
			continue
		}
		out = append(out, graphKeyEdge{Key: diskgraph.Key{BlobSHA: sourceBlob.SHA, SymbolOffset: offset}, Edge: diskgraph.Edge{
			Type: graphEdgeType(sc, sourceBlob.Content), TargetBlob: targetBlob.SHA, TargetOffset: uint64(sc.Target.Start),
			Confidence: sc.Confidence, Evidence: sc.Evidence, Name: name, Generation: generation,
		}})
	}
	return out
}

func TestPreparedGraphFanoutMatchesExpandedOracle(t *testing.T) {
	shared := "package demo\nfunc SharedCaller() { Target() }\n"
	files := []graphFile{
		{"alpha", "a.go", "package demo\nfunc Target() {}\nfunc Caller() { Target(); /* Target() */ }\n"},
		{"alpha", "b.go", "package demo\nfunc Target() {}\n// Target() outside a symbol\n"},
		{"beta", "c.go", "package demo\nfunc Target() {}\nfunc ForeignCaller() { Target() }\n"},
		{"alpha", "shared.go", shared}, {"beta", "shared.go", shared},
		{"alpha", "notes.cs", "// Target outside code\nclass Fixture { void Test() { var s = \"Target()\"; Target(); } }\n"},
		{"", "unknown.go", "package demo\nfunc UnknownCaller() { Target() }\n"},
	}
	dir := t.TempDir()
	writeGraphShards(t, dir, files, 1)
	s, err := openGraphSweep(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := candidates.PrepareName(s.corpus, "Target")
	want := legacyExpandedEdges(s, "Target", p, 0, p.NumSources(), 17)
	raw, cross := s.suppressedRawCandidates.Swap(0), s.suppressedCrossRepoPatterns.Swap(0)
	got, stats, err := s.computeEdgesParallelBatched([]string{"Target"}, 17, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got[0].Edges, want) {
		t.Fatalf("source-first output differs: got %#v want %#v", got[0].Edges, want)
	}
	if s.suppressedRawCandidates.Load() != raw || s.suppressedCrossRepoPatterns.Load() != cross {
		t.Fatalf("suppression changed: raw %d/%d cross %d/%d", s.suppressedRawCandidates.Load(), raw, s.suppressedCrossRepoPatterns.Load(), cross)
	}
	if raw == 0 || cross == 0 {
		t.Fatalf("fixture must exercise both suppression paths: %d/%d", raw, cross)
	}
	if stats.SourcesVerified != uint64(p.NumSources()) || stats.CandidatePairs != uint64(len(p.Generate(0, p.NumSources()))) || stats.RetainedPairs != uint64(len(want)) {
		t.Fatalf("source/pair accounting: %+v", stats)
	}
	if stats.CandidatePairs != stats.RetainedPairs+uint64(raw+cross) {
		t.Fatalf("pair accounting does not balance: %+v", stats)
	}
	if !stats.TextOccurrences.Built || stats.TextOccurrences.Occurrences == 0 || stats.TextOccurrences.BudgetBytes != graphTextOccurrenceBytes {
		t.Fatalf("one-pass occurrence table was not used: %+v", stats.TextOccurrences)
	}
}

func TestGraphOccurrenceTableLifecycle(t *testing.T) {
	dir := t.TempDir()
	writeGraphShards(t, dir, []graphFile{
		{"repo", "target.go", "package demo\nfunc Target() {}\n"},
		{"repo", "caller.go", "package demo\nfunc Caller() { Target() }\n"},
	}, 1)
	s, err := openGraphSweep(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := s.corpus
	first, stats, err := s.computeEdgesParallel([]string{"Target"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.TextOccurrences.Built || s.corpus == base || s.baseCorpus != base {
		t.Fatal("table was not scoped to a corpus view")
	}
	second, _, err := s.computeEdgesParallel([]string{"Target"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("repeated preparation changed graph records")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s.corpus != nil || s.baseCorpus != nil || s.verifierRegions != nil {
		t.Fatal("closed sweep retained occurrence or verification caches")
	}
}

func TestRefreshGraphExtractorVersionInvalidatesRoster(t *testing.T) {
	dir := t.TempDir()
	content := "class Fixture { void Test() { var source = @\"\npublic static void Phantom() {}\n\"; } }\n"
	writeGraphShards(t, dir, []graphFile{{"repo", "fixture.cs", content}}, 1)
	s, err := openGraphSweep(dir)
	if err != nil {
		t.Fatal(err)
	}
	builder := diskgraph.NewBuilder()
	marker := fmt.Sprintf("\x00extractors=%d", symbol.ExtractorsVersion)
	for _, token := range s.identity {
		if !strings.Contains(token, marker) {
			t.Fatalf("roster omits extractor version: %q", token)
		}
		builder.AddCorpusEntry(strings.Replace(token, marker, "", 1))
	}
	s.Close()
	sha := diskstore.GitBlobSHA1([]byte(content))
	phantom := uint64(strings.Index(content, "Phantom"))
	if err := builder.AddEdge(diskgraph.Key{BlobSHA: sha, SymbolOffset: phantom}, diskgraph.Edge{Type: diskgraph.EdgeSiblingDefinition, TargetBlob: sha, TargetOffset: phantom, Name: "Phantom", Confidence: graph.Candidate, Generation: 1, Evidence: graph.Evidence{BlobSHA: sha, ByteOffset: phantom, ByteLength: 7}}); err != nil {
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
		t.Fatalf("old extractor roster was carried forward: %+v", stats)
	}
	for _, record := range readGraph(t, path) {
		if record.edge.Name == "Phantom" {
			t.Fatalf("obsolete false definition edge retained: %+v", record)
		}
	}
	_, again, err := RefreshGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Unchanged {
		t.Fatalf("current extractor roster did not settle: %+v", again)
	}
}

func BenchmarkRawCandidateFanout(b *testing.B) {
	for _, definitions := range []int{100, 1000} {
		b.Run(fmt.Sprintf("definitions=%d", definitions), func(b *testing.B) {
			dir := b.TempDir()
			ix := index.New()
			for i := 0; i < definitions; i++ {
				content := []byte(fmt.Sprintf("package demo\n// %d\nfunc Target() {}\n", i))
				ix.AddFile("repo", fmt.Sprintf("%d.go", i), "", diskstore.GitBlobSHA1(content), content)
			}
			noise := []byte(strings.Repeat("// Target()\n", 100))
			noiseSHA := diskstore.GitBlobSHA1(noise)
			ix.AddFile("repo", "noise.go", "", noiseSHA, noise)
			if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
				b.Fatal(err)
			}
			s, err := openGraphSweep(dir)
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			p := candidates.PrepareName(s.corpus, "Target")
			prototypes := p.SourceCandidates()
			start, end := -1, 0
			for i, source := range prototypes {
				if source.Evidence.BlobSHA == noiseSHA {
					if start < 0 {
						start = i
					}
					end = i + 1
				}
			}
			if start < 0 {
				b.Fatal("no raw sources")
			}
			b.Run("expanded", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					legacyExpandedEdges(s, "Target", p, start, end, 1)
				}
			})
			b.Run("source_first", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, err := s.computeEdgesForPreparedRange("Target", p, start, end, 1); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
