package main

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"moedex/internal/server"
)

// waitOrTimeout blocks on wg.Wait() in a goroutine and reports whether it
// returned within d. A refcount leak (a missing release()) means wg.Wait()
// would otherwise block forever, so this never calls retire()/Close() on a
// corpus we've deliberately broken — it only observes whether the
// WaitGroup itself drained.
func waitOrTimeout(t *testing.T, label string, wg *sync.WaitGroup, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s: refcount leaked — wg never drained after handler panic (release() not deferred)", label)
	}
}

// TestMetricsHandlerReleasesRefcountOnPanic proves metricsHandler releases its
// corpus refcount even when something panics between acquire() and release().
// It forces the panic by nil-ing the live snapshot's corpus pointer (same
// package as corpusSnapshot, so the unexported field is reachable) so that
// snap.c.NumShards() hits a nil-pointer dereference inside the acquire/release
// window — exactly the gap F-018 flags. withRecover (the same outer layer
// runHTTP installs) catches it and must not leave the refcount incremented.
func TestMetricsHandlerReleasesRefcountOnPanic(t *testing.T) {
	dir := makeShardDir(t, "marker")
	c, err := server.Open(dir)
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	defer c.Close()

	holder := newCorpusHolder(c)
	m := newUnpublishedMetrics()
	h := withRecover(metricsHandler(holder, m), m)

	holder.cur.c = nil // trigger a nil-pointer panic inside the acquire/release window

	before := m.panics.Value()
	rec := doGet(t, h, "/metrics", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (panic should be recovered)", rec.Code)
	}
	if got := m.panics.Value(); got != before+1 {
		t.Fatalf("panic counter = %d, want %d", got, before+1)
	}

	waitOrTimeout(t, "metricsHandler", &holder.cur.wg, 200*time.Millisecond)
}

// TestRankMetricsHandlerReleasesRefcountOnPanic is the rankHolder analogue of
// TestMetricsHandlerReleasesRefcountOnPanic, covering the -mcp daemon's
// rankMetricsHandler.
func TestRankMetricsHandlerReleasesRefcountOnPanic(t *testing.T) {
	dir := makeShardDir(t, "marker")
	ctx := context.Background()
	rc, err := server.OpenRank(ctx, dir, server.RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("openrank: %v", err)
	}
	defer rc.Close()

	holder := newRankHolder(rc)
	m := newUnpublishedMetrics()
	h := withRecover(rankMetricsHandler(holder, m), m)

	holder.cur.rc = nil // trigger a nil-pointer panic inside the acquire/release window

	before := m.panics.Value()
	rec := doGet(t, h, "/metrics", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (panic should be recovered)", rec.Code)
	}
	if got := m.panics.Value(); got != before+1 {
		t.Fatalf("panic counter = %d, want %d", got, before+1)
	}

	waitOrTimeout(t, "rankMetricsHandler", &holder.cur.wg, 200*time.Millisecond)
}
