//go:build lsp

package navigate

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// newWedgedLSP builds a bare *LSP whose "stdin" is the write end of a
// synchronous io.Pipe() with no reader: unlike a real OS pipe (which buffers a
// few KB before a Write() call actually blocks), io.Pipe() rendezvous on every
// single byte, so any write blocks immediately and deterministically — the
// same "server that stopped draining stdin while staying alive" condition
// F-10 describes, without needing a real subprocess or a multi-KB write to
// exhaust a kernel buffer. procCancel is instrumented so tests can observe
// whether writeFrame's timeout path actually fires it.
func newWedgedLSP(t *testing.T) (c *LSP, killed *boolFlag) {
	t.Helper()
	_, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })

	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	killed = &boolFlag{}
	c = &LSP{
		stdin:      pw,
		procCancel: func() { killed.set() },
		writeSem:   sem,
		pending:    make(map[int64]chan rpcResponse),
		docs:       make(map[string]*docState),
		log:        resolveLogger(nil),
	}
	return c, killed
}

type boolFlag struct{ v bool }

func (b *boolFlag) set()      { b.v = true }
func (b *boolFlag) get() bool { return b.v }

// TestCall_WedgedStdin_BoundedByContext is the F-10 regression: call()'s
// stdin write used to happen before any ctx-aware select, so a server that
// stopped draining stdin (wedged, but still alive) hung the caller forever
// regardless of the ctx deadline it passed in. Here the "server" never reads
// anything, so the first byte of the request never leaves the write; call()
// must still return within its own 300ms ctx deadline rather than the test's
// 2s outer watchdog.
func TestCall_WedgedStdin_BoundedByContext(t *testing.T) {
	c, killed := newWedgedLSP(t)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := c.call(ctx, "textDocument/definition", map[string]any{})
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("call() err = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call() blocked well past its context deadline while writing to a wedged server (F-10)")
	}

	if c.Alive() {
		t.Error("expected the LSP to be marked dead after a write timed out, so Pool evicts and restarts it")
	}
	if !killed.get() {
		t.Error("expected procCancel to run so the abandoned write eventually unblocks instead of leaking forever")
	}
}

// TestNotify_WedgedStdin_BoundedByContext covers notify() directly (used by
// ensureInit/ensureFresh/NotifyChanged/SetOverlay/DropOverlay), which
// previously took no context argument at all and so had no way to ever time
// out on a wedged server.
func TestNotify_WedgedStdin_BoundedByContext(t *testing.T) {
	c, _ := newWedgedLSP(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- c.notify(ctx, "textDocument/didOpen", map[string]any{}) }()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("notify() err = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notify() blocked well past its context deadline while writing to a wedged server (F-10)")
	}
}

// TestWriteFrame_HealthySecondWriter_NotWedgedByAbandonedOne guards the
// writeSem handoff: after one write is abandoned on ctx timeout, a
// *different*, healthy write (simulated by draining the pipe reader that was
// never read by the first, wedged send) must eventually be able to proceed
// rather than the token leaking forever. We can't reuse the same *LSP (it is
// correctly dead by then, per call()'s fast-fail), so this exercises
// writeFrame's release path directly: the token returns to the semaphore once
// the abandoned write unblocks.
func TestWriteFrame_TokenReleasedAfterAbandonedWriteUnblocks(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })

	sem := make(chan struct{}, 1)
	sem <- struct{}{}
	c := &LSP{
		stdin:      pw,
		procCancel: func() {}, // no real process to kill in this unit test
		writeSem:   sem,
		log:        resolveLogger(nil),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	err := c.writeFrame(ctx, map[string]any{"probe": 1})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("writeFrame err = %v, want context.DeadlineExceeded", err)
	}

	// The token must not be back yet: the abandoned write is still blocked
	// (nothing has read from pr).
	select {
	case <-sem:
		t.Fatal("writeSem token released before the abandoned write actually unblocked")
	default:
	}

	// Draining the pipe reader unblocks the abandoned write, which then must
	// release the token in the background.
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := pr.Read(buf); err != nil {
				return
			}
		}
	}()

	select {
	case <-sem:
		// token released — success
	case <-time.After(2 * time.Second):
		t.Fatal("writeSem token never released after the abandoned write unblocked")
	}
}
