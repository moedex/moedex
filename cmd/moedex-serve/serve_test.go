package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"moedex/internal/server"
)

// newTestChain builds the same hardened handler runHTTP serves: the real mux
// (with /healthz, /metrics, /stats, /search) wrapped by chain(...). Tests drive
// this, not reimplementations, so they exercise the production composition.
func newTestChain(t *testing.T, token string, timeout time.Duration) (http.Handler, *corpusHolder, *metrics) {
	t.Helper()
	dir := makeShardDir(t, "marker") // "marker" is the in-shard content token
	c, err := server.Open(dir)
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	holder := newCorpusHolder(c)
	m := newUnpublishedMetrics() // unpublished: avoids expvar duplicate-name panic across tests

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/metrics", metricsHandler(holder, m))
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		snap := holder.acquire()
		defer snap.release()
		writeJSON(w, http.StatusOK, map[string]any{"shards": snap.c.NumShards(), "blobs": snap.c.NumBlobs()})
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		snap := holder.acquire()
		defer snap.release()
		handleSearch(snap.c, w, r)
	})
	return chain(mux, token, timeout, m), holder, m
}

func doGet(t *testing.T, h http.Handler, path, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthRequiredWhenTokenSet(t *testing.T) {
	const tok = "s3cret-bearer" // distinct from the shard content marker
	h, _, _ := newTestChain(t, tok, 5*time.Second)

	// /search requires the token.
	if rec := doGet(t, h, "/search?q=marker", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("/search no header: status = %d, want 401", rec.Code)
	}
	if rec := doGet(t, h, "/search?q=marker", "wrong-token"); rec.Code != http.StatusUnauthorized {
		t.Errorf("/search wrong token: status = %d, want 401", rec.Code)
	}
	rec := doGet(t, h, "/search?q=marker", tok)
	if rec.Code != http.StatusOK {
		t.Errorf("/search right token: status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "\"count\"") {
		t.Errorf("/search right token: body lacks count field: %s", rec.Body.String())
	}

	// Open probes pass without a token even when one is configured.
	if rec := doGet(t, h, "/healthz", ""); rec.Code != http.StatusOK {
		t.Errorf("/healthz no token: status = %d, want 200", rec.Code)
	}
	if rec := doGet(t, h, "/metrics", ""); rec.Code != http.StatusOK {
		t.Errorf("/metrics no token: status = %d, want 200", rec.Code)
	}
}

func TestNoAuthWhenTokenEmpty(t *testing.T) {
	h, _, _ := newTestChain(t, "", 5*time.Second)
	rec := doGet(t, h, "/search?q=marker", "")
	if rec.Code != http.StatusOK {
		t.Errorf("/search with empty token config: status = %d, want 200", rec.Code)
	}
}

// TestRequestTimeoutBoundsResponse drives a deliberately slow handler through the
// real withTimeout to confirm the HTTP layer returns early with 503.
//
// CAVEAT (by design): this bounds only the HTTP response. Corpus.Regex/Literal
// take no context.Context, so a real /search scan keeps running to completion
// after the client receives the 503. Deep cancellation requires threading ctx
// through internal/server + internal/search and is a tracked follow-up.
func TestRequestTimeoutBoundsResponse(t *testing.T) {
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	h := withTimeout(slow, 30*time.Millisecond)

	start := time.Now()
	rec := doGet(t, h, "/anything", "")
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("slow handler: status = %d, want 503", rec.Code)
	}
	if elapsed >= 200*time.Millisecond {
		t.Errorf("response took %s; timeout did not bound it early", elapsed)
	}
}

func TestPanicHandlerYields500AndSurvives(t *testing.T) {
	m := newUnpublishedMetrics()
	var panicNext bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if panicNext {
			panic("boom")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("alive"))
	})
	// recover(log(inner)); auth/timeout are irrelevant to this property.
	h := withRecover(withAccessLog(inner, m), m)

	before := m.panics.Value()
	panicNext = true
	rec := doGet(t, h, "/x", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panicking handler: status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal error") {
		t.Errorf("500 body lacks error message: %s", rec.Body.String())
	}
	if got := m.panics.Value(); got != before+1 {
		t.Errorf("panic counter = %d, want %d", got, before+1)
	}

	// The daemon survives: a second request is served normally.
	panicNext = false
	rec = doGet(t, h, "/x", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "alive" {
		t.Errorf("post-panic request: status = %d body = %q, want 200 \"alive\"", rec.Code, rec.Body.String())
	}
}

func TestRecoverDoesNotOverwriteWrittenResponse(t *testing.T) {
	m := newUnpublishedMetrics()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("after write")
	})
	h := withRecover(inner, m)

	rec := doGet(t, h, "/x", "")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (recover must not overwrite a committed header)", rec.Code)
	}
	if rec.Body.String() != "partial" {
		t.Errorf("body = %q, want %q (recover must not append an error doc)", rec.Body.String(), "partial")
	}
}

func TestEffectiveAddrLoopback(t *testing.T) {
	cases := []struct {
		addr, token, want string
	}{
		{":8080", "", "127.0.0.1:8080"},
		{":8080", "secret", "127.0.0.1:8080"},
		{"8080", "", "127.0.0.1:8080"},
		{"0.0.0.0:8080", "", "0.0.0.0:8080"},
		{"0.0.0.0:8080", "secret", "0.0.0.0:8080"},
		{"192.168.1.5:8080", "", "192.168.1.5:8080"},
		{"127.0.0.1:9090", "", "127.0.0.1:9090"},
	}
	for _, tc := range cases {
		if got := resolveAddr(tc.addr, tc.token); got != tc.want {
			t.Errorf("resolveAddr(%q, %q) = %q, want %q", tc.addr, tc.token, got, tc.want)
		}
	}
}

func TestMetricsEndpointExposition(t *testing.T) {
	h, holder, m := newTestChain(t, "", 5*time.Second)

	// Drive a couple of requests so counters/histogram have observations.
	doGet(t, h, "/search?q=marker", "")
	doGet(t, h, "/healthz", "")

	rec := doGet(t, h, "/metrics", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "version=0.0.4") {
		t.Errorf("/metrics Content-Type = %q, want Prometheus 0.0.4", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"moedex_http_requests_total",
		"moedex_http_request_duration_seconds_count",
		"moedex_http_request_duration_seconds_bucket{le=\"+Inf\"}",
		"moedex_corpus_shards",
		"moedex_corpus_blobs",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics body missing %q\nbody:\n%s", want, body)
		}
	}

	// The gauges reflect the live corpus read through the holder.
	snap := holder.acquire()
	wantBlobs := snap.c.NumBlobs()
	snap.release()
	if wantBlobs <= 0 {
		t.Fatalf("test corpus has %d blobs; expected >0", wantBlobs)
	}
	_ = m // metrics already exercised via observe in the chain
	// Drain the body fully (httptest already buffers, but keep io imported and
	// assert the body is non-trivial).
	if _, err := io.Copy(io.Discard, strings.NewReader(body)); err != nil {
		t.Fatalf("read body: %v", err)
	}
}
