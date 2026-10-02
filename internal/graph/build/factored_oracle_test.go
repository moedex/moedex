package graphbuild

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"testing"

	"moedex/internal/graph/candidates"
	"moedex/internal/graph/diskgraph"
)

// Compare the published representation against the original expanded pipeline,
// including emitter deduplication and whole-corpus passes. The oracle never
// constructs compact groups or calls their expansion implementation.
func TestFactoredPublicationMatchesExpandedOracle(t *testing.T) {
	for seed := int64(0); seed < 24; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			var files []graphFile
			repos := []string{"alpha", "beta", ""}
			for n := 0; n < 9; n++ {
				name := []string{"Target", "Factory", "Process"}[rng.Intn(3)]
				content := fmt.Sprintf("package demo\nfunc %s() {}\nfunc Caller%d() { %s(); /* %s() */; _ = `%s()` }\n// %s() outside\n", name, n, name, name, name, name)
				files = append(files, graphFile{repos[rng.Intn(len(repos))], fmt.Sprintf("file%d.go", n), content})
				if n%3 == 0 {
					// Identical source and definition content across shards must not
					// turn physical self-exclusion into content-key exclusion.
					files = append(files, graphFile{repos[rng.Intn(len(repos))], fmt.Sprintf("copy%d.go", n), content})
				}
			}
			files = append(files, graphFile{"alpha", "raw.cs", "public class Fixture {\npublic void Target() {}\npublic void Run() {\nvar s = \"\"\"\"\nembedded \"\"\"\nTarget();\n\"\"\"\";\nTarget();\n}\n}\n"})
			rng.Shuffle(len(files), func(i, j int) { files[i], files[j] = files[j], files[i] })
			dir := t.TempDir()
			writeGraphShards(t, dir, files, 1+rng.Intn(3))
			sweep, err := openGraphSweep(dir)
			if err != nil {
				t.Fatal(err)
			}
			baseline := diskgraph.NewBuilder()
			sweep.recordCorpusRoster(baseline)
			if err := sweep.addDefinitionNodes(baseline); err != nil {
				t.Fatal(err)
			}
			emit := newGraphEmitter(baseline)
			for _, name := range sweep.names {
				prepared := candidates.PrepareName(sweep.corpus, name)
				for _, record := range legacyExpandedEdges(sweep, name, prepared, 0, prepared.NumSources(), diskgraph.FirstGeneration) {
					if err := emit.Add(record.Key, record.Edge); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := sweep.addWholeCorpusEdges(baseline, emit.seen); err != nil {
				t.Fatal(err)
			}
			oracle := filepath.Join(t.TempDir(), "expanded.graph")
			if err := baseline.Save(oracle); err != nil {
				t.Fatal(err)
			}
			if err := sweep.Close(); err != nil {
				t.Fatal(err)
			}
			path, report, err := BuildGraph(dir)
			if err != nil {
				t.Fatal(err)
			}
			want, got := readGraph(t, oracle), readGraph(t, path)
			if report.Edges != uint64(len(got)) || report.Schedule.PhysicalSources == 0 || report.Schedule.LogicalEdges <= report.Schedule.PhysicalSources {
				t.Fatalf("compact build accounting lost: %+v", report)
			}
			if !reflect.DeepEqual(got, want) {
				for i := 0; i < min(len(got), len(want)); i++ {
					if got[i] != want[i] {
						t.Fatalf("record %d changed:\ngot %+v\nwant %+v", i, got[i], want[i])
					}
				}
				t.Fatalf("record count changed: got %d, want %d", len(got), len(want))
			}
			if !reflect.DeepEqual(readGraphKeys(t, path), readGraphKeys(t, oracle)) {
				t.Fatal("source node roster changed")
			}
		})
	}
}
