//go:build lsp

package server

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

func (*concurrentGraphNavigator) References(context.Context, navigate.Pos, bool) ([]navigate.Location, error) {
	return nil, nil
}

func (f *fakeGraphNavigator) DocumentSymbol(_ context.Context, file string) ([]navigate.Symbol, error) {
	return f.documents[cleanAbsolute(file)], nil
}

func (f *fakeGraphNavigator) References(_ context.Context, at navigate.Pos, _ bool) ([]navigate.Location, error) {
	data, err := os.ReadFile(at.File)
	if err != nil {
		return nil, err
	}
	off, ok := byteOffset(data, at)
	if !ok {
		return nil, nil
	}
	name := identifierAtTest(data, off)
	f.order = append(f.order, name)
	return f.reference[name], nil
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

func TestBuildGraphLSPPersistsProvenInterfaceDispatch(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping real LSP call-graph integration test")
	}
	repo := t.TempDir()
	file := filepath.Join(repo, "fixture.go")
	writeLSPGraphFile(t, filepath.Join(repo, "go.mod"), "module example/interfacecalls\n\ngo 1.26\n")
	writeLSPGraphFile(t, file, lspGraphRepoA)

	shardDir := t.TempDir()
	ix := lspGraphIndex("fixture", file, "fixture.go", lspGraphRepoA)
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

	sha := diskstore.GitBlobSHA1([]byte(lspGraphRepoA))
	invoke := uint64(strings.Index(lspGraphRepoA, "Invoke"))
	interfaceDo := uint64(strings.Index(lspGraphRepoA, "Do"))
	callDo := uint64(strings.LastIndex(lspGraphRepoA, "Do"))
	var found bool
	for _, edge := range g.Load(sha, invoke) {
		if edge.Type == diskgraph.EdgeCalls && edge.TargetBlob == sha && edge.TargetOffset == interfaceDo {
			found = true
			if edge.Confidence != graph.Proven || edge.Confidence.Score() != 1.0 {
				t.Fatalf("LSP call confidence = %s / %.2f, want Proven / 1.0", edge.Confidence, edge.Confidence.Score())
			}
			if edge.Name != "Do" || edge.Generation != diskgraph.FirstGeneration {
				t.Fatalf("LSP call provenance = name %q generation %d, want Do at generation %d", edge.Name, edge.Generation, diskgraph.FirstGeneration)
			}
			if edge.Evidence.ByteOffset != callDo || edge.Evidence.ByteLength != 2 {
				t.Fatalf("LSP call evidence = %+v, want Do at %d", edge.Evidence, callDo)
			}
		}
	}
	if !found {
		t.Fatalf("Invoke adjacency = %#v, stats = %+v; want Proven interface-dispatched Do call", g.Load(sha, invoke), stats)
	}
	if stats.ReferenceRequests != stats.DiscoveredSymbols || stats.CallEdges == 0 {
		t.Fatalf("LSP graph coverage stats = %+v", stats)
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
