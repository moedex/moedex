package servecmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"moedex/internal/contextwin"
	"moedex/internal/mcp"
	server "moedex/internal/serve"
)

// newTestMCPHTTPChain builds the same hardened handler runMCPHTTP serves: a real
// ranked corpus behind the rankHolder, the /mcp + /healthz + /metrics mux (with
// /mcp bounded by withConcurrencyLimit exactly as runMCPHTTP wires it), wrapped
// by the production chain(...). Tests drive this, not a reimplementation, so they
// exercise the real auth/transport/concurrency composition.
func newTestMCPHTTPChain(t *testing.T, token string, mcpMaxConcurrency int) (http.Handler, *rankHolder, *metrics) {
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
	mux.Handle("/mcp", withConcurrencyLimit(mcp.NewServer(holder).HTTPHandler(), mcpMaxConcurrency, m.mcpRejected, "too many concurrent mcp requests"))
	return chain(mux, token, 5*time.Second, m), holder, m
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
	h, _, _ := newTestMCPHTTPChain(t, tok, defaultMCPMaxConcurrency)

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
	h, holder, _ := newTestMCPHTTPChain(t, "", defaultMCPMaxConcurrency)
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
		"moedex_http_mcp_rejected_total",
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

// blockingContextSearcher blocks every SearchContext call until release is
// closed (or ctx is cancelled), letting a test force overlapping /mcp
// requests deterministically rather than relying on a real scan's timing.
type blockingContextSearcher struct {
	release  chan struct{}
	inFlight int32
}

func (b *blockingContextSearcher) SearchContext(ctx context.Context, _ string, _, _ int) (contextwin.ContextWindow, error) {
	atomic.AddInt32(&b.inFlight, 1)
	defer atomic.AddInt32(&b.inFlight, -1)
	select {
	case <-b.release:
	case <-ctx.Done():
		return contextwin.ContextWindow{}, ctx.Err()
	}
	return contextwin.ContextWindow{TokenEstimate: 1}, nil
}

// TestMCPConcurrencyLimitWiredIntoRealChain proves /mcp is bounded by
// withConcurrencyLimit in the exact composition runMCPHTTP uses (see the /mcp
// line there, added to close F-09: the Streamable HTTP transport previously had
// no concurrency bound at all). Once the limit's in-flight slots are held, the
// next /mcp request is rejected immediately with 503 and counted on the
// dedicated moedex_http_mcp_rejected_total series — it does not queue behind
// the held slots, which would itself let one client's burst of ranking work
// starve every other agent session.
func TestMCPConcurrencyLimitWiredIntoRealChain(t *testing.T) {
	m := newUnpublishedMetrics()
	bs := &blockingContextSearcher{release: make(chan struct{})}
	const limit = 2
	h := withConcurrencyLimit(mcp.NewServer(bs).HTTPHandler(), limit, m.mcpRejected, "too many concurrent mcp requests")

	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_context","arguments":{"query":"x"}}}`

	// Saturate the limiter: fire `limit` requests and wait until they are
	// actually in flight inside the searcher (past the semaphore, not merely
	// goroutine-scheduled) before firing the overflow request.
	inFlightDone := make(chan *httptest.ResponseRecorder, limit)
	for i := 0; i < limit; i++ {
		go func() { inFlightDone <- postMCP(t, h, call, "") }()
	}
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&bs.inFlight) < int32(limit) {
		select {
		case <-deadline:
			t.Fatalf("only %d/%d requests reached the searcher (limiter not wired in?)", atomic.LoadInt32(&bs.inFlight), limit)
		default:
			time.Sleep(time.Millisecond)
		}
	}

	// All slots are held: the next request must be rejected immediately, not
	// queued behind them.
	rec := postMCP(t, h, call, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("overflow /mcp request: status = %d, want 503", rec.Code)
	}
	if got := m.mcpRejected.Value(); got != 1 {
		t.Errorf("moedex_http_mcp_rejected_total = %d, want 1", got)
	}

	close(bs.release)
	for i := 0; i < limit; i++ {
		if got := (<-inFlightDone).Code; got != http.StatusOK {
			t.Errorf("in-flight request: status = %d, want 200", got)
		}
	}
}
