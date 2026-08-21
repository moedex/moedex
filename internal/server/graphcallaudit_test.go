package server

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
)

func TestPatternCallGroupsUseExactEvidenceAndDeterministicTargets(t *testing.T) {
	evidence := graph.Evidence{BlobSHA: "source", ByteOffset: 41, ByteLength: 6}
	edge := func(target string, offset uint64) graphKeyEdge {
		return graphKeyEdge{
			Key: diskgraph.Key{BlobSHA: "source", SymbolOffset: 7},
			Edge: diskgraph.Edge{
				Type: diskgraph.EdgeCalls, TargetBlob: target, TargetOffset: offset,
				Confidence: graph.Pattern, Evidence: evidence, Name: "Target",
			},
		}
	}
	results := []graphNameResult{{Edges: []graphKeyEdge{edge("z", 9), edge("a", 3), edge("z", 9)}}}
	groups := patternCallGroups(results)
	if len(groups) != 1 {
		t.Fatalf("groups = %#v, want one exact call-site group", groups)
	}
	if got, want := groups[0].key, (patternCallSiteKey{sourceBlob: "source", evidence: 41, name: "Target"}); got != want {
		t.Fatalf("key = %#v, want %#v", got, want)
	}
	wantTargets := []diskgraph.Key{{BlobSHA: "a", SymbolOffset: 3}, {BlobSHA: "z", SymbolOffset: 9}}
	if !reflect.DeepEqual(groups[0].targets, wantTargets) {
		t.Fatalf("targets = %#v, want deterministic unique %#v", groups[0].targets, wantTargets)
	}
}

func TestAuditPatternCallsCountsFanoutWithoutWritingGraph(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(t.TempDir(), "repo")
	callerContent := []byte("package fixture\n\nfunc Caller() { Target() }\n")
	targetAContent := []byte("package fixture\n\nfunc Target() {}\n")
	targetBContent := []byte("package fixture\n\nfunc Target(v int) {}\n")
	files := []struct {
		rel     string
		content []byte
	}{
		{"caller.go", callerContent},
		{"target_a.go", targetAContent},
		{"target_b.go", targetBContent},
	}
	ix := index.New()
	for _, file := range files {
		abs := filepath.Join(repo, file.rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, file.content, 0o644); err != nil {
			t.Fatal(err)
		}
		ix.AddFile("repo", file.rel, abs, diskstore.GitBlobSHA1(file.content), file.content)
	}
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}
	// Persist the pure candidate population directly so this audit contract is
	// identical in default and lsp-tagged test binaries. Calling BuildGraph here
	// would intentionally reconcile the fixture when the lsp tag is active.
	sweep, err := openGraphSweep(dir)
	if err != nil {
		t.Fatal(err)
	}
	results, _, err := sweep.computeEdgesParallel(sweep.names, diskgraph.FirstGeneration)
	if err != nil {
		_ = sweep.Close()
		t.Fatal(err)
	}
	builder := diskgraph.NewBuilder()
	sweep.recordCorpusRoster(builder)
	for _, result := range results {
		for _, candidate := range result.Edges {
			if err := builder.AddEdge(candidate.Key, candidate.Edge); err != nil {
				_ = sweep.Close()
				t.Fatal(err)
			}
		}
	}
	if err := sweep.Close(); err != nil {
		t.Fatal(err)
	}
	graphPath := GraphPath(dir)
	if err := builder.Save(graphPath); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(graphPath)
	if err != nil {
		t.Fatal(err)
	}

	report, err := AuditPatternCalls(context.Background(), dir, PatternCallAuditOptions{SampleLimit: 1, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	if report.PatternCallEdges != 2 || report.DistinctCallSites != 1 || report.DistinctDefinitions != 2 {
		t.Fatalf("audit counts = %+v, want two candidates at one site and two definitions", report)
	}
	if report.TargetsPerSite.P50 != 2 || report.TargetsPerSite.P95 != 2 || report.TargetsPerSite.Max != 2 {
		t.Fatalf("target distribution = %+v, want all 2", report.TargetsPerSite)
	}
	if report.ProjectedReferenceRequests != 2 || report.ProjectedDefinitionRequests != 1 {
		t.Fatalf("request projections = references %d definitions %d, want 2 and 1", report.ProjectedReferenceRequests, report.ProjectedDefinitionRequests)
	}
	if report.Contexts.Current != 1 || len(report.Sample) != 1 || report.Sample[0].Targets != 2 {
		t.Fatalf("context/sample census = %+v / %#v", report.Contexts, report.Sample)
	}
	if len(report.Routes) != 1 || report.Routes[0].Language != "go" || report.Routes[0].ProjectedDefinitionRequests != 1 || report.Routes[0].ProjectedReferenceRequests != 2 {
		t.Fatalf("route census = %#v, want one Go route with definition=1 references=2", report.Routes)
	}
	after, err := os.ReadFile(graphPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("audit rewrote the graph artifact")
	}
	if !strings.Contains(report.Sample[0].Name, "Target") {
		t.Fatalf("sample = %#v", report.Sample[0])
	}
}

func TestAuditPatternCallsHonorsCancellationBeforeSweep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := AuditPatternCalls(ctx, t.TempDir(), PatternCallAuditOptions{})
	if err == nil {
		t.Fatal("cancelled audit returned nil error")
	}
}
