package servecmd

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"moedex/internal/contextwin"
	graphbuild "moedex/internal/graph/build"
	graphserve "moedex/internal/graph/serve"
	"moedex/internal/mcp"
	server "moedex/internal/serve"
)

// pauseRankResult pauses only after real source retrieval has captured its
// generation. Publication can then deterministically occur before annotation.
type pauseRankResult struct {
	holder          *rankHolder
	once            sync.Once
	started, resume chan struct{}
}

func (p *pauseRankResult) SearchContext(ctx context.Context, query string, budget, topK int) (contextwin.ContextWindow, error) {
	r, err := p.SearchContextWithSnapshot(ctx, query, budget, topK)
	return r.Window, err
}
func (p *pauseRankResult) SearchContextWithSnapshot(ctx context.Context, query string, budget, topK int) (mcp.ContextSearchResult, error) {
	r, err := p.holder.SearchContextWithSnapshot(ctx, query, budget, topK)
	if err != nil {
		return r, err
	}
	p.once.Do(func() {
		close(p.started)
		select {
		case <-p.resume:
		case <-ctx.Done():
			err = ctx.Err()
		}
	})
	return r, err
}

func TestSearchContextRejectsConcurrentRankGraphReload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dirA, dirB := makeShardDir(t, "AlphaMarker"), makeShardDir(t, "BetaMarker")
	for _, dir := range []string{dirA, dirB} {
		if _, _, err := graphbuild.BuildGraph(dir); err != nil {
			t.Fatal(err)
		}
	}
	rankA, err := server.OpenRank(ctx, dirA, server.RankConfig{})
	if err != nil {
		t.Fatal(err)
	}
	holder := newRankHolder(rankA)
	t.Cleanup(func() { holder.cur.retire() })
	graphs, err := graphserve.OpenGraphTools(dirA)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = graphs.Close() })
	paused := &pauseRankResult{holder: holder, started: make(chan struct{}), resume: make(chan struct{})}
	handler := mcp.NewServer(paused, mcp.WithGraphAnnotator(graphs)).HTTPHandler()
	const call = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_context","arguments":{"query":"marker"}}}`
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(call)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	completed := make(chan *httptest.ResponseRecorder, 1)
	go func() { completed <- request() }()
	select {
	case <-paused.started:
	case <-ctx.Done():
		t.Fatal("search never reached publication boundary")
	}
	// Same order as stdio and HTTP production reloads: graph first, rank next.
	if openErr, closeErr := graphs.Reload(dirB); openErr != nil || closeErr != nil {
		t.Fatalf("reload: %v / %v", openErr, closeErr)
	}
	rankB, err := server.OpenRank(ctx, dirB, server.RankConfig{})
	if err != nil {
		t.Fatal(err)
	}
	old := holder.swap(rankB)
	// Reclaim the actual old mappings before the paused answer is rendered.
	// Source text must already be owned, and mixed annotations must be rejected.
	old.retire()
	close(paused.resume)
	var rec *httptest.ResponseRecorder
	select {
	case rec = <-completed:
	case <-ctx.Done():
		t.Fatal("request did not finish after reload")
	}
	decode := func(rec *httptest.ResponseRecorder) struct {
		Result struct {
			IsError    bool                            `json:"isError"`
			Structured map[string]interface{}          `json:"structuredContent"`
			Meta       map[string]mcp.SnapshotIdentity `json:"_meta"`
		} `json:"result"`
	} {
		t.Helper()
		var result struct {
			Result struct {
				IsError    bool                            `json:"isError"`
				Structured map[string]interface{}          `json:"structuredContent"`
				Meta       map[string]mcp.SnapshotIdentity `json:"_meta"`
			} `json:"result"`
		}
		if rec.Code != 200 {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	mixed := decode(rec).Result
	if !mixed.IsError || mixed.Structured["error"].(map[string]interface{})["code"] != "snapshot_unavailable" {
		t.Fatalf("mixed answer=%s", rec.Body.String())
	}
	if identity := mixed.Meta[mcp.SnapshotMetaKey]; identity.Cacheable || !identity.HasCorpusConflict() {
		t.Fatalf("mixed identity=%+v", identity)
	}
	if strings.Contains(rec.Body.String(), "AlphaMarker marker") {
		t.Fatal("mixed answer leaked source")
	}

	// A later request acquires the complete new corpus and graph and succeeds.
	freshRec := request()
	fresh := decode(freshRec).Result
	if fresh.IsError {
		t.Fatalf("fresh request failed: %s", freshRec.Body.String())
	}
	identity := fresh.Meta[mcp.SnapshotMetaKey]
	if !identity.Cacheable || identity.HasCorpusConflict() || identity.GraphBuildID == "" {
		t.Fatalf("fresh identity=%+v", identity)
	}
	if !strings.Contains(freshRec.Body.String(), "BetaMarker") || strings.Contains(freshRec.Body.String(), "AlphaMarker") {
		t.Fatalf("fresh source=%s", freshRec.Body.String())
	}
}
