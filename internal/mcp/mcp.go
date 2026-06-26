// Package mcp exposes moedex as an agent tool over the Model Context Protocol's
// stdio transport: newline-delimited JSON-RPC 2.0 on stdin/stdout. It serves one
// tool, search_context, that runs a ranked search and returns the assembled,
// token-budgeted context window — the index as an agent tool, not a grep server.
//
// The protocol handling is decoupled from retrieval via ContextSearcher so the
// JSON-RPC layer is testable with a fake, and the real wiring (rank ->
// contextwin) lives in the IndexSearcher adapter.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"moedex/internal/contextwin"
	"moedex/internal/index"
	"moedex/internal/rank"
)

// protocolVersion is the MCP revision this server speaks.
const protocolVersion = "2024-11-05"

// Hardening defaults. These keep NewServer working unchanged while giving the
// server sane bounds for live multi-agent traffic. Override via the With*
// options on NewServer.
const (
	// defaultRequestTimeout bounds a single tools/call search so one hung or
	// slow query cannot wedge the server.
	defaultRequestTimeout = 30 * time.Second
	// defaultMaxConcurrency bounds the number of in-flight handlers. The output
	// stream is serialized regardless; this caps CPU/search fan-out.
	defaultMaxConcurrency = 8
	// defaultMaxRequestBytes caps a single newline-delimited JSON-RPC line so a
	// giant request can't OOM the decoder.
	defaultMaxRequestBytes = 1 << 20 // 1 MiB
	// defaultMaxQueryBytes caps the search query string itself; oversized
	// queries are rejected as a tool-level error, mirroring the empty-query path.
	defaultMaxQueryBytes = 8 << 10 // 8 KiB
)

// ContextSearcher is the retrieval dependency the server calls per tool
// invocation. tokenBudget and topK of 0 mean "use defaults".
type ContextSearcher interface {
	SearchContext(ctx context.Context, query string, tokenBudget, topK int) (contextwin.ContextWindow, error)
}

// Server speaks MCP over a reader/writer pair. It is hardened for concurrent
// live traffic: requests are handled with bounded concurrency, each tools/call
// runs under a per-request timeout, writes to the output stream are serialized,
// incoming request size is capped, and a panic in a handler is recovered into a
// JSON-RPC internal error rather than killing the loop.
type Server struct {
	searcher ContextSearcher
	name     string
	version  string

	requestTimeout  time.Duration
	maxConcurrency  int
	maxRequestBytes int
	maxQueryBytes   int
}

// Option configures a Server. All options are safe to omit; NewServer applies
// sane defaults so existing callers keep working unchanged.
type Option func(*Server)

// WithRequestTimeout sets the per-tools/call timeout. A value <= 0 leaves the
// default in place.
func WithRequestTimeout(d time.Duration) Option {
	return func(s *Server) {
		if d > 0 {
			s.requestTimeout = d
		}
	}
}

// WithMaxConcurrency caps the number of in-flight request handlers. A value
// <= 0 leaves the default in place. Output framing is serialized regardless of
// this setting.
func WithMaxConcurrency(n int) Option {
	return func(s *Server) {
		if n > 0 {
			s.maxConcurrency = n
		}
	}
}

// WithMaxRequestBytes caps the size of a single incoming JSON-RPC message. A
// value <= 0 leaves the default in place.
func WithMaxRequestBytes(n int) Option {
	return func(s *Server) {
		if n > 0 {
			s.maxRequestBytes = n
		}
	}
}

// WithMaxQueryBytes caps the size of the search query string. A value <= 0
// leaves the default in place.
func WithMaxQueryBytes(n int) Option {
	return func(s *Server) {
		if n > 0 {
			s.maxQueryBytes = n
		}
	}
}

// NewServer builds a Server backed by searcher. With no options it uses the
// hardening defaults (30s timeout, 8-way concurrency, 1 MiB request cap, 8 KiB
// query cap).
func NewServer(searcher ContextSearcher, opts ...Option) *Server {
	s := &Server{
		searcher:        searcher,
		name:            "moedex",
		version:         "0.1.0",
		requestTimeout:  defaultRequestTimeout,
		maxConcurrency:  defaultMaxConcurrency,
		maxRequestBytes: defaultMaxRequestBytes,
		maxQueryBytes:   defaultMaxQueryBytes,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// JSON-RPC envelopes.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // absent on notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInternalError  = -32603
)

// Serve runs the JSON-RPC loop until r is exhausted or ctx is cancelled.
// Notifications (requests without an id) are processed but not answered.
//
// Concurrency model: the read loop decodes requests sequentially (a single
// reader, as the transport requires), then dispatches each request to a worker
// goroutine bounded by maxConcurrency. Workers compute responses concurrently
// and hand them to a single dedicated writer goroutine over a channel; that
// writer owns the sole json.Encoder, so output framing is never interleaved.
// Because handlers run concurrently, responses MAY be emitted out of request
// order — this is legal under JSON-RPC since each reply carries the request's
// id. Notifications produce no reply. Each tools/call runs under a derived
// context with requestTimeout (and honors parent-ctx cancellation); a panic in
// any handler is recovered into a JSON-RPC internal error.
//
// Concurrency-safety assumption: the ContextSearcher implementation is safe for
// concurrent SearchContext calls. The production impl (IndexSearcher over a
// read-only rank.Ranker + index.Index) is read-only and satisfies this.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Read one newline-delimited JSON-RPC message at a time, capping each line so
	// a giant message can't OOM us. bufio.Scanner enforces the per-line size with
	// bufio.ErrTooLong; we surface that as a parse error and stop, since a
	// newline-delimited stream cannot be reliably resynced after an oversized or
	// malformed line.
	sc := bufio.NewScanner(r)
	// Initial buffer grows as needed up to maxRequestBytes; past that bufio
	// returns ErrTooLong. Keep the initial allocation modest and never larger
	// than the cap.
	initBuf := 64 << 10
	if initBuf > s.maxRequestBytes {
		initBuf = s.maxRequestBytes
	}
	sc.Buffer(make([]byte, 0, initBuf), s.maxRequestBytes)

	// Single writer goroutine owns the encoder; all responses flow through outCh.
	enc := json.NewEncoder(w)
	outCh := make(chan response, s.maxConcurrency)
	writeDone := make(chan struct{})
	var writeErr error
	go func() {
		defer close(writeDone)
		for resp := range outCh {
			if err := enc.Encode(resp); err != nil && writeErr == nil {
				writeErr = err
				cancel() // stop accepting/dispatching new work
			}
		}
	}()

	sem := make(chan struct{}, s.maxConcurrency)
	var wg sync.WaitGroup

	var loopErr error
	for {
		if err := ctx.Err(); err != nil {
			loopErr = err
			break
		}
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				if err == bufio.ErrTooLong {
					// Oversized line: emit a parse error (no id available) and stop
					// gracefully — framing past the truncated line is unrecoverable,
					// but this is a client error, not a server failure, so Serve
					// returns nil after draining.
					select {
					case outCh <- response{JSONRPC: "2.0", Error: &rpcError{Code: codeParseError, Message: "request exceeds maximum size"}}:
					case <-ctx.Done():
					}
				} else {
					loopErr = err
				}
			}
			break // EOF or error
		}
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue // skip blank lines between messages
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			// Malformed JSON: reply with a parse error (id is unknown -> null) and
			// keep going; a single bad line is line-bounded, so the stream stays
			// framed.
			select {
			case outCh <- response{JSONRPC: "2.0", Error: &rpcError{Code: codeParseError, Message: "parse error: " + err.Error()}}:
			case <-ctx.Done():
				loopErr = ctx.Err()
			}
			if loopErr != nil {
				break
			}
			continue
		}

		// Acquire a slot before spawning. Respect cancellation while waiting.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			loopErr = ctx.Err()
		}
		if loopErr != nil {
			break
		}

		wg.Add(1)
		go func(req request) {
			defer wg.Done()
			defer func() { <-sem }()
			resp, respond := s.handleSafe(ctx, &req)
			if !respond {
				return // notification: no reply
			}
			select {
			case outCh <- resp:
			case <-ctx.Done():
			}
		}(req)
	}

	// Wait for in-flight handlers, then drain the writer.
	wg.Wait()
	close(outCh)
	<-writeDone

	if writeErr != nil {
		return writeErr
	}
	if loopErr != nil && loopErr != context.Canceled {
		return loopErr
	}
	// A clean parent-ctx cancellation surfaces as the ctx error, matching the
	// original Serve contract.
	if err := ctx.Err(); err != nil && writeErr == nil {
		return err
	}
	return nil
}

// handleSafe wraps handle with panic recovery so a buggy handler or searcher
// cannot kill Serve. A recovered panic on a request (non-notification) yields a
// JSON-RPC internal error; on a notification it is swallowed.
func (s *Server) handleSafe(ctx context.Context, req *request) (resp response, respond bool) {
	isNotification := len(req.ID) == 0
	defer func() {
		if r := recover(); r != nil {
			debug.PrintStack()
			if isNotification {
				resp, respond = response{}, false
				return
			}
			resp = response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &rpcError{Code: codeInternalError, Message: fmt.Sprintf("internal error: %v", r)},
			}
			respond = true
		}
	}()
	return s.handle(ctx, req)
}

// handle dispatches one request. The bool is false for notifications (no reply).
func (s *Server) handle(ctx context.Context, req *request) (response, bool) {
	isNotification := len(req.ID) == 0
	base := response{JSONRPC: "2.0", ID: req.ID}

	switch req.Method {
	case "initialize":
		base.Result = map[string]interface{}{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
			"serverInfo":      map[string]interface{}{"name": s.name, "version": s.version},
		}
		return base, !isNotification

	case "notifications/initialized", "initialized":
		return base, false // pure notification

	case "tools/list":
		base.Result = map[string]interface{}{"tools": []interface{}{toolDescriptor()}}
		return base, !isNotification

	case "tools/call":
		// Derive a per-request deadline so a slow/hung search can't wedge the
		// server; honors parent-ctx cancellation too.
		callCtx, cancel := context.WithTimeout(ctx, s.requestTimeout)
		defer cancel()
		result, err := s.callTool(callCtx, req.Params)
		if err != nil {
			base.Error = &rpcError{Code: codeInternalError, Message: err.Error()}
			return base, !isNotification
		}
		base.Result = result
		return base, !isNotification

	default:
		if isNotification {
			return base, false
		}
		base.Error = &rpcError{Code: codeMethodNotFound, Message: "unknown method: " + req.Method}
		return base, true
	}
}

func toolDescriptor() map[string]interface{} {
	return map[string]interface{}{
		"name":        "search_context",
		"description": "Search the indexed code corpus and return ranked, deduplicated, token-budgeted context blocks for an LLM agent.",
		"inputSchema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query":        map[string]interface{}{"type": "string", "description": "The search query (keywords or identifiers)."},
				"token_budget": map[string]interface{}{"type": "integer", "description": "Maximum tokens for the returned context (optional)."},
				"top_k":        map[string]interface{}{"type": "integer", "description": "Maximum number of ranked results to draw blocks from (optional)."},
				"format":       map[string]interface{}{"type": "string", "enum": []string{"text", "structured"}, "description": "Output format (optional): \"text\" (default) renders agent-readable blocks; \"structured\" returns a typed JSON payload in structuredContent with per-block provenance (blob id, fused score, BM25 and dense components) for machine consumers."},
			},
			"required": []string{"query"},
		},
	}
}

type callParams struct {
	Name      string `json:"name"`
	Arguments struct {
		Query       string `json:"query"`
		TokenBudget int    `json:"token_budget"`
		TopK        int    `json:"top_k"`
		Format      string `json:"format"`
	} `json:"arguments"`
}

// toolResult is an MCP tools/call result with text content. isError lets us
// report a tool-level failure (e.g. empty query) without a JSON-RPC error.
func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (map[string]interface{}, error) {
	var p callParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid tools/call params: %w", err)
	}
	if p.Name != "search_context" {
		return nil, fmt.Errorf("unknown tool: %s", p.Name)
	}
	if strings.TrimSpace(p.Arguments.Query) == "" {
		return textResult("query must not be empty", true), nil
	}
	if len(p.Arguments.Query) > s.maxQueryBytes {
		return textResult(fmt.Sprintf("query too large: %d bytes (max %d)", len(p.Arguments.Query), s.maxQueryBytes), true), nil
	}
	switch p.Arguments.Format {
	case "", "text", "structured":
		// supported
	default:
		return textResult(fmt.Sprintf("unknown format: %q (want \"text\" or \"structured\")", p.Arguments.Format), true), nil
	}

	win, err := s.searcher.SearchContext(ctx, p.Arguments.Query, p.Arguments.TokenBudget, p.Arguments.TopK)
	if err != nil {
		return nil, err
	}
	if p.Arguments.Format == "structured" {
		return structuredResult(win), nil
	}
	return textResult(formatWindow(win), false), nil
}

func textResult(text string, isError bool) map[string]interface{} {
	return map[string]interface{}{
		"content": []interface{}{
			map[string]interface{}{"type": "text", "text": text},
		},
		"isError": isError,
	}
}

// formatWindow renders a ContextWindow as agent-readable text: a summary line
// followed by each block under a "path:start-end (score)" header.
func formatWindow(win contextwin.ContextWindow) string {
	if len(win.Blocks) == 0 {
		return "No matching context found."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d context block(s), ~%d tokens", len(win.Blocks), win.TokenEstimate)
	if win.Truncated {
		b.WriteString(" (truncated to fit budget)")
	}
	b.WriteString("\n")
	for _, blk := range win.Blocks {
		fmt.Fprintf(&b, "\n--- %s:%d-%d (score %.4f) ---\n", blk.RelPath, blk.StartLine, blk.EndLine, blk.Score)
		b.WriteString(blk.Text)
	}
	return b.String()
}

// structuredWindow is the machine-readable form of a ContextWindow, returned when
// the caller passes format="structured" (ADR 0015). It carries the provenance the
// text rendering drops: content identity (Blob) for cross-call/source dedup and
// the raw per-arm scores (Lexical/Dense) behind the fused Score.
type structuredWindow struct {
	Summary structuredSummary `json:"summary"`
	Blocks  []structuredBlock `json:"blocks"`
}

type structuredSummary struct {
	Blocks        int  `json:"blocks"`
	TokenEstimate int  `json:"token_estimate"`
	Truncated     bool `json:"truncated"`
}

type structuredBlock struct {
	Blob      uint64  `json:"blob"`
	Repo      string  `json:"repo"`
	RelPath   string  `json:"rel_path"`
	AbsPath   string  `json:"abs_path"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Score     float64 `json:"score"`
	Lexical   float64 `json:"lexical"`
	Dense     float64 `json:"dense"` // 0 when the dense arm did not fire (treat as "arm absent")
	Text      string  `json:"text"`
}

func newStructuredWindow(win contextwin.ContextWindow) structuredWindow {
	sw := structuredWindow{
		Summary: structuredSummary{
			Blocks:        len(win.Blocks),
			TokenEstimate: win.TokenEstimate,
			Truncated:     win.Truncated,
		},
		Blocks: make([]structuredBlock, 0, len(win.Blocks)),
	}
	for _, blk := range win.Blocks {
		sw.Blocks = append(sw.Blocks, structuredBlock{
			Blob:      blk.Blob,
			Repo:      blk.Repo,
			RelPath:   blk.RelPath,
			AbsPath:   blk.AbsPath,
			StartLine: blk.StartLine,
			EndLine:   blk.EndLine,
			Score:     blk.Score,
			Lexical:   blk.Lexical,
			Dense:     blk.Dense,
			Text:      blk.Text,
		})
	}
	return sw
}

// structuredResult renders a ContextWindow as an MCP tool result carrying both a
// short text fallback (content) and the typed payload (structuredContent), per
// ADR 0015. structuredContent is the machine channel; the text content keeps MCP
// clients that ignore structuredContent functional.
func structuredResult(win contextwin.ContextWindow) map[string]interface{} {
	summary := fmt.Sprintf("%d context block(s), ~%d tokens", len(win.Blocks), win.TokenEstimate)
	if win.Truncated {
		summary += " (truncated to fit budget)"
	}
	return map[string]interface{}{
		"content": []interface{}{
			map[string]interface{}{"type": "text", "text": summary},
		},
		"structuredContent": newStructuredWindow(win),
		"isError":           false,
	}
}

// IndexSearcher adapts a Ranker + index into a ContextSearcher by assembling the
// ranked results into a context window.
type IndexSearcher struct {
	ix     *index.Index
	ranker *rank.Ranker
	// candidateTopK bounds how many ranked results feed assembly when the caller
	// passes top_k=0.
	candidateTopK int
	// enclosing, when set, scopes context blocks to real symbol boundaries
	// (see contextwin.Options.EnclosingBytes); nil keeps the brace/indent heuristic.
	enclosing func(blob uint64, byteOff int) (startByte, endByte int, ok bool)
}

// NewIndexSearcher wires a ranker and its index into a searcher. defaultTopK is
// used when a call passes top_k <= 0.
func NewIndexSearcher(ix *index.Index, ranker *rank.Ranker, defaultTopK int) *IndexSearcher {
	if defaultTopK <= 0 {
		defaultTopK = 20
	}
	return &IndexSearcher{ix: ix, ranker: ranker, candidateTopK: defaultTopK}
}

// SetEnclosingBytes enables symbol-scoped context blocks by supplying the
// enclosing-symbol lookup (e.g. (*symbol.Index).EnclosingBytesFunc()). Passing
// nil leaves block scoping on the brace/indent heuristic.
func (a *IndexSearcher) SetEnclosingBytes(fn func(blob uint64, byteOff int) (startByte, endByte int, ok bool)) {
	a.enclosing = fn
}

// SearchContext implements ContextSearcher.
func (a *IndexSearcher) SearchContext(ctx context.Context, query string, tokenBudget, topK int) (contextwin.ContextWindow, error) {
	if topK <= 0 {
		topK = a.candidateTopK
	}
	results, err := a.ranker.Rank(ctx, query, topK)
	if err != nil {
		return contextwin.ContextWindow{}, err
	}
	return contextwin.Assemble(a.ix, results, contextwin.Options{
		TokenBudget:    tokenBudget,
		EnclosingBytes: a.enclosing,
	}), nil
}
