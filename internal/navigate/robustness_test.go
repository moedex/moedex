//go:build lsp

package navigate

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// These tests cover the Part-B robustness surface: negotiated incremental sync,
// per-request cancellation that spares the shared process, the typed
// ErrServerDead sentinel, extra-env plumbing, and structured logging. The
// gopls-dependent ones skip when gopls is absent (env-gated skip per CLAUDE.md).

// TestNegotiatedSync_Incremental asserts that after the handshake gopls
// negotiated Incremental(2) sync with the utf-8 position encoding — the two facts
// that gate the range-diff path. It reaches the pooled *LSP directly, as
// pool_test does.
func TestNegotiatedSync_Incremental(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping negotiation test")
	}
	root, _, mainFile := writeFixture(t)
	nav := newNav(t, root)

	// Force the handshake by running one query.
	at := findPos(t, mainFile, "Circle", 1)
	_ = queryWithRetry(t, func(ctx context.Context) ([]Location, error) {
		return nav.Definition(ctx, at)
	})

	if nav.syncKind != 2 {
		t.Errorf("expected gopls to negotiate Incremental(2) sync, got syncKind=%d", nav.syncKind)
	}
	if nav.posEncoding != "utf-8" {
		t.Errorf("expected utf-8 positionEncoding, got %q", nav.posEncoding)
	}
}

// TestRequestCancel_DoesNotKillServer cancels one in-flight request via an
// already-expired ctx, then proves a fresh query on the SAME server still
// succeeds — the per-request cancel did not kill the shared process.
func TestRequestCancel_DoesNotKillServer(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping cancel test")
	}
	root, _, mainFile := writeFixture(t)
	nav := newNav(t, root)

	// Warm the server so the cancel hits a real (not handshake) request.
	at := findPos(t, mainFile, "Circle", 1)
	_ = queryWithRetry(t, func(ctx context.Context) ([]Location, error) {
		return nav.Definition(ctx, at)
	})

	// Cancel a request immediately.
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := nav.Definition(cctx, at)
	if err == nil {
		t.Fatal("expected a cancellation error")
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context cancel/deadline error, got %v", err)
	}

	// The process must have survived: a fresh query still resolves.
	if !nav.Alive() {
		t.Fatal("server died after a per-request cancel")
	}
	locs := queryWithRetry(t, func(ctx context.Context) ([]Location, error) {
		return nav.Definition(ctx, at)
	})
	if len(locs) == 0 {
		t.Fatal("post-cancel query returned no locations; server unusable")
	}
}

// TestServerDeath_TypedError kills the process, then asserts a direct query
// returns an error matching ErrServerDead.
func TestServerDeath_TypedError(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping dead-server test")
	}
	root, _, mainFile := writeFixture(t)
	nav := newNav(t, root)

	at := findPos(t, mainFile, "Circle", 1)
	_ = queryWithRetry(t, func(ctx context.Context) ([]Location, error) {
		return nav.Definition(ctx, at)
	})

	if nav.cmd.Process == nil {
		t.Fatal("server has no process")
	}
	_ = nav.cmd.Process.Kill()
	deadline := time.Now().Add(10 * time.Second)
	for nav.Alive() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if nav.Alive() {
		t.Fatal("server still Alive after Kill")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := nav.Definition(ctx, at)
	if err == nil {
		t.Fatal("expected an error from a dead server")
	}
	if !errors.Is(err, ErrServerDead) {
		t.Fatalf("expected errors.Is(err, ErrServerDead), got %v", err)
	}
}

// TestConfigEnv smoke-tests that extra environment does not break startup and a
// query still resolves.
func TestConfigEnv(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping env test")
	}
	root, _, mainFile := writeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	nav, err := NewLSP(ctx, Config{RootDir: root, Env: []string{"GOFLAGS=-mod=mod"}})
	if err != nil {
		t.Fatalf("NewLSP with extra env: %v", err)
	}
	t.Cleanup(func() { _ = nav.Close() })

	at := findPos(t, mainFile, "Circle", 1)
	locs := queryWithRetry(t, func(c context.Context) ([]Location, error) {
		return nav.Definition(c, at)
	})
	if len(locs) == 0 {
		t.Fatal("query returned no locations under extra env")
	}
}

// TestLoggerCapture passes a Config.Logger writing to a buffer and asserts at
// least one structured debug line is emitted during the handshake.
func TestLoggerCapture(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping logger test")
	}
	root, _, mainFile := writeFixture(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	nav, err := NewLSP(ctx, Config{RootDir: root, Logger: logger})
	if err != nil {
		t.Fatalf("NewLSP: %v", err)
	}
	t.Cleanup(func() { _ = nav.Close() })

	at := findPos(t, mainFile, "Circle", 1)
	_ = queryWithRetry(t, func(c context.Context) ([]Location, error) {
		return nav.Definition(c, at)
	})

	out := buf.String()
	if !strings.Contains(out, "lsp handshake") {
		t.Fatalf("expected a handshake debug line in logger output; got:\n%s", out)
	}
}
