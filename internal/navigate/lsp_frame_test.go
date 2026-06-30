//go:build lsp

package navigate

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// TestReadFrame_RejectsOversizedContentLength guards against F-034: a server
// declaring a huge but otherwise valid (positive, fits in int) Content-Length
// must not drive readFrame's allocation into a panic/OOM. readFrame should
// reject it with a plain error instead of calling make([]byte, contentLen).
func TestReadFrame_RejectsOversizedContentLength(t *testing.T) {
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", int64(9223372036854775807))
	r := bufio.NewReader(strings.NewReader(header))

	_, err := readFrame(r)
	if err == nil {
		t.Fatal("expected readFrame to reject an oversized Content-Length, got nil error")
	}
}

// TestReadFrame_AcceptsNormalFrame is a sanity check that the cap above does
// not reject ordinary, small frames.
func TestReadFrame_AcceptsNormalFrame(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":null}`
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	r := bufio.NewReader(strings.NewReader(header))

	got, err := readFrame(r)
	if err != nil {
		t.Fatalf("readFrame on a normal frame: %v", err)
	}
	if string(got) != body {
		t.Fatalf("readFrame body = %q, want %q", got, body)
	}
}

// TestReadLoop_OversizedContentLength_MarksServerDead drives the real
// readLoop goroutine (not just readFrame) with a malicious header over a pipe,
// the same path a hostile/buggy language server would take. It must not crash
// the test process; it must mark the LSP dead and fail pending callers with
// ErrServerDead.
func TestReadLoop_OversizedContentLength_MarksServerDead(t *testing.T) {
	pr, pw := io.Pipe()
	c := &LSP{
		log:     resolveLogger(nil),
		pending: make(map[int64]chan rpcResponse),
	}
	ch := make(chan rpcResponse, 1)
	c.mu.Lock()
	c.pending[1] = ch
	c.mu.Unlock()

	done := make(chan struct{})
	go func() {
		c.readLoop(pr)
		close(done)
	}()

	fmt.Fprintf(pw, "Content-Length: %d\r\n\r\n", int64(9223372036854775807))
	_ = pw.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("readLoop did not return after an oversized Content-Length")
	}

	if !c.dead.Load() {
		t.Fatal("expected LSP to be marked dead after an oversized Content-Length")
	}

	select {
	case resp := <-ch:
		if !errors.Is(resp.errAny, ErrServerDead) {
			t.Fatalf("expected pending request to fail with ErrServerDead, got %v", resp.errAny)
		}
	case <-time.After(time.Second):
		t.Fatal("pending request was never failed")
	}
}

// infiniteReader streams bytes forever and never produces a '\n', modeling a
// misbehaving server (or an attacker, since LSP binaries are semi-trusted) that
// sends an unterminated header line.
type infiniteReader struct{}

func (infiniteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'A'
	}
	return len(p), nil
}

// TestReadFrame_RejectsUnboundedHeaderLine guards against F-035: the header
// loop must cap how many bytes it will buffer looking for a line terminator,
// not grow without bound via r.ReadString('\n') on a line that never ends.
func TestReadFrame_RejectsUnboundedHeaderLine(t *testing.T) {
	r := bufio.NewReader(infiniteReader{})

	done := make(chan error, 1)
	go func() {
		_, err := readFrame(r)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected readFrame to reject an unterminated header line, got nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readFrame did not return for an unterminated header line (unbounded read)")
	}
}
