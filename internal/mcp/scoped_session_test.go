package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"moedex/internal/contextwin"
	"moedex/internal/graph"
	"moedex/internal/sourcescope"
)

type scopedSessionSearch struct {
	fakeSearcher
	scope sourcescope.Scope
	err   error
}

func (s *scopedSessionSearch) SearchContextScopedWithSnapshot(ctx context.Context, q string, budget, k int, scope sourcescope.Scope) (ContextSearchResult, error) {
	s.scope = scope
	return ContextSearchResult{Window: s.win}, s.err
}

type leasedSearch struct {
	fakeSearcher
	session      ContextSession
	acquisitions int
}

func (s *leasedSearch) AcquireContextSession(context.Context) (ContextSession, error) {
	s.acquisitions++
	return s.session, nil
}

type sessionGraph struct {
	calls int
	err   error
}

func (g *sessionGraph) Neighbors(context.Context, []contextwin.ContextBlock, int) ([]BlockNeighbors, error) {
	g.calls++
	return []BlockNeighbors{NewBlockNeighbors()}, g.err
}
func (g *sessionGraph) NeighborsScopedWithSnapshot(ctx context.Context, blocks []contextwin.ContextBlock, depth int, tier graph.ConfidenceTier, scope sourcescope.Scope) (GraphAnnotationResult, error) {
	n, e := g.Neighbors(ctx, blocks, depth)
	return GraphAnnotationResult{Neighbors: n}, e
}
func TestContextSessionScopeAndRelease(t *testing.T) {
	for _, phase := range []string{"success", "search_error", "graph_error", "unsupported"} {
		t.Run(phase, func(t *testing.T) {
			search := &scopedSessionSearch{fakeSearcher: fakeSearcher{win: contextwin.ContextWindow{Blocks: []contextwin.ContextBlock{{Repo: "chosen", RelPath: "src/a.go", AbsPath: "/new/group/chosen/src/a.go"}}}}}
			g := &sessionGraph{}
			released := 0
			outer := &leasedSearch{session: ContextSession{CorpusRoot: "/new", Searcher: search, Graph: g, Release: func() { released++ }}}
			switch phase {
			case "search_error":
				search.err = errors.New("search failed")
			case "graph_error":
				g.err = errors.New("graph failed")
			case "unsupported":
				outer.session.Searcher = &fakeSearcher{}
			}
			server := NewServer(outer)
			server.corpusRoot = "/old"
			result, err := server.callTool(context.Background(), json.RawMessage(`{"name":"search_context","arguments":{"query":"needle","repo":"chosen","path_prefix":"src","language":"go","format":"structured"}}`))
			if released != 1 || outer.acquisitions != 1 {
				t.Fatalf("lease acquisitions=%d releases=%d", outer.acquisitions, released)
			}
			if phase == "search_error" || phase == "graph_error" {
				if err == nil {
					t.Fatal("expected failure")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if phase == "unsupported" {
				if result["isError"] != true {
					t.Fatal("unsupported scope succeeded")
				}
				return
			}
			payload := result["structuredContent"].(structuredWindow)
			if payload.Blocks[0].PathWithNamespace != "group/chosen" {
				t.Fatalf("namespace used stale root: %+v", payload.Blocks[0])
			}
			if payload.Summary.Scope == nil || *payload.Summary.Scope != search.scope || g.calls != 1 {
				t.Fatalf("scope/graph lost: %+v calls=%d", payload.Summary, g.calls)
			}
			if outer.gotQuery != "" {
				t.Fatal("searched outer holder instead of leased searcher")
			}
		})
	}
}
func TestContextSessionAbsentGraphDoesNotUseServerGraph(t *testing.T) {
	g := &sessionGraph{}
	outer := &leasedSearch{session: ContextSession{Searcher: &fakeSearcher{win: contextwin.ContextWindow{Blocks: []contextwin.ContextBlock{{Text: "x"}}}}}}
	server := NewServer(outer)
	server.graph = g
	if _, err := server.callTool(context.Background(), json.RawMessage(`{"name":"search_context","arguments":{"query":"x"}}`)); err != nil {
		t.Fatal(err)
	}
	if g.calls != 0 {
		t.Fatal("used graph outside leased pair")
	}
}
func TestInvalidScopeDoesNotAcquireSession(t *testing.T) {
	outer := &leasedSearch{}
	result, err := NewServer(outer).callTool(context.Background(), json.RawMessage(`{"name":"search_context","arguments":{"query":"x","path_prefix":"../src"}}`))
	if err != nil || result["isError"] != true || outer.acquisitions != 0 {
		t.Fatalf("invalid scope result=%v err=%v acquisitions=%d", result, err, outer.acquisitions)
	}
}
