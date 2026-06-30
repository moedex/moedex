//go:build lsp

package navigate

import (
	"context"
	"testing"
	"time"
)

// TestClose_WedgedShutdown_DoesNotHang proves the Close() teardown contract: a
// language server that accepts the "shutdown" request but never replies (a
// wedged server) must not block Close() forever. Close() drives the process via
// a plain "sleep" command standing in for a wedged language server — it reads
// nothing from stdin and writes nothing to stdout, so the "shutdown" call's
// response channel never fires and ctx.Background() never expires on its own.
//
// Before the fix, Close() calls c.call(context.Background(), "shutdown", nil)
// with no deadline, and "shutdown" is exempt from the dead/closed fast-fail
// guards in call() — so a wedged server hangs Close() (and therefore
// Pool.Close(), which closes servers synchronously) indefinitely. The kill
// (procCancel) and reap (cmd.Wait()) only run after call() returns, so a hung
// shutdown handshake leaks the process and its reader goroutine.
func TestClose_WedgedShutdown_DoesNotHang(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// "sleep 30" stands in for a wedged language server: a live process that
	// never speaks the LSP framing, so any request awaiting a reply blocks
	// forever absent a bounded context.
	nav, err := NewLSP(ctx, Config{Server: "sleep", Args: []string{"30"}})
	if err != nil {
		t.Fatalf("NewLSP: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- nav.Close() }()

	select {
	case <-done:
		// Close returned — the process must have actually been reaped (Close's
		// own implementation returns cmd.Wait()'s result), so no zombie/leak.
	case <-time.After(5 * time.Second):
		t.Fatal("Close() did not return within 5s; a wedged shutdown handshake hung teardown")
	}
}
