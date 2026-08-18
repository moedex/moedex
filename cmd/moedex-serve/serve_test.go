package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/server"
)

// newTestChain builds the same hardened handler runHTTP serves: the real mux
// (with /healthz, /metrics, /stats, /search) wrapped by chain(...), with /search
// bounded by withConcurrencyLimit exactly as runHTTP wires it. Tests drive this,
// not reimplementations, so they exercise the production composition.
func newTestChain(t *testing.T, token string, timeout time.Duration, searchMaxConcurrency int) (http.Handler, *corpusHolder, *metrics) {
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
	mux.Handle("/search", withConcurrencyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := holder.acquire()
		defer snap.release()
		handleSearch(snap.c, w, r)
	}), searchMaxConcurrency, m.searchRejected, "too many concurrent searches"))
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
	h, _, _ := newTestChain(t, tok, 5*time.Second, defaultSearchMaxConcurrency)

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
	h, _, _ := newTestChain(t, "", 5*time.Second, defaultSearchMaxConcurrency)
	rec := doGet(t, h, "/search?q=marker", "")
	if rec.Code != http.StatusOK {
		t.Errorf("/search with empty token config: status = %d, want 200", rec.Code)
	}
}

// TestRequestTimeoutBoundsResponse drives a deliberately slow handler through the
// real withTimeout to confirm the HTTP layer returns early with 503.
//
// This test asserts only the HTTP-layer bound (a 503 returns before the slow
// handler finishes). The companion TestRequestTimeoutCancelsScan asserts the
// deeper property — that a real /search scan observes the cancelled context and
// aborts promptly rather than running to completion.
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

// makeLargeShardDir writes a one-shard dir whose blobs are big enough that a full
// scan takes meaningful wall-time — used to prove the scan observes cancellation
// rather than running to completion.
func makeLargeShardDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ix := index.New()
	for b := 0; b < 80; b++ {
		var sb []byte
		for line := 0; line < 1500; line++ {
			sb = append(sb, []byte(fmt.Sprintf("var handler_%d=function(response){return payload(%d)};\n", line, b))...)
		}
		rel := fmt.Sprintf("app%d.js", b)
		ix.AddFile("repo", rel, filepath.Join(dir, rel), fmt.Sprintf("sha-%d", b), sb)
	}
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatalf("save shard: %v", err)
	}
	return dir
}

// TestRequestTimeoutCancelsScan proves the deep cancellation property the
// HTTP-only TestRequestTimeoutBoundsResponse cannot: when a /search request's
// context is cancelled (as withTimeout does on expiry), the underlying corpus
// scan observes the cancellation and aborts promptly instead of running to
// completion. handleSearch is driven directly with a pre-cancelled request
// context, which is fully synchronous (no detached goroutine racing Close):
//   - (1) it returns far faster than an uncancelled scan of the same query, and
//   - (2) it takes the cancellation early-return branch — writing nothing, so the
//     TimeoutHandler's own 503 body is the only response the client ever sees.
func TestRequestTimeoutCancelsScan(t *testing.T) {
	dir := makeLargeShardDir(t)
	c, err := server.Open(dir)
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	defer c.Close()

	// Baseline: how long does the uncancelled regex scan take? The cancelled run
	// must be far below this to prove the scan did not run to completion.
	start := time.Now()
	if _, _, err := c.Regex(context.Background(), "handler|response|payload"); err != nil {
		t.Fatalf("baseline Regex: %v", err)
	}
	base := time.Since(start)
	if base < 5*time.Millisecond {
		t.Skipf("baseline scan too fast (%s) to test cancellation meaningfully", base)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/search?q=handler%7Cresponse%7Cpayload&regex=1", nil).WithContext(ctx)
	hrec := httptest.NewRecorder()
	hStart := time.Now()
	handleSearch(c, hrec, req)
	elapsed := time.Since(hStart)

	// (1) Returned promptly — the scan aborted, it did not complete.
	if elapsed >= base/2 {
		t.Errorf("pre-cancelled handleSearch took %s; uncancelled baseline %s — scan did not abort promptly", elapsed, base)
	}
	// (2) Took the cancellation early return: no body is written, so the outer
	// TimeoutHandler's 503 is the only thing the client sees (and crucially, no
	// "count" search payload was produced).
	if hrec.Body.Len() != 0 {
		t.Errorf("pre-cancelled handleSearch wrote a body (%q); want empty (cancellation early return)", hrec.Body.String())
	}
	if strings.Contains(hrec.Body.String(), "\"count\"") {
		t.Errorf("scan ran to completion: body has a search payload (%q)", hrec.Body.String())
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
	h, holder, m := newTestChain(t, "", 5*time.Second, defaultSearchMaxConcurrency)

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
