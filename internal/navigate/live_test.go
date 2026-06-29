//go:build lsp

package navigate

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"
)

// These tests are the ADR 0017 Condition-3 evidence: navigation reflects the
// LIVE working tree — uncommitted on-disk edits and even unsaved in-memory
// buffers — not a committed snapshot. A SHA-addressed index is stale against
// both by construction; this path is not. They skip when gopls is absent.

// fixtureMainPlus is fixtureMain with a third Circle literal — one extra
// reference to shape.Circle.
const fixtureMainPlus = `package main

import "navfix/shape"

func describe(s shape.Shape) float64 {
	return s.Area()
}

func main() {
	a := shape.Circle{R: 2}
	b := shape.Circle{R: 3}
	c := shape.Circle{R: 4}
	_ = describe(a)
	_ = describe(b)
	_ = describe(c)
}
`

// waitForRefCount re-queries references to the Circle type until the result has
// exactly want locations or the deadline passes. gopls reanalyzes asynchronously
// after a didOpen/didChange, so the post-edit count converges rather than
// flipping instantly.
func waitForRefCount(t *testing.T, pool *Pool, declFile string, want int) []Location {
	t.Helper()
	at := identPos(t, declFile, "type Circle struct", "Circle")
	deadline := time.Now().Add(30 * time.Second)
	var last []Location
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		locs, err := pool.References(ctx, at, true)
		cancel()
		if err != nil {
			t.Fatalf("references: %v", err)
		}
		if len(locs) == want {
			return locs
		}
		last = locs
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("references count never reached %d; last=%d", want, len(last))
	return nil
}

func refCount(t *testing.T, pool *Pool, declFile string) int {
	t.Helper()
	at := identPos(t, declFile, "type Circle struct", "Circle")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	locs := queryWithRetry(t, func(c context.Context) ([]Location, error) {
		return pool.References(ctx, at, true)
	})
	return len(locs)
}

// TestLiveEdit_DiskResync proves an uncommitted edit written to the working tree
// between two queries is picked up: the second query reflects the new reference.
func TestLiveEdit_DiskResync(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping live-edit test")
	}
	_, shapeFile, mainFile := writeFixture(t)
	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })

	base := refCount(t, pool, shapeFile)
	if base < 2 {
		t.Fatalf("baseline reference count too low: %d", base)
	}

	// Simulate an Execute-phase edit: add a third shape.Circle{...} use on disk,
	// then tell the pool the file changed (the signal an orchestrator that just
	// edited main.go would send — no file watcher, no new dependency).
	mustWrite(t, mainFile, fixtureMainPlus)
	if err := pool.NotifyChanged(context.Background(), mainFile); err != nil {
		t.Fatalf("NotifyChanged: %v", err)
	}

	got := waitForRefCount(t, pool, shapeFile, base+1)
	var inMain int
	for _, l := range got {
		if endsWith(l.File, "main.go") {
			inMain++
		}
	}
	if inMain < 3 {
		t.Errorf("after edit expected >=3 references in main.go, got %d", inMain)
	}
}

// TestOverlay_UnsavedBuffer proves navigation over an in-memory buffer that was
// NEVER written to disk: SetOverlay pushes edited content, references reflect it,
// disk stays untouched, and DropOverlay reverts. This is the dirty-buffer case a
// committed-corpus index cannot serve at all.
func TestOverlay_UnsavedBuffer(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping overlay test")
	}
	_, shapeFile, mainFile := writeFixture(t)
	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })

	base := refCount(t, pool, shapeFile)

	// Push an unsaved buffer with one extra reference. Disk is not touched.
	diskBefore, _ := os.ReadFile(mainFile)
	if err := pool.SetOverlay(context.Background(), mainFile, []byte(fixtureMainPlus)); err != nil {
		t.Fatalf("SetOverlay: %v", err)
	}
	waitForRefCount(t, pool, shapeFile, base+1)

	// The working-tree file must be byte-for-byte unchanged — the extra
	// reference lives only in the server's buffer.
	diskAfter, _ := os.ReadFile(mainFile)
	if !bytes.Equal(diskBefore, diskAfter) {
		t.Fatal("overlay must not modify the file on disk")
	}

	// Dropping the overlay reverts navigation to the on-disk content.
	if err := pool.DropOverlay(context.Background(), mainFile); err != nil {
		t.Fatalf("DropOverlay: %v", err)
	}
	waitForRefCount(t, pool, shapeFile, base)
}

func endsWith(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
