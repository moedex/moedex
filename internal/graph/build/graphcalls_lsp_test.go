//go:build lsp

package graphbuild

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/graph"
	"moedex/internal/graph/candidates"
	"moedex/internal/graph/diskgraph"
	graphverify "moedex/internal/graph/verify"
	"moedex/internal/index"
	"moedex/internal/navigate"
	"moedex/internal/symbol"
)

const lspGraphRepoA = `package a

func Relevant() {}
func Ordinary() {}

type Runner interface { Do() }

func Invoke(r Runner) { r.Do() }
`

const lspGraphRepoB = `package b

func Use() { Relevant() }
`

type fakeGraphNavigator struct {
	documents map[string][]navigate.Symbol
	reference map[string][]navigate.Location
	order     []string
}

type concurrentGraphNavigator struct {
	started chan struct{}
	release chan struct{}
	active  atomic.Int32
	max     atomic.Int32
}

type statusGraphNavigator struct {
	result navigate.LocationQueryResult
	err    error
}

func (*statusGraphNavigator) DocumentSymbol(context.Context, string) ([]navigate.Symbol, error) {
	return nil, nil
}

func (n *statusGraphNavigator) ReferencesDetailed(context.Context, navigate.Pos, bool) (navigate.LocationQueryResult, error) {
	return n.result, n.err
}

func (f *concurrentGraphNavigator) DocumentSymbol(context.Context, string) ([]navigate.Symbol, error) {
	active := f.active.Add(1)
	for {
		maxSeen := f.max.Load()
		if active <= maxSeen || f.max.CompareAndSwap(maxSeen, active) {
			break
		}
	}
	f.started <- struct{}{}
	<-f.release
	f.active.Add(-1)
	return nil, nil
}

func (*concurrentGraphNavigator) ReferencesDetailed(context.Context, navigate.Pos, bool) (navigate.LocationQueryResult, error) {
	return navigate.LocationQueryResult{Status: navigate.LocationQueryReadyEmpty}, nil
}

func (f *fakeGraphNavigator) DocumentSymbol(_ context.Context, file string) ([]navigate.Symbol, error) {
	return f.documents[cleanAbsolute(file)], nil
}

func (f *fakeGraphNavigator) ReferencesDetailed(_ context.Context, at navigate.Pos, _ bool) (navigate.LocationQueryResult, error) {
	data, err := os.ReadFile(at.File)
	if err != nil {
		return navigate.LocationQueryResult{Status: navigate.LocationQueryUnavailable}, err
	}
	off, ok := byteOffset(data, at)
	if !ok {
		return navigate.LocationQueryResult{Status: navigate.LocationQueryReadyEmpty}, nil
	}
	name := identifierAtTest(data, off)
	f.order = append(f.order, name)
	locations := f.reference[name]
	status := navigate.LocationQueryReadyEmpty
	if len(locations) > 0 {
		status = navigate.LocationQueryResolved
	}
	return navigate.LocationQueryResult{Status: status, Locations: locations}, nil
}

func TestCollectLSPCallEdgesPrioritizesCrossShardAndFindsShortInterfaceCall(t *testing.T) {
	root := t.TempDir()
	repoA := filepath.Join(root, "repo-a")
	repoB := filepath.Join(root, "repo-b")
	fileA := filepath.Join(repoA, "a.go")
	fileB := filepath.Join(repoB, "b.go")
	writeLSPGraphFile(t, filepath.Join(repoA, "go.mod"), "module example/a\n\ngo 1.26\n")
	writeLSPGraphFile(t, filepath.Join(repoB, "go.mod"), "module example/b\n\ngo 1.26\n")
	writeLSPGraphFile(t, fileA, lspGraphRepoA)
	writeLSPGraphFile(t, fileB, lspGraphRepoB)

	shards := []*index.Index{
		lspGraphIndex("repo-a", fileA, "a.go", lspGraphRepoA),
		lspGraphIndex("repo-b", fileB, "b.go", lspGraphRepoB),
	}
	merged := symbol.NewCorpus()
	for i, ix := range shards {
		merged.AddShard(string(rune('a'+i)), symbol.BuildMulti(ix))
	}
	corpus, err := candidates.NewCorpus(merged, shards...)
	if err != nil {
		t.Fatal(err)
	}
	var relevantCross bool
	for _, edge := range candidates.GenerateCandidates(corpus, "Relevant") {
		if edge.CrossShard() {
			relevantCross = true
			break
		}
	}
	if !relevantCross {
		t.Fatal("fixture did not produce the Phase 3 cross-shard Relevant candidate")
	}
	// The default trigram-length regex sweep excludes Do entirely. The LSP pass
	// must still enumerate it from documentSymbol and resolve r.Do().
	if got := candidates.GenerateAll(corpus, candidates.Options{}).ForName("Do"); len(got) != 0 {
		t.Fatalf("regex candidate sweep unexpectedly covered short name Do: %#v", graphverify.Verify(got))
	}

	fake := &fakeGraphNavigator{
		documents: map[string][]navigate.Symbol{
			cleanAbsolute(fileA): testDocumentSymbols(fileA, lspGraphRepoA, "Relevant", "Ordinary", "Runner", "Do", "Invoke"),
			cleanAbsolute(fileB): testDocumentSymbols(fileB, lspGraphRepoB, "Use"),
		},
		reference: map[string][]navigate.Location{
			"Do": {testLocation(fileA, lspGraphRepoA, strings.LastIndex(lspGraphRepoA, "Do"), len("Do"))},
		},
	}
	calls, stats, err := collectLSPCallEdges(
		context.Background(), fake, merged, shards, map[string]bool{"Relevant": true}, time.Second, newLSPRequestPacer(1e9),
	)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ReferenceRequests != stats.DiscoveredSymbols || stats.DiscoveredSymbols != 6 {
		t.Fatalf("coverage stats = %+v, want find_references for all 6 exported symbols", stats)
	}
	if len(fake.order) != 6 || fake.order[0] != "Relevant" {
		t.Fatalf("find_references order = %v, want cross-shard Relevant first and all symbols queried", fake.order)
	}
	if len(calls) != 1 {
		t.Fatalf("confirmed calls = %#v, want one interface-dispatched Do call", calls)
	}
	wantSource := uint64(strings.Index(lspGraphRepoA, "Invoke"))
	wantTarget := uint64(strings.Index(lspGraphRepoA, "Do"))
	wantEvidence := uint64(strings.LastIndex(lspGraphRepoA, "Do"))
	if calls[0].source.SymbolOffset != wantSource || calls[0].target.SymbolOffset != wantTarget || calls[0].evidence.ByteOffset != wantEvidence {
		t.Fatalf("interface call = %+v, want Invoke:%d -> interface Do:%d with evidence %d", calls[0], wantSource, wantTarget, wantEvidence)
	}
}

func TestLSPRequestPacerSpacesStarts(t *testing.T) {
	now := time.Unix(100, 0)
	var slept []time.Duration
	pacer := &lspRequestPacer{
		interval: 100 * time.Millisecond,
		now:      func() time.Time { return now },
		sleep: func(_ context.Context, delay time.Duration) error {
			slept = append(slept, delay)
			now = now.Add(delay)
			return nil
		},
	}
	for range 3 {
		if err := pacer.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if want := []time.Duration{100 * time.Millisecond, 100 * time.Millisecond}; !reflect.DeepEqual(slept, want) {
		t.Fatalf("pacer sleeps = %v, want %v", slept, want)
	}
}

func TestLSPReferenceRouteStopsAfterUnavailableResult(t *testing.T) {
	jobs := []lspDefinitionJob{
		{name: "One", repoRoot: "/repo", route: "go\x00/repo", at: navigate.Pos{File: "/repo/a.go", Line: 1, Col: 1}},
		{name: "Two", repoRoot: "/repo", route: "go\x00/repo", at: navigate.Pos{File: "/repo/a.go", Line: 2, Col: 1}},
		{name: "Three", repoRoot: "/repo", route: "go\x00/repo", at: navigate.Pos{File: "/repo/a.go", Line: 3, Col: 1}},
	}
	result := collectLSPReferenceGroup(
		context.Background(),
		&statusGraphNavigator{result: navigate.LocationQueryResult{Status: navigate.LocationQueryUnavailable}},
		jobs, nil, nil, time.Second, newLSPRequestPacer(1e9), &lspPhaseCounters{},
	)
	if result.requests != 1 || result.unavailable != 1 || result.failed != 1 || result.skipped != 2 {
		t.Fatalf("route circuit breaker = %+v, want one failed request and two skipped", result)
	}
}

func TestCollectLSPCallEdgesRunsIndependentWorkspacesConcurrently(t *testing.T) {
	root := t.TempDir()
	var shards []*index.Index
	merged := symbol.NewCorpus()
	for i, repo := range []string{"repo-a", "repo-b"} {
		repoRoot := filepath.Join(root, repo)
		file := filepath.Join(repoRoot, "main.go")
		writeLSPGraphFile(t, filepath.Join(repoRoot, "go.mod"), "module example/"+repo+"\n\ngo 1.26\n")
		writeLSPGraphFile(t, file, lspGraphRepoA)
		ix := lspGraphIndex(repo, file, "main.go", lspGraphRepoA)
		shards = append(shards, ix)
		merged.AddShard(string(rune('a'+i)), symbol.BuildMulti(ix))
	}
	fake := &concurrentGraphNavigator{
		started: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := collectLSPCallEdgesConcurrent(
			context.Background(), fake, merged, shards, nil, time.Second,
			newLSPRequestPacer(1e9), 2,
		)
		done <- err
	}()
	for range 2 {
		select {
		case <-fake.started:
		case <-time.After(time.Second):
			close(fake.release)
			t.Fatal("independent workspace requests did not overlap")
		}
	}
	close(fake.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := fake.max.Load(); got != 2 {
		t.Fatalf("max concurrent workspace requests = %d, want 2", got)
	}
}

func TestCollectLSPCallEdgesSerializesOneWorkspaceRoute(t *testing.T) {
	repo := t.TempDir()
	writeLSPGraphFile(t, filepath.Join(repo, "go.mod"), "module example/one\n\ngo 1.26\n")
	ix := index.New()
	for _, name := range []string{"a.go", "b.go"} {
		file := filepath.Join(repo, name)
		writeLSPGraphFile(t, file, lspGraphRepoA)
		ix.AddFile("repo", name, file, diskstore.GitBlobSHA1([]byte(lspGraphRepoA)), []byte(lspGraphRepoA))
	}
	merged := symbol.NewCorpus()
	merged.AddShard("repo", symbol.BuildMulti(ix))
	fake := &concurrentGraphNavigator{
		started: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := collectLSPCallEdgesConcurrent(
			context.Background(), fake, merged, []*index.Index{ix}, nil, time.Second,
			newLSPRequestPacer(1e9), 2,
		)
		done <- err
	}()
	select {
	case <-fake.started:
	case <-time.After(time.Second):
		close(fake.release)
		t.Fatal("first workspace request did not start")
	}
	select {
	case <-fake.started:
		close(fake.release)
		t.Fatal("same workspace route ran two requests concurrently")
	case <-time.After(100 * time.Millisecond):
	}
	close(fake.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := fake.max.Load(); got != 1 {
		t.Fatalf("max concurrent requests for one route = %d, want 1", got)
	}
}

func TestLSPGraphPrivacyPreflightRejectsWorkspaceBeforeRequests(t *testing.T) {
	repo := t.TempDir()
	file := filepath.Join(repo, "fixture.go")
	writeLSPGraphFile(t, file, lspGraphRepoA)
	writeLSPGraphFile(t, filepath.Join(repo, ".ai-privacy.yml"), `global_privacy_level: 3
privacy_levels:
  - path: /secret/
    privacy_level: 1
`)
	ix := lspGraphIndex("fixture", file, "fixture.go", lspGraphRepoA)
	merged := symbol.NewCorpus()
	merged.AddShard("fixture", symbol.BuildMulti(ix))
	fake := &fakeGraphNavigator{}
	calls, stats, err := collectLSPCallEdges(context.Background(), fake, merged, []*index.Index{ix}, nil, time.Second, newLSPRequestPacer(1e9))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 || stats.RestrictedWorkspaces != 1 || stats.DocumentRequests != 0 || stats.ReferenceRequests != 0 {
		t.Fatalf("restricted sweep = calls %#v, stats %+v; want rejection before requests", calls, stats)
	}
}

func TestReconcilePatternCallGroupsPrunesCompleteExactPositive(t *testing.T) {
	group := testPatternCallGroup("source", 17, "Resolve", []diskgraph.Key{
		{BlobSHA: "target-a", SymbolOffset: 3},
		{BlobSHA: "target-b", SymbolOffset: 5},
	})
	exact := []lspConfirmedCall{
		{source: group.source, target: group.targets[0], name: group.key.name, evidence: group.edges[0].Edge.Evidence, repoRoot: "/repo"},
		{source: group.source, target: group.targets[1], name: group.key.name, evidence: group.edges[0].Edge.Evidence, repoRoot: "/repo"},
	}
	authoritative := map[lspTargetContext]struct{}{
		{repoRoot: "/repo", target: group.targets[0]}: {},
		{repoRoot: "/repo", target: group.targets[1]}: {},
	}
	reconciled := reconcilePatternCallGroups(
		[]patternCallGroup{group},
		lspCallCollection{calls: exact, authoritative: authoritative},
		map[string]int{"source": 1},
	)
	if _, ok := reconciled.pruneSites[group.key]; !ok {
		t.Fatalf("prune sites = %#v, want exact group", reconciled.pruneSites)
	}
	if reconciled.stats.ReconciledCallGroups != 1 || reconciled.stats.PrunedPatternEdges != 2 {
		t.Fatalf("stats = %+v, want one reconciled group and two pruned siblings", reconciled.stats)
	}
}

func TestReconcilePatternCallGroupsRetainsPartialAndMultiRepoFanout(t *testing.T) {
	tests := []struct {
		name     string
		contexts int
		auth     bool
	}{
		{name: "partial target coverage", contexts: 1, auth: false},
		{name: "content shared across repositories", contexts: 2, auth: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group := testPatternCallGroup("shared-source", 23, "Resolve", []diskgraph.Key{
				{BlobSHA: "target-a", SymbolOffset: 3},
				{BlobSHA: "target-b", SymbolOffset: 5},
			})
			exact := lspConfirmedCall{
				source: group.source, target: group.targets[0], name: group.key.name,
				evidence: group.edges[0].Edge.Evidence, repoRoot: "/repo",
			}
			authoritative := map[lspTargetContext]struct{}{
				{repoRoot: "/repo", target: group.targets[0]}: {},
			}
			if tt.auth {
				authoritative[lspTargetContext{repoRoot: "/repo", target: group.targets[1]}] = struct{}{}
			}
			reconciled := reconcilePatternCallGroups(
				[]patternCallGroup{group},
				lspCallCollection{calls: []lspConfirmedCall{exact}, authoritative: authoritative},
				map[string]int{"shared-source": tt.contexts},
			)
			if _, ok := reconciled.pruneSites[group.key]; ok {
				t.Fatalf("uncertain group was pruned: %+v", reconciled)
			}
			if len(reconciled.calls) != 1 || reconciled.stats.RetainedUncertainCallGroups != 1 {
				t.Fatalf("reconciliation = %+v, want Pattern retention plus exact Proven call", reconciled)
			}
		})
	}
}

func TestLSPDefinitionJobsForGroupsScopesIncrementalReferences(t *testing.T) {
	repo := t.TempDir()
	file := filepath.Join(repo, "fixture.go")
	writeLSPGraphFile(t, file, lspGraphRepoA)
	ix := lspGraphIndex("fixture", file, "fixture.go", lspGraphRepoA)
	merged := symbol.NewCorpus()
	merged.AddShard("fixture", symbol.BuildMulti(ix))
	files, _, err := lspEligibleFiles(merged, []*index.Index{ix})
	if err != nil {
		t.Fatal(err)
	}
	definition := merged.Definitions("Relevant")[0]
	group := testPatternCallGroup("source", 10, "Relevant", []diskgraph.Key{{
		BlobSHA: ix.Blob(definition.Blob).SHA, SymbolOffset: uint64(definition.Start),
	}})
	// The call source shares the same repository context as the target.
	group.key.sourceBlob = ix.Blob(definition.Blob).SHA
	jobs := lspDefinitionJobsForGroups(files, []patternCallGroup{group}, nil)
	if len(jobs) != 1 {
		t.Fatalf("dirty-name jobs = %#v, want only Relevant", jobs)
	}
	for _, job := range jobs {
		if job.name != "Relevant" {
			t.Fatalf("job = %+v, want Relevant only", job)
		}
	}
}

func testPatternCallGroup(sourceBlob string, evidenceOffset uint64, name string, targets []diskgraph.Key) patternCallGroup {
	group := patternCallGroup{
		key:     patternCallSiteKey{sourceBlob: sourceBlob, evidence: evidenceOffset, name: name},
		source:  diskgraph.Key{BlobSHA: sourceBlob, SymbolOffset: 2},
		targets: append([]diskgraph.Key(nil), targets...),
	}
	for _, target := range targets {
		group.edges = append(group.edges, graphKeyEdge{
			Key: group.source,
			Edge: diskgraph.Edge{
				Type: diskgraph.EdgeCalls, TargetBlob: target.BlobSHA, TargetOffset: target.SymbolOffset,
				Confidence: graph.Pattern, Name: name,
				Evidence: graph.Evidence{BlobSHA: sourceBlob, ByteOffset: evidenceOffset, ByteLength: uint64(len(name))},
			},
		})
	}
	return group
}

func TestBuildGraphLSPReconcilesPatternCallWithoutDocumentSweep(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping real LSP call-graph integration test")
	}
	const source = `package fixture

func Target() {}
func Caller() { Target() }
`
	repo := t.TempDir()
	file := filepath.Join(repo, "fixture.go")
	writeLSPGraphFile(t, filepath.Join(repo, "go.mod"), "module example/interfacecalls\n\ngo 1.26\n")
	writeLSPGraphFile(t, file, source)

	shardDir := t.TempDir()
	ix := lspGraphIndex("fixture", file, "fixture.go", source)
	if err := diskstore.Save(ix, filepath.Join(shardDir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}
	var stats LSPGraphStats
	path, _, err := BuildGraphWithOptions(shardDir, GraphBuildOptions{
		LSPRequestsPerSecond: 1000,
		LSPRequestTimeout:    30 * time.Second,
		LSPStats:             &stats,
	})
	if err != nil {
		t.Fatal(err)
	}
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	sha := diskstore.GitBlobSHA1([]byte(source))
	caller := uint64(strings.Index(source, "Caller"))
	target := uint64(strings.Index(source, "Target"))
	callTarget := uint64(strings.LastIndex(source, "Target"))
	var found bool
	for _, edge := range g.Load(sha, caller) {
		if edge.Type == diskgraph.EdgeCalls && edge.TargetBlob == sha && edge.TargetOffset == target {
			found = true
			if edge.Confidence != graph.Proven || edge.Confidence.Score() != 1.0 {
				t.Fatalf("LSP call confidence = %s / %.2f, want Proven / 1.0", edge.Confidence, edge.Confidence.Score())
			}
			if edge.Name != "Target" || edge.Generation != diskgraph.FirstGeneration {
				t.Fatalf("LSP call provenance = name %q generation %d, want Target at generation %d", edge.Name, edge.Generation, diskgraph.FirstGeneration)
			}
			if edge.Evidence.ByteOffset != callTarget || edge.Evidence.ByteLength != uint64(len("Target")) {
				t.Fatalf("LSP call evidence = %+v, want Target at %d", edge.Evidence, callTarget)
			}
		}
	}
	if !found {
		t.Fatalf("Caller adjacency = %#v, stats = %+v; want Proven Target call", g.Load(sha, caller), stats)
	}
	if stats.DocumentRequests != 0 || stats.ReferenceRequests != 1 || stats.CallEdges != 1 || stats.PrunedPatternEdges != 1 {
		t.Fatalf("LSP graph coverage stats = %+v", stats)
	}
	statusTotal := stats.ResolvedReferenceRequests + stats.EmptyReferenceRequests + stats.UnsupportedReferenceRequests + stats.UnavailableReferenceRequests
	if statusTotal != stats.ReferenceRequests || stats.ReferenceLatencyTotal <= 0 || stats.ReferenceLatencyMax <= 0 {
		t.Fatalf("LSP request status/latency accounting = %+v", stats)
	}
}

func TestBuildGraphLSPCancellationDoesNotPublishGraph(t *testing.T) {
	repo := t.TempDir()
	file := filepath.Join(repo, "fixture.go")
	writeLSPGraphFile(t, filepath.Join(repo, "go.mod"), "module example/cancelled\n\ngo 1.26\n")
	writeLSPGraphFile(t, file, lspGraphRepoA)
	shardDir := t.TempDir()
	if err := diskstore.Save(lspGraphIndex("fixture", file, "fixture.go", lspGraphRepoA), filepath.Join(shardDir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := BuildGraphWithOptions(shardDir, GraphBuildOptions{Context: ctx}); err == nil {
		t.Fatal("cancelled LSP graph build returned nil error")
	}
	if _, err := os.Stat(GraphPath(shardDir)); !os.IsNotExist(err) {
		t.Fatalf("cancelled build published graph: stat error %v", err)
	}
}

func TestRefreshGraphLSPReconcilesDirtyNameWithoutDocumentSweep(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping real LSP refresh integration test")
	}
	repo := t.TempDir()
	file := filepath.Join(repo, "fixture.go")
	writeLSPGraphFile(t, filepath.Join(repo, "go.mod"), "module example/refreshcalls\n\ngo 1.26\n")
	initial := "package fixture\n\nfunc Target() {}\nfunc Caller() { Target() }\n"
	writeLSPGraphFile(t, file, initial)
	shardDir := t.TempDir()
	shardPath := filepath.Join(shardDir, "shard-0000.idx")
	if err := diskstore.Save(lspGraphIndex("fixture", file, "fixture.go", initial), shardPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := BuildGraphWithOptions(shardDir, GraphBuildOptions{LSPRequestsPerSecond: 1000}); err != nil {
		t.Fatal(err)
	}

	updated := "package fixture\n\nfunc Target() {}\nfunc Caller() {\n\tTarget()\n}\n"
	writeLSPGraphFile(t, file, updated)
	if err := diskstore.Save(lspGraphIndex("fixture", file, "fixture.go", updated), shardPath); err != nil {
		t.Fatal(err)
	}
	var lspStats LSPGraphStats
	path, stats, err := RefreshGraphWithOptions(shardDir, GraphBuildOptions{
		LSPRequestsPerSecond: 1000,
		LSPStats:             &lspStats,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.FullRebuild || stats.NamesRecomputed == 0 {
		t.Fatalf("refresh stats = %+v, want dirty-name delta", stats)
	}
	if lspStats.DocumentRequests != 0 || lspStats.ReferenceRequests == 0 {
		t.Fatalf("targeted LSP stats = %+v, want references without document sweep", lspStats)
	}
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	sha := diskstore.GitBlobSHA1([]byte(updated))
	caller := uint64(strings.Index(updated, "Caller"))
	target := uint64(strings.Index(updated, "Target"))
	var calls []diskgraph.Edge
	for _, edge := range g.Load(sha, caller) {
		if edge.Type == diskgraph.EdgeCalls && edge.Name == "Target" {
			calls = append(calls, edge)
		}
	}
	if len(calls) != 1 || calls[0].Confidence != graph.Proven || calls[0].TargetOffset != target {
		t.Fatalf("dirty Caller calls = %#v, want one reconciled Proven Target", calls)
	}
}

func lspGraphIndex(repo, abs, rel, content string) *index.Index {
	ix := index.New()
	ix.AddFile(repo, rel, abs, diskstore.GitBlobSHA1([]byte(content)), []byte(content))
	return ix
}

func writeLSPGraphFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testDocumentSymbols(file, content string, names ...string) []navigate.Symbol {
	out := make([]navigate.Symbol, 0, len(names))
	from := 0
	for _, name := range names {
		rel := strings.Index(content[from:], name)
		if rel < 0 {
			panic("test symbol not found: " + name)
		}
		off := from + rel
		out = append(out, navigate.Symbol{Name: name, Kind: "Function", Loc: testLocation(file, content, off, len(name))})
		from = off + len(name)
	}
	return out
}

func testLocation(file, content string, offset, length int) navigate.Location {
	return navigate.Location{
		File:  file,
		Start: byteOffsetPos(file, []byte(content), offset),
		End:   byteOffsetPos(file, []byte(content), offset+length),
	}
}

func identifierAtTest(content []byte, offset int) string {
	end := offset
	for end < len(content) && lspIdentifierByte(content[end]) {
		end++
	}
	return string(content[offset:end])
}
