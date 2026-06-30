//go:build lsp

package navigate

import (
	"context"
	"testing"
	"time"
)

// TestNotifyChanged_HonorsCtxCancellation is the regression for F-078:
// NotifyChanged did a bare `<-ent.ready`, unlike NavigatorFor (which selects on
// ctx.Done()), so a caller had no way to abandon a NotifyChanged parked on a
// creation that never finishes. Seed an in-flight (never-ready) entry, cancel
// ctx, and assert NotifyChanged returns promptly with ctx.Err() instead of
// blocking forever.
func TestNotifyChanged_HonorsCtxCancellation(t *testing.T) {
	clk := time.Now()
	p := newTestPool(Config{}, &clk)

	root := t.TempDir()
	file := root + "/x.go"
	fileLang, wsRoot := workspaceRoot(file)
	key := keyFor(p.routingLang(fileLang), wsRoot)
	seedInFlight(p, key) // creation that will never finish in this test
	ent := p.entries[key]
	t.Cleanup(func() {
		close(ent.ready) // let Close()'s wait-for-in-flight finish instead of hanging
		_ = p.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	done := make(chan error, 1)
	go func() { done <- p.NotifyChanged(ctx, file) }()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("NotifyChanged err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("NotifyChanged did not return promptly after ctx cancellation; it blocked on <-ent.ready")
	}
}
