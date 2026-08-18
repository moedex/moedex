//go:build lsp

package navigate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"
)

// TestDocFor_EvictsLRUAndSendsDidClose is the F-18 regression: docFor used to
// never remove entries from c.docs, so a pooled server's per-file document
// state — and the full-text buffer the external language server mirrors for
// each one — grew for the server's entire life, bounded only by the number of
// distinct files ever navigated to. Once more files have been touched than
// maxOpenDocs, docFor must evict the least-recently-used ones and, for any
// that had actually been opened, send textDocument/didClose — the capability
// the client already advertises at handshake but, before this fix, never
// actually sent.
func TestDocFor_EvictsLRUAndSendsDidClose(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })

	sem := make(chan struct{}, 1)
	sem <- struct{}{}
	c := &LSP{
		stdin:      pw,
		procCancel: func() {},
		writeSem:   sem,
		pending:    make(map[int64]chan rpcResponse),
		docs:       make(map[string]*docState),
		log:        resolveLogger(nil),
	}

	// Drain frames off the pipe in the real Content-Length wire format (mirrors
	// TestReadLoop_*'s use of readFrame) and report every notification method.
	notified := make(chan string, maxOpenDocs+10)
	go func() {
		r := bufio.NewReader(pr)
		for {
			body, err := readFrame(r)
			if err != nil {
				return
			}
			var msg rpcNotification
			if json.Unmarshal(body, &msg) == nil {
				notified <- msg.Method
			}
		}
	}()

	// Touch more distinct files than the cap allows, marking each "opened" the
	// way ensureFresh's syncLocked would after a real didOpen, then release it
	// (as every real call site does once it is done with the doc) before
	// moving to the next file.
	const extra = 5
	paths := make([]string, maxOpenDocs+extra)
	for i := range paths {
		p := fmt.Sprintf("/repo/file%04d.go", i)
		paths[i] = p
		ds := c.docFor(p)
		ds.mu.Lock()
		ds.opened = true
		ds.mu.Unlock()
		c.releaseDoc(ds)
	}

	c.mu.Lock()
	n := len(c.docs)
	_, oldestPresent := c.docs[paths[0]]
	_, newestPresent := c.docs[paths[len(paths)-1]]
	c.mu.Unlock()

	if n > maxOpenDocs {
		t.Fatalf("len(c.docs) = %d after touching %d distinct files, want <= maxOpenDocs (%d)", n, len(paths), maxOpenDocs)
	}
	if oldestPresent {
		t.Error("the least-recently-touched doc should have been evicted, but is still in c.docs")
	}
	if !newestPresent {
		t.Error("the most-recently-touched doc should not have been evicted")
	}

	wantClosed := len(paths) - maxOpenDocs
	gotClosed := 0
	deadline := time.After(2 * time.Second)
	for gotClosed < wantClosed {
		select {
		case m := <-notified:
			if m == "textDocument/didClose" {
				gotClosed++
			}
		case <-deadline:
			t.Fatalf("got %d textDocument/didClose notifications, want %d", gotClosed, wantClosed)
		}
	}
}

// TestDocFor_InFlightDocIsNeverEvicted guards the race the F-18 fix exists to
// close: a caller that has obtained a docState from docFor but has not yet
// called releaseDoc is "between docFor and its own use of the pointer" — if
// eviction could remove that exact docState from c.docs anyway, a second,
// freshly created docState for the same path could later send its own
// didOpen before the first caller ever syncs its, putting two opens on the
// wire for one URI with no didClose between them.
func TestDocFor_InFlightDocIsNeverEvicted(t *testing.T) {
	c := &LSP{
		pending: make(map[int64]chan rpcResponse),
		docs:    make(map[string]*docState),
		log:     resolveLogger(nil),
	}

	const pinnedPath = "/repo/pinned.go"
	pinned := c.docFor(pinnedPath) // deliberately never released

	for i := 0; i < maxOpenDocs+5; i++ {
		p := fmt.Sprintf("/repo/other%04d.go", i)
		ds := c.docFor(p)
		c.releaseDoc(ds)
	}

	c.mu.Lock()
	_, stillThere := c.docs[pinnedPath]
	n := len(c.docs)
	c.mu.Unlock()

	if !stillThere {
		t.Fatal("in-flight doc was evicted even though its caller had not released it yet")
	}
	if n > maxOpenDocs {
		t.Errorf("len(c.docs) = %d, want <= maxOpenDocs (%d)", n, maxOpenDocs)
	}
	if pinned.inFlight < 1 {
		t.Errorf("pinned.inFlight = %d, want >= 1 while still held", pinned.inFlight)
	}
}
