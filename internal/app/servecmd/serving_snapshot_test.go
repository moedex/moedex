package servecmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"moedex/internal/contextwin"
	"moedex/internal/graph"
	graphbuild "moedex/internal/graph/build"
	graphserve "moedex/internal/graph/serve"
	"moedex/internal/mcp"
	server "moedex/internal/serve"
)

func servingFixture(t *testing.T, marker string) (string, *servingHolder) {
	t.Helper()
	dir := makeShardDir(t, marker)
	if _, _, err := graphbuild.BuildGraph(dir); err != nil {
		t.Fatal(err)
	}
	rank, err := server.OpenRank(context.Background(), dir, server.RankConfig{})
	if err != nil {
		t.Fatal(err)
	}
	graphs, err := openServingGraph(dir)
	if err != nil {
		_ = rank.Close()
		t.Fatal(err)
	}
	h, err := newServingHolder(rank, graphs)
	if err != nil {
		_ = closeServingComponents(graphs, rank)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := h.Close(); err != nil {
			t.Error(err)
		}
	})
	return dir, h
}

func awaitServingPublication(t *testing.T, ctx context.Context, h *servingHolder, old *server.RankCorpus) {
	t.Helper()
	for {
		s, release, err := h.acquire(ctx)
		if err != nil {
			t.Fatalf("waiting for publication: %v", err)
		}
		changed := s.rank != old
		release()
		if changed {
			return
		}
		runtime.Gosched()
	}
}

func TestServingSnapshotLeaseSurvivesAtomicReload(t *testing.T) {
	_, h := servingFixture(t, "AlphaMarker")
	dirB, unused := servingFixture(t, "BetaMarker")
	if err := unused.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lease, err := h.AcquireContextSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lease.Release)
	oldRank := lease.Searcher.(*server.RankCorpus)
	oldGraph := lease.Graph.(*graphserve.GraphToolset)
	source, err := oldRank.SearchContextWithSnapshot(ctx, "marker", 1000, 5)
	if err != nil || len(source.Window.Blocks) == 0 {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	done := make(chan error, 1)
	go func() {
		openErr, closeErr := h.Reload(ctx, dirB, server.RankConfig{})
		done <- errors.Join(openErr, closeErr)
	}()
	awaitServingPublication(t, ctx, h, oldRank)
	select {
	case err := <-done:
		t.Fatalf("retired while lease held: %v", err)
	default:
	}
	if !oldGraph.Available() {
		t.Fatal("old graph closed before lease release")
	}
	annotations, err := oldGraph.NeighborsWithSnapshot(ctx, source.Window.Blocks, 1, graph.DefaultMinConfidence)
	if err != nil {
		t.Fatal(err)
	}
	if source.Snapshot.CorpusFingerprint != annotations.Snapshot.CorpusFingerprint {
		t.Fatalf("lease mixed generations: %+v / %+v", source.Snapshot, annotations.Snapshot)
	}
	fresh, err := h.SearchContextWithSnapshot(ctx, "marker", 1000, 5)
	if err != nil || len(fresh.Window.Blocks) == 0 || !strings.Contains(fresh.Window.Blocks[0].Text, "BetaMarker") {
		t.Fatalf("fresh=%+v err=%v", fresh, err)
	}
	lease.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("retirement did not drain")
	}
	if oldGraph.Available() {
		t.Fatal("retired graph still open")
	}
}

func TestServingToolWrapperFollowsPublishedPair(t *testing.T) {
	_, h := servingFixture(t, "AlphaMarker")
	dirB, unused := servingFixture(t, "BetaMarker")
	_ = unused.Close()
	var reader mcp.ToolHandler
	for _, tool := range h.Tools() {
		if tool.Name() == "read_source" {
			reader = tool
		}
	}
	if reader == nil {
		t.Fatal("missing source tool")
	}
	read := func() string {
		result, err := reader.Call(context.Background(), json.RawMessage(`{"repo":"repo","path":"x.go"}`))
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(result)
		return string(data)
	}
	if got := read(); !strings.Contains(got, "AlphaMarker") {
		t.Fatal(got)
	}
	if openErr, closeErr := h.Reload(context.Background(), dirB, server.RankConfig{}); openErr != nil || closeErr != nil {
		t.Fatalf("reload: %v / %v", openErr, closeErr)
	}
	if got := read(); !strings.Contains(got, "BetaMarker") || strings.Contains(got, "AlphaMarker") {
		t.Fatal(got)
	}
}

func TestServingReloadFailureRetainsCompletePair(t *testing.T) {
	_, h := servingFixture(t, "AlphaMarker")
	old, release, err := h.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	badGraph := makeShardDir(t, "BetaMarker")
	if err := os.WriteFile(server.GraphPath(badGraph), []byte("broken graph"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(t.TempDir(), "absent"), badGraph} {
		if openErr, _ := h.Reload(context.Background(), dir, server.RankConfig{}); openErr == nil {
			t.Fatalf("accepted incomplete generation %s", dir)
		}
		current, release, err := h.acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if current != old {
			t.Error("failed opening replaced the pair")
		}
		release()
	}
	// A genuinely absent optional graph publishes a new source-only pair, never
	// the old graph attached to a newer source corpus.
	noGraph := makeShardDir(t, "GammaMarker")
	if openErr, closeErr := h.Reload(context.Background(), noGraph, server.RankConfig{}); openErr != nil || closeErr != nil {
		t.Fatalf("source-only reload: %v / %v", openErr, closeErr)
	}
	current, release, err := h.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if current == old || current.graph.Available() {
		t.Fatal("source-only reload retained previous graph")
	}
}

func TestServingCloseAndCanceledAcquisition(t *testing.T) {
	_, h := servingFixture(t, "AlphaMarker")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.AcquireContextSession(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquisition=%v", err)
	}
	if openErr, _ := h.Reload(ctx, "ignored", server.RankConfig{}); !errors.Is(openErr, context.Canceled) {
		t.Fatalf("canceled reload=%v", openErr)
	}
	lease, err := h.AcquireContextSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lease.Release)
	done := make(chan error, 1)
	go func() { done <- h.Close() }()
	deadline, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	for {
		_, release, err := h.acquire(deadline)
		if errors.Is(err, errServingClosed) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		release()
		runtime.Gosched()
	}
	select {
	case err := <-done:
		t.Fatalf("close returned with active lease: %v", err)
	default:
	}
	lease.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-deadline.Done():
		t.Fatal("close did not drain")
	}
	if _, err := h.AcquireContextSession(context.Background()); !errors.Is(err, errServingClosed) {
		t.Fatalf("closed acquisition=%v", err)
	}
	if openErr, _ := h.Reload(context.Background(), "ignored", server.RankConfig{}); !errors.Is(openErr, errServingClosed) {
		t.Fatalf("closed reload=%v", openErr)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}

type closingFailure struct {
	err   error
	calls int
}

func (c *closingFailure) Close() error { c.calls++; return c.err }
func TestServingClosePreservesBothErrors(t *testing.T) {
	graphErr, rankErr := errors.New("graph close"), errors.New("rank close")
	g, r := &closingFailure{err: graphErr}, &closingFailure{err: rankErr}
	err := closeServingComponents(g, r)
	if !errors.Is(err, graphErr) || !errors.Is(err, rankErr) || g.calls != 1 || r.calls != 1 {
		t.Fatalf("close=%v calls=%d/%d", err, g.calls, r.calls)
	}
}

// Pausing inside a leased search forces publication between retrieval and
// annotation through the real MCP dispatch, rather than testing holder calls only.
type pausedServingProvider struct {
	*servingHolder
	started, resume chan struct{}
}

func (p *pausedServingProvider) AcquireContextSession(ctx context.Context) (mcp.ContextSession, error) {
	s, err := p.servingHolder.AcquireContextSession(ctx)
	if err != nil {
		return s, err
	}
	s.Searcher = &pausedSessionSearch{searcher: s.Searcher, started: p.started, resume: p.resume}
	return s, nil
}

type pausedSessionSearch struct {
	searcher        mcp.ContextSearcher
	started, resume chan struct{}
}

func (p *pausedSessionSearch) SearchContext(ctx context.Context, q string, b, k int) (contextwin.ContextWindow, error) {
	r, err := p.SearchContextWithSnapshot(ctx, q, b, k)
	return r.Window, err
}
func (p *pausedSessionSearch) SearchContextWithSnapshot(ctx context.Context, q string, b, k int) (mcp.ContextSearchResult, error) {
	r, err := p.searcher.(mcp.SnapshotContextSearcher).SearchContextWithSnapshot(ctx, q, b, k)
	if err != nil {
		return r, err
	}
	close(p.started)
	select {
	case <-p.resume:
		return r, nil
	case <-ctx.Done():
		return mcp.ContextSearchResult{}, ctx.Err()
	}
}

func TestMCPServingLeaseKeepsSourceAndGraphCoherentAcrossReload(t *testing.T) {
	_, h := servingFixture(t, "AlphaMarker")
	dirB, unused := servingFixture(t, "BetaMarker")
	_ = unused.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	old, release, err := h.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release()
	p := &pausedServingProvider{servingHolder: h, started: make(chan struct{}), resume: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-p.resume:
		default:
			close(p.resume)
		}
	})
	handler := mcp.NewServer(p, mcp.WithTools(h.Tools()...)).HTTPHandler()
	response := make(chan string, 1)
	go func() {
		rec := postMCP(t, handler, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_context","arguments":{"query":"marker"}}}`, "")
		response <- rec.Body.String()
	}()
	select {
	case <-p.started:
	case <-ctx.Done():
		t.Fatal("request did not acquire lease")
	}
	done := make(chan error, 1)
	go func() { a, b := h.Reload(ctx, dirB, server.RankConfig{}); done <- errors.Join(a, b) }()
	awaitServingPublication(t, ctx, h, old.rank)
	close(p.resume)
	select {
	case body := <-response:
		if strings.Contains(body, `"isError":true`) || !strings.Contains(body, "AlphaMarker") || strings.Contains(body, "BetaMarker") || strings.Contains(body, "graph_corpus_fingerprint") {
			t.Fatalf("mixed lease answer: %s", body)
		}
		if !strings.Contains(body, "graph_build_id") {
			t.Fatalf("annotations were skipped: %s", body)
		}
	case <-ctx.Done():
		t.Fatal("request did not finish")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("reload did not drain")
	}
}

func TestServingRejectsMismatchedPreparedPair(t *testing.T) {
	_, h := servingFixture(t, "AlphaMarker")
	_, other := servingFixture(t, "BetaMarker")
	a, releaseA, err := h.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()
	b, releaseB, err := other.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer releaseB()
	// Model refresh racing the two component opens without an on-disk timing
	// dependency: both components are valid, but describe different sources.
	if _, err := newServingHolder(a.rank, b.graph); err == nil {
		t.Fatal("boot accepted mismatched prepared components")
	}
	h.lifecycle.Lock()
	old, err := h.publish(newServingSnapshot(b.rank, a.graph))
	h.lifecycle.Unlock()
	if err == nil || old != nil {
		t.Fatalf("published mismatch: old=%v err=%v", old, err)
	}
	current, release, err := h.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if current != a {
		t.Fatal("preparation mismatch replaced the complete serving pair")
	}
}

func TestSourceOnlyServingLeaseAndTransitions(t *testing.T) {
	_, h := servingFixture(t, "GraphMarker")
	source := makeShardDir(t, "SourceMarker")
	if a, b := h.Reload(context.Background(), source, server.RankConfig{}); a != nil || b != nil {
		t.Fatalf("%v / %v", a, b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	old, release, err := h.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if old.graph.Available() || old.graph.CorpusFingerprint() == "" || old.graph.CorpusFingerprint() != old.rank.CorpusFingerprint() {
		t.Fatal("source identity mismatch")
	}
	read := func(g *graphserve.GraphToolset, want string) {
		t.Helper()
		for _, tool := range g.Tools() {
			if tool.Name() == "read_source" {
				r, e := tool.Call(ctx, json.RawMessage(`{"repo":"repo","path":"x.go"}`))
				b, _ := json.Marshal(r)
				if e != nil || r["isError"] == true || !strings.Contains(string(b), want) {
					t.Fatalf("%s %v", b, e)
				}
				return
			}
		}
		t.Fatal("no read_source")
	}
	read(old.graph, "SourceMarker")
	next := makeShardDir(t, "NextMarker")
	done := make(chan error, 1)
	go func() { a, b := h.Reload(ctx, next, server.RankConfig{}); done <- errors.Join(a, b) }()
	awaitServingPublication(t, ctx, h, old.rank)
	read(old.graph, "SourceMarker")
	select {
	case err := <-done:
		t.Fatalf("closed leased source: %v", err)
	default:
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("retirement blocked")
	}
	graphDir, unused := servingFixture(t, "FinalGraphMarker")
	_ = unused.Close()
	if a, b := h.Reload(ctx, graphDir, server.RankConfig{}); a != nil || b != nil {
		t.Fatalf("%v / %v", a, b)
	}
	final, finish, err := h.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	if !final.graph.Available() || final.graph.CorpusFingerprint() != final.rank.CorpusFingerprint() {
		t.Fatal("graph transition identity mismatch")
	}
	read(final.graph, "FinalGraphMarker")
}
