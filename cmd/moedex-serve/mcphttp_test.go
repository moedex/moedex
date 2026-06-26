package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"moedex/internal/mcp"
	"moedex/internal/server"
)

// newTestMCPHTTPChain builds the same hardened handler runMCPHTTP serves: a real
// ranked corpus behind the rankHolder, the /mcp + /healthz + /metrics mux, wrapped
// by the production chain(...). Tests drive this, not a reimplementation, so they
// exercise the real auth/transport composition.
func newTestMCPHTTPChain(t *testing.T, token string) (http.Handler, *rankHolder) {
	t.Helper()
	dir := makeShardDir(t, "marker") // "marker" is the in-shard content token
	rc, err := server.OpenRank(context.Background(), dir, server.RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("openrank: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	holder := newRankHolder(rc)
	m := newUnpublishedMetrics()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/metrics", rankMetricsHandler(holder, m))
	mux.Handle("/mcp", mcp.NewServer(holder).HTTPHandler())
	return chain(mux, token, 5*time.Second, m), holder
}

func postMCP(t *testing.T, h http.Handler, body, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestMCPHTTPAuthAndRoundTrip is the headline wiring test for the warm shared
// daemon: /mcp is gated behind the bearer token, and an authenticated tools/call
// runs a real ranked search end-to-end and returns the in-shard content.
func TestMCPHTTPAuthAndRoundTrip(t *testing.T) {
	const tok = "s3cret-bearer" // distinct from the shard content marker
	h, _ := newTestMCPHTTPChain(t, tok)

	const list = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	if rec := postMCP(t, h, list, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("/mcp no token: status = %d, want 401", rec.Code)
	}
	if rec := postMCP(t, h, list, "wrong-token"); rec.Code != http.StatusUnauthorized {
		t.Errorf("/mcp wrong token: status = %d, want 401", rec.Code)
	}

	rec := postMCP(t, h, list, tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("/mcp tools/list with token: status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "search_context") {
		t.Errorf("tools/list missing search_context: %s", rec.Body.String())
	}

	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_context","arguments":{"query":"marker","token_budget":2000,"top_k":5}}}`
	rec = postMCP(t, h, call, tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("/mcp tools/call: status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "marker") {
		t.Errorf("tools/call result lacks in-shard marker content: %s", rec.Body.String())
	}

	// /healthz stays open even with a token configured.
	hreq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	hrec := httptest.NewRecorder()
	h.ServeHTTP(hrec, hreq)
	if hrec.Code != http.StatusOK {
		t.Errorf("/healthz: status = %d, want 200", hrec.Code)
	}
}

// TestMCPHTTPMetricsExposeRankGauges confirms /metrics renders the ranked-corpus
// gauges (and stays open without a token).
func TestMCPHTTPMetricsExposeRankGauges(t *testing.T) {
	h, holder := newTestMCPHTTPChain(t, "")
	postMCP(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "") // one observation

	mreq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	mrec := httptest.NewRecorder()
	h.ServeHTTP(mrec, mreq)
	if mrec.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", mrec.Code)
	}
	body := mrec.Body.String()
	for _, want := range []string{
		"moedex_http_requests_total",
		"moedex_corpus_blobs",
		"moedex_corpus_symbol_blobs",
		"moedex_corpus_dense_chunks",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing %q\n%s", want, body)
		}
	}
	snap := holder.acquire()
	defer snap.release()
	if snap.rc.NumBlobs() <= 0 {
		t.Fatalf("test corpus has %d blobs, want >0", snap.rc.NumBlobs())
	}
}
