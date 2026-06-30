package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"moedex/internal/server"
)

// TestWithConcurrencyLimitRejectsOverflow proves the core property: once n
// requests are in flight, the next one is rejected immediately with 503
// rather than queuing behind the held slots.
func TestWithConcurrencyLimitRejectsOverflow(t *testing.T) {
	m := newUnpublishedMetrics()
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	})
	h := withConcurrencyLimit(slow, 1, m)

	done1 := make(chan *httptest.ResponseRecorder, 1)
	go func() { done1 <- doGet(t, h, "/search?q=x", "") }()
	<-started // first request now holds the only slot

	rec2 := doGet(t, h, "/search?q=y", "")
	if rec2.Code != http.StatusServiceUnavailable {
		t.Errorf("overflow request: status = %d, want 503", rec2.Code)
	}

	close(release)
	rec1 := <-done1
	if rec1.Code != http.StatusOK {
		t.Errorf("in-flight request: status = %d, want 200", rec1.Code)
	}
	if got := m.searchRejected.Value(); got != 1 {
		t.Errorf("searchRejected counter = %d, want 1", got)
	}
}

// TestWithConcurrencyLimitAllowsUpToCapacity proves the limiter does not
// reject requests that fit within its capacity.
func TestWithConcurrencyLimitAllowsUpToCapacity(t *testing.T) {
	m := newUnpublishedMetrics()
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	})
	h := withConcurrencyLimit(slow, 2, m)

	done := make(chan *httptest.ResponseRecorder, 2)
	go func() { done <- doGet(t, h, "/search?q=a", "") }()
	go func() { done <- doGet(t, h, "/search?q=b", "") }()
	<-started
	<-started

	close(release)
	for i := range 2 {
		if rec := <-done; rec.Code != http.StatusOK {
			t.Errorf("request %d: status = %d, want 200", i, rec.Code)
		}
	}
	if got := m.searchRejected.Value(); got != 0 {
		t.Errorf("searchRejected counter = %d, want 0 (both within capacity)", got)
	}
}

// TestWithConcurrencyLimitZeroDisables proves n<=0 is a passthrough (no cap).
func TestWithConcurrencyLimitZeroDisables(t *testing.T) {
	m := newUnpublishedMetrics()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := withConcurrencyLimit(inner, 0, m)
	if rec := doGet(t, h, "/search?q=x", ""); rec.Code != http.StatusOK {
		t.Errorf("disabled limiter: status = %d, want 200", rec.Code)
	}
}

// TestSearchConcurrencyLimitWiredIntoRealChain drives newHTTPMux — the exact
// function runHTTP calls to build its route table — with a slow real corpus
// scan, to prove the limiter is wired around /search in production and not
// just unit-tested in isolation. It skips on hosts where a single scan
// completes too fast for N concurrent requests to reliably overlap, mirroring
// the existing tolerance for timing-based heuristics in
// TestRequestTimeoutCancelsScan.
func TestSearchConcurrencyLimitWiredIntoRealChain(t *testing.T) {
	dir := makeLargeShardDir(t)
	c, err := server.Open(dir)
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	defer c.Close()

	start := time.Now()
	if _, _, err := c.Regex(context.Background(), "handler|response|payload"); err != nil {
		t.Fatalf("baseline regex: %v", err)
	}
	if base := time.Since(start); base < 10*time.Millisecond {
		t.Skipf("baseline scan too fast (%s) to test concurrency overlap reliably", base)
	}

	holder := newCorpusHolder(c)
	m := newUnpublishedMetrics()
	const searchCap = 2
	mux := newHTTPMux(holder, m, searchCap)
	h := chain(mux, "", 30*time.Second, m)

	const n = searchCap + 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			rec := doGet(t, h, "/search?q=handler%7Cresponse%7Cpayload&regex=1", "")
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()

	var rejected, ok int
	for _, code := range codes {
		switch code {
		case http.StatusServiceUnavailable:
			rejected++
		case http.StatusOK:
			ok++
		default:
			t.Errorf("unexpected status %d", code)
		}
	}
	if rejected == 0 {
		t.Errorf("0/%d requests rejected with cap=%d concurrent /search; limiter not wired in", n, searchCap)
	}
	if ok == 0 {
		t.Errorf("0/%d requests succeeded; limiter rejected everything", n)
	}
}
