package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"moedex/internal/contextwin"
)

// blockingSearcher blocks until released (or ctx is cancelled), letting tests
// force overlapping in-flight requests and exercise the timeout path.
type blockingSearcher struct {
	release  chan struct{} // closed to let all calls proceed
	inFlight int32         // peak concurrent calls observed
	peak     int32
}

func (b *blockingSearcher) SearchContext(ctx context.Context, q string, _, _ int) (contextwin.ContextWindow, error) {
	n := atomic.AddInt32(&b.inFlight, 1)
	for {
		p := atomic.LoadInt32(&b.peak)
		if n <= p || atomic.CompareAndSwapInt32(&b.peak, p, n) {
			break
		}
	}
	defer atomic.AddInt32(&b.inFlight, -1)
	select {
	case <-b.release:
	case <-ctx.Done():
		return contextwin.ContextWindow{}, ctx.Err()
	}
	return contextwin.ContextWindow{
		Blocks:        []contextwin.ContextBlock{{RelPath: q + ".go", StartLine: 1, EndLine: 1, Text: "x\n"}},
		TokenEstimate: 1,
	}, nil
}

// sleepSearcher sleeps for d (respecting ctx) then returns.
type sleepSearcher struct{ d time.Duration }

func (s *sleepSearcher) SearchContext(ctx context.Context, _ string, _, _ int) (contextwin.ContextWindow, error) {
	select {
	case <-time.After(s.d):
		return contextwin.ContextWindow{}, nil
	case <-ctx.Done():
		return contextwin.ContextWindow{}, ctx.Err()
	}
}

// panicSearcher panics, modeling a buggy retrieval path.
type panicSearcher struct{}

func (panicSearcher) SearchContext(context.Context, string, int, int) (contextwin.ContextWindow, error) {
	panic("boom in searcher")
}

func toolCall(id int, query string) map[string]interface{} {
	return map[string]interface{}{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]interface{}{
			"name":      "search_context",
			"arguments": map[string]interface{}{"query": query},
		},
	}
}

func encodeLines(t *testing.T, msgs ...interface{}) *bytes.Buffer {
	t.Helper()
	var in bytes.Buffer
	enc := json.NewEncoder(&in)
	if err := enc.Encode(legacyInitialize("bootstrap")); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(map[string]interface{}{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			t.Fatal(err)
		}
	}
	return &in
}

func decodeResponses(t *testing.T, out *bytes.Buffer) []response {
	t.Helper()
	var resps []response
	dec := json.NewDecoder(out)
	for dec.More() {
		var r response
		if err := dec.Decode(&r); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if string(r.ID) == `"bootstrap"` {
			continue
		}
		resps = append(resps, r)
	}
	return resps
}

// idOf extracts the numeric id from a response (ids are JSON numbers here).
func idOf(t *testing.T, r response) int {
	t.Helper()
	if len(r.ID) == 0 {
		return -1
	}
	var n int
	if err := json.Unmarshal(r.ID, &n); err != nil {
		t.Fatalf("bad id %q: %v", r.ID, err)
	}
	return n
}

// TestPerRequestTimeoutReturnsError: a searcher that outlives the request
// timeout must produce a JSON-RPC error, not a hang.
func TestPerRequestTimeoutReturnsError(t *testing.T) {
	s := NewServer(&sleepSearcher{d: 2 * time.Second}, WithRequestTimeout(50*time.Millisecond))
	in := encodeLines(t, toolCall(1, "needle"))
	var out bytes.Buffer

	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), in, &out) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve hung past the request timeout")
	}

	resps := decodeResponses(t, &out)
	if len(resps) != 1 {
		t.Fatalf("want 1 response, got %d", len(resps))
	}
	if resps[0].Error == nil || resps[0].Error.Code != codeInternalError {
		t.Fatalf("want internal error from timeout, got %+v (result=%v)", resps[0].Error, resps[0].Result)
	}
	if !strings.Contains(resps[0].Error.Message, "context deadline exceeded") {
		t.Errorf("error should mention deadline, got %q", resps[0].Error.Message)
	}
}

// TestConcurrentRequestsAllAnsweredWithMatchingIDs: many overlapping tools/call
// requests must each get a correctly-framed reply with the right id, and the
// server must actually run them concurrently.
func TestConcurrentRequestsAllAnsweredWithMatchingIDs(t *testing.T) {
	const n = 16
	bs := &blockingSearcher{release: make(chan struct{})}
	s := NewServer(bs, WithMaxConcurrency(n))

	msgs := make([]interface{}, n)
	for i := 0; i < n; i++ {
		msgs[i] = toolCall(i, fmt.Sprintf("q%d", i))
	}
	in := encodeLines(t, msgs...)
	var out bytes.Buffer

	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), in, &out) }()

	// Give the reader time to dispatch all requests so they overlap, then release.
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&bs.inFlight) < int32(n) {
		select {
		case <-deadline:
			t.Fatalf("only %d requests went in-flight, want %d (not concurrent)", atomic.LoadInt32(&bs.inFlight), n)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(bs.release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve hung")
	}

	if bs.peak < 2 {
		t.Errorf("peak concurrency %d, expected overlap", bs.peak)
	}
	resps := decodeResponses(t, &out)
	if len(resps) != n {
		t.Fatalf("want %d responses, got %d", n, len(resps))
	}
	seen := map[int]bool{}
	for _, r := range resps {
		id := idOf(t, r)
		if r.Error != nil {
			t.Errorf("id %d got error %+v", id, r.Error)
		}
		if r.Result == nil {
			t.Errorf("id %d missing result", id)
		}
		if seen[id] {
			t.Errorf("duplicate id %d", id)
		}
		seen[id] = true
	}
	for i := 0; i < n; i++ {
		if !seen[i] {
			t.Errorf("missing reply for id %d", i)
		}
	}
}

// TestOutputNotInterleavedUnderConcurrency: every line on the output stream must
// be a complete, parseable JSON object — concurrent handlers must not corrupt
// framing. We force big result payloads and high overlap.
func TestOutputNotInterleavedUnderConcurrency(t *testing.T) {
	const n = 24
	big := strings.Repeat("ABCDEFGHIJ", 2000) // ~20KB per block
	ms := &multiSearcher{text: big}
	s := NewServer(ms, WithMaxConcurrency(n))

	msgs := make([]interface{}, n)
	for i := 0; i < n; i++ {
		msgs[i] = toolCall(i, fmt.Sprintf("q%d", i))
	}
	in := encodeLines(t, msgs...)
	var out bytes.Buffer
	if err := s.Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	// Each non-empty output line must independently decode as one response.
	lines := bytes.Split(bytes.TrimRight(out.Bytes(), "\n"), []byte("\n"))
	seen := map[int]bool{}
	for i, ln := range lines {
		var r response
		if err := json.Unmarshal(ln, &r); err != nil {
			t.Fatalf("line %d not a clean JSON object (interleaved?): %v\nfirst 80 bytes: %q", i, err, ln[:min(80, len(ln))])
		}
		if string(r.ID) == `"bootstrap"` {
			continue
		}
		seen[idOf(t, r)] = true
	}
	if len(seen) != n {
		t.Fatalf("want %d framed tool responses, got %d", n, len(seen))
	}
	for i := 0; i < n; i++ {
		if !seen[i] {
			t.Errorf("missing id %d", i)
		}
	}
}

type multiSearcher struct{ text string }

func (m *multiSearcher) SearchContext(_ context.Context, q string, _, _ int) (contextwin.ContextWindow, error) {
	return contextwin.ContextWindow{
		Blocks:        []contextwin.ContextBlock{{RelPath: q, StartLine: 1, EndLine: 1, Text: m.text}},
		TokenEstimate: len(m.text) / 4,
	}, nil
}

// TestOversizedRequestRejected: a request line larger than the cap must not OOM,
// crash, or reach the searcher. The SDK closes the malformed stdio session
// without fabricating a JSON-RPC reply for bytes it did not parse.
func TestOversizedRequestRejected(t *testing.T) {
	searcher := &fakeSearcher{}
	s := NewServer(searcher, WithMaxRequestBytes(256))
	// A single line well over the cap.
	huge := toolCall(1, strings.Repeat("x", 5000))
	in := encodeLines(t, huge)
	var out bytes.Buffer

	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), in, &out) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve hung on oversized input")
	}

	resps := decodeResponses(t, &out)
	if len(resps) != 0 {
		t.Fatalf("oversized request produced %d tool responses, want none", len(resps))
	}
	if searcher.gotQuery != "" {
		t.Fatalf("oversized request reached searcher with %q", searcher.gotQuery)
	}
}

// TestOversizedQueryIsToolError: a query past maxQueryBytes (but within the
// request-size cap) must come back as a tool-level isError, mirroring empty.
func TestOversizedQueryIsToolError(t *testing.T) {
	s := NewServer(&fakeSearcher{}, WithMaxQueryBytes(16))
	in := encodeLines(t, toolCall(1, strings.Repeat("q", 100)))
	var out bytes.Buffer
	if err := s.Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	resps := decodeResponses(t, &out)
	if len(resps) != 1 {
		t.Fatalf("want 1 response, got %d", len(resps))
	}
	if resps[0].Error != nil {
		t.Fatalf("oversized query should be a tool error, not a JSON-RPC error: %+v", resps[0].Error)
	}
	res := resps[0].Result.(map[string]interface{})
	if res["isError"] != true {
		t.Errorf("isError=%v, want true", res["isError"])
	}
}

// TestPanicInSearcherYieldsErrorNotCrash: a panicking searcher must produce a
// JSON-RPC internal error and not kill Serve.
func TestPanicInSearcherYieldsErrorNotCrash(t *testing.T) {
	s := NewServer(panicSearcher{})
	// Two calls: the second proves the loop survived the first panic.
	in := encodeLines(t, toolCall(1, "boom"), toolCall(2, "boom2"))
	var out bytes.Buffer

	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), in, &out) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve should not error on handler panic: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve hung after panic")
	}

	resps := decodeResponses(t, &out)
	if len(resps) != 2 {
		t.Fatalf("want 2 responses (loop survived), got %d", len(resps))
	}
	for _, r := range resps {
		if r.Error == nil || r.Error.Code != codeInternalError {
			t.Errorf("id %d: want internal error from panic, got %+v", idOf(t, r), r.Error)
		}
	}
}

// TestParentContextCancellationStops: cancelling the parent ctx must stop Serve.
func TestParentContextCancellationStops(t *testing.T) {
	bs := &blockingSearcher{release: make(chan struct{})} // never released
	s := NewServer(bs)
	ctx, cancel := context.WithCancel(context.Background())
	in := encodeLines(t, toolCall(1, "needle"))
	var out bytes.Buffer

	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, in, &out) }()

	// Wait until the handler is in-flight, then cancel.
	for atomic.LoadInt32(&bs.inFlight) < 1 {
		time.Sleep(time.Millisecond)
	}
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not stop on parent cancellation")
	}
}

// TestBlankLinesAndNotificationsSkipped: blank lines and notifications produce
// no replies, and a following request is still answered.
func TestBlankLinesAndNotificationsSkipped(t *testing.T) {
	s := NewServer(&fakeSearcher{})
	var in bytes.Buffer
	in.WriteString("\n")
	enc := json.NewEncoder(&in)
	_ = enc.Encode(legacyInitialize("bootstrap"))
	_ = enc.Encode(map[string]interface{}{"jsonrpc": "2.0", "method": "notifications/initialized"})
	_ = enc.Encode(map[string]interface{}{"jsonrpc": "2.0", "method": "notifications/initialized"})
	in.WriteString("   \n")
	_ = enc.Encode(map[string]interface{}{"jsonrpc": "2.0", "id": 9, "method": "tools/list"})

	var out bytes.Buffer
	if err := s.Serve(context.Background(), &in, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	resps := decodeResponses(t, &out)
	if len(resps) != 1 {
		t.Fatalf("want 1 response (only tools/list), got %d", len(resps))
	}
	if idOf(t, resps[0]) != 9 {
		t.Errorf("want reply for id 9, got %d", idOf(t, resps[0]))
	}
}
