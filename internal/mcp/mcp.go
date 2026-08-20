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
	"moedex/internal/graph"
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
	// defaultMaxBatchSize caps the number of messages in a single Streamable
	// HTTP JSON-RPC batch array. Without a cap, one POST within maxRequestBytes
	// can still pack thousands of tools/call messages, each processed serially
	// in handleHTTPBatch under its own requestTimeout — turning a single
	// request into hours of held CPU. 32 comfortably covers legitimate legacy
	// batch use while keeping the worst case bounded.
	defaultMaxBatchSize = 32
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
	maxBatchSize    int

	// corpusRoot is the directory the corpus was built under (cfg.Root), if known.
	// The mirror is laid out as <corpusRoot>/<path_with_namespace>, so a block's
	// abs_path lets us recover the repo's full namespace for the structured block.
	// Empty when unknown (e.g. single-repo serving), which omits the field rather
	// than fabricating one.
	corpusRoot string

	// extraTools are additional tools registered alongside the built-in
	// search_context (e.g. the LSP navigation tools the warm daemon exposes under
	// -tags lsp). order preserves tools/list ordering; byName dispatches calls.
	extraOrder []ToolHandler
	byName     map[string]ToolHandler

	// graph, when wired via WithGraphAnnotator, fuses the graph layer into
	// search_context: each returned block is annotated with its graph
	// neighborhood. Nil leaves search_context un-annotated.
	graph GraphAnnotator
}

// ToolHandler is an additional MCP tool plugged into the server. It owns its
// tools/list descriptor and its tools/call handling. Call receives the raw
// `arguments` object of the tools/call request and returns an MCP result map
// (use TextResult for the common case). Implementations must be safe for
// concurrent calls — the server dispatches requests on bounded worker goroutines.
type ToolHandler interface {
	Name() string
	Descriptor() map[string]interface{}
	Call(ctx context.Context, arguments json.RawMessage) (map[string]interface{}, error)
}

// WithTools registers extra tools alongside search_context. Names must be unique
// and must not be "search_context"; duplicates and that reserved name are
// ignored. Used by the warm daemon to add LSP navigation tools under -tags lsp.
func WithTools(tools ...ToolHandler) Option {
	return func(s *Server) {
		for _, t := range tools {
			if t == nil || t.Name() == "" || t.Name() == "search_context" {
				continue
			}
			if _, dup := s.byName[t.Name()]; dup {
				continue
			}
			s.byName[t.Name()] = t
			s.extraOrder = append(s.extraOrder, t)
		}
	}
}

// TextResult builds a plain-text MCP tools/call result. isError reports a
// tool-level failure (not a transport error). Exported so ToolHandler
// implementations in other packages can produce results in the standard shape.
func TextResult(text string, isError bool) map[string]interface{} { return textResult(text, isError) }

// StructuredResult builds an MCP tools/call result with a short text fallback
// and a typed machine-readable payload. Graph tools use this path so confidence
// tiers and evidence links remain structured instead of being flattened into
// presentation text.
func StructuredResult(text string, structured any, isError bool) map[string]interface{} {
	return map[string]interface{}{
		"content": []interface{}{
			map[string]interface{}{"type": "text", "text": text},
		},
		"structuredContent": structured,
		"isError":           isError,
	}
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

// WithMaxBatchSize caps the number of messages accepted in a single
// Streamable HTTP JSON-RPC batch array (see handleHTTPBatch). A value <= 0
// leaves the default in place.
func WithMaxBatchSize(n int) Option {
	return func(s *Server) {
		if n > 0 {
			s.maxBatchSize = n
		}
	}
}

// WithCorpusRoot tells the server the directory the corpus was built under, so
// the structured block can carry each hit's full path_with_namespace (the corpus
// is laid out as <root>/<path_with_namespace>). An empty value leaves the field
// omitted — never fabricated. Used by the warm daemon / offline indexer, which
// recover root from the served shard dir's manifest.
func WithCorpusRoot(root string) Option {
	return func(s *Server) {
		s.corpusRoot = root
	}
}

// NewServer builds a Server backed by searcher. With no options it uses the
// hardening defaults (30s timeout, 8-way concurrency, 1 MiB request cap, 8 KiB
// query cap, 32-message HTTP batch cap).
func NewServer(searcher ContextSearcher, opts ...Option) *Server {
	s := &Server{
		searcher:        searcher,
		name:            "moedex",
		version:         "0.1.0",
		requestTimeout:  defaultRequestTimeout,
		maxConcurrency:  defaultMaxConcurrency,
		maxRequestBytes: defaultMaxRequestBytes,
		maxQueryBytes:   defaultMaxQueryBytes,
		maxBatchSize:    defaultMaxBatchSize,
		byName:          make(map[string]ToolHandler),
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
		tools := []interface{}{toolDescriptor()}
		for _, t := range s.extraOrder {
			tools = append(tools, t.Descriptor())
		}
		base.Result = map[string]interface{}{"tools": tools}
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
		"description": "Search the indexed code corpus and return ranked, deduplicated, token-budgeted context blocks for an LLM agent, each annotated with its code-graph neighborhood (callers, callees, consumers, publishers, dependencies, semantic siblings).",
		"inputSchema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query":        map[string]interface{}{"type": "string", "description": "The search query (keywords or identifiers)."},
				"token_budget": map[string]interface{}{"type": "integer", "description": "Maximum tokens for the returned context (optional)."},
				"top_k":        map[string]interface{}{"type": "integer", "description": "Maximum number of ranked results to draw blocks from (optional)."},
				"format":       map[string]interface{}{"type": "string", "enum": []string{"text", "structured"}, "description": "Output format (optional): \"text\" (default) renders agent-readable blocks; \"structured\" returns a typed JSON payload in structuredContent with per-block provenance (blob id, fused score, BM25 and dense components) for machine consumers."},
				"graph_depth": map[string]interface{}{
					"type":        "integer",
					"minimum":     0,
					"maximum":     MaxGraphDepth,
					"description": fmt.Sprintf("Graph annotation radius in hops (optional, default %d). Each result block carries a \"neighbors\" field with the callers, callees, consumers, publishers, dependencies, and semantic siblings of the symbols it contains — no second graph tool call needed. 0 disables the annotation.", DefaultGraphDepth),
				},
				"min_confidence": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"Candidate", "Pattern", "Verified", "Proven"},
					"description": "Minimum graph-edge confidence included in annotations (optional, default Pattern). Edges below the floor are excluded before traversal.",
				},
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
		// GraphDepth is a pointer so an omitted argument (DefaultGraphDepth) is
		// distinguishable from an explicit 0 (annotation off).
		GraphDepth    *int   `json:"graph_depth"`
		MinConfidence string `json:"min_confidence"`
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
		// Dispatch to a registered extra tool (e.g. LSP navigation) by name,
		// handing it the raw arguments object.
		if h := s.byName[p.Name]; h != nil {
			var head struct {
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(raw, &head)
			return h.Call(ctx, head.Arguments)
		}
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
	depth := DefaultGraphDepth
	if p.Arguments.GraphDepth != nil {
		depth = *p.Arguments.GraphDepth
	}
	if depth < 0 || depth > MaxGraphDepth {
		return textResult(fmt.Sprintf("graph_depth must be between 0 and %d", MaxGraphDepth), true), nil
	}
	minConfidence, err := graph.ParseMinConfidence(p.Arguments.MinConfidence)
	if err != nil {
		return textResult(err.Error(), true), nil
	}

	win, err := s.searcher.SearchContext(ctx, p.Arguments.Query, p.Arguments.TokenBudget, p.Arguments.TopK)
	if err != nil {
		return nil, err
	}
	neighbors, err := s.annotate(ctx, win.Blocks, depth, minConfidence)
	if err != nil {
		return nil, err
	}
	if p.Arguments.Format == "structured" {
		return structuredResult(win, s.corpusRoot, neighbors), nil
	}
	return textResult(formatWindow(win, neighbors), false), nil
}

// annotate resolves the graph neighborhood of win's blocks, index-aligned with
// them. It returns nil — meaning "leave the response un-annotated" — when no
// annotator is wired, when the caller passed graph_depth=0, when there are no
// blocks, or when the annotator reports that no graph is available.
//
// A length mismatch is a contract violation in the annotator, not a partial
// result, so it surfaces as an error rather than silently mis-attributing one
// block's neighborhood to another.
func (s *Server) annotate(ctx context.Context, blocks []contextwin.ContextBlock, depth int, minConfidence graph.ConfidenceTier) ([]BlockNeighbors, error) {
	if s.graph == nil || depth <= 0 || len(blocks) == 0 {
		return nil, nil
	}
	var neighbors []BlockNeighbors
	var err error
	if aware, ok := s.graph.(ConfidenceGraphAnnotator); ok {
		neighbors, err = aware.NeighborsWithConfidence(ctx, blocks, depth, minConfidence)
	} else {
		neighbors, err = s.graph.Neighbors(ctx, blocks, depth)
	}
	if err != nil {
		return nil, fmt.Errorf("graph annotation: %w", err)
	}
	if neighbors == nil {
		return nil, nil
	}
	if len(neighbors) != len(blocks) {
		return nil, fmt.Errorf("graph annotation: %d neighbor set(s) for %d block(s)", len(neighbors), len(blocks))
	}
	return neighbors, nil
}

// neighborsAt returns the annotation for block i, or nil when the response is
// un-annotated.
func neighborsAt(neighbors []BlockNeighbors, i int) *BlockNeighbors {
	if i < 0 || i >= len(neighbors) {
		return nil
	}
	return &neighbors[i]
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
// followed by each block under a "path:start-end (score)" header. When the block
// carries a graph annotation with at least one neighbor, a single "[graph] ..."
// line sits between the header and the code; a block with no neighbors (or an
// un-annotated response) renders exactly as it did before graph fusion.
func formatWindow(win contextwin.ContextWindow, neighbors []BlockNeighbors) string {
	if len(win.Blocks) == 0 {
		return "No matching context found."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d context block(s), ~%d tokens", len(win.Blocks), win.TokenEstimate)
	writeWindowStatus(&b, win)
	b.WriteString("\n")
	for i, blk := range win.Blocks {
		fmt.Fprintf(&b, "\n--- %s:%d-%d (score %.4f)", blk.RelPath, blk.StartLine, blk.EndLine, blk.Score)
		if blk.Clipped {
			b.WriteString(" [clipped]")
		}
		b.WriteString(" ---\n")
		if line := renderNeighbors(neighborsAt(neighbors, i)); line != "" {
			b.WriteString(line)
			b.WriteString("\n")
		}
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
	Clipped       bool `json:"clipped"`
}

type structuredBlock struct {
	Blob uint64 `json:"blob"`
	Repo string `json:"repo"`
	// PathWithNamespace is the repo's full GitLab namespace path (e.g.
	// "Services.Domains/TC.MarketplaceApi"), recovered from abs_path relative to
	// the corpus root. It is what an agent needs to actually clone the repo; Repo
	// (the bare leaf) is kept for back-compat. Omitted when the corpus root is
	// unknown or abs_path doesn't fall under it (never fabricated).
	PathWithNamespace string  `json:"path_with_namespace,omitempty"`
	RelPath           string  `json:"rel_path"`
	AbsPath           string  `json:"abs_path"`
	StartLine         int     `json:"start_line"`
	EndLine           int     `json:"end_line"`
	Score             float64 `json:"score"`
	Lexical           float64 `json:"lexical"`
	Dense             float64 `json:"dense"` // 0 when the dense arm did not fire (treat as "arm absent")
	Text              string  `json:"text"`
	Clipped           bool    `json:"clipped"`
	// Neighbors is the block's graph neighborhood (phase 14). It is nil — and the
	// field omitted — when the response is un-annotated (no graph wired or
	// graph_depth=0); a block whose symbols have no edges carries a PRESENT
	// annotation with empty buckets, so "off" and "nothing found" stay distinct.
	Neighbors *BlockNeighbors `json:"neighbors,omitempty"`
}

// deriveNamespace recovers a repo's full path_with_namespace from a block's
// absolute path. The corpus mirror is laid out as <root>/<path_with_namespace>,
// and abs = <root>/<path_with_namespace>/<rel>, so stripping the rel suffix from
// abs yields the repo dir and stripping the root prefix yields the namespace. It
// returns "" (field omitted) whenever the namespace cannot be derived honestly:
// no root, abs not under root, or rel not a suffix of abs. Never fabricates.
func deriveNamespace(abs, rel, root string) string {
	if root == "" || abs == "" {
		return ""
	}
	repoDir := strings.TrimSuffix(abs, rel)
	if repoDir == abs {
		return "" // rel was not a suffix of abs — can't trust the split
	}
	repoDir = strings.TrimRight(repoDir, "/")
	root = strings.TrimRight(root, "/")
	if repoDir == root {
		return "" // the repo IS the root (no namespace level)
	}
	prefix := root + "/"
	if !strings.HasPrefix(repoDir, prefix) {
		return "" // abs path does not fall under the corpus root
	}
	return strings.TrimPrefix(repoDir, prefix)
}

func newStructuredWindow(win contextwin.ContextWindow, corpusRoot string, neighbors []BlockNeighbors) structuredWindow {
	sw := structuredWindow{
		Summary: structuredSummary{
			Blocks:        len(win.Blocks),
			TokenEstimate: win.TokenEstimate,
			Truncated:     win.Truncated,
			Clipped:       win.Clipped,
		},
		Blocks: make([]structuredBlock, 0, len(win.Blocks)),
	}
	for i, blk := range win.Blocks {
		sw.Blocks = append(sw.Blocks, structuredBlock{
			Blob:              blk.Blob,
			Repo:              blk.Repo,
			PathWithNamespace: deriveNamespace(blk.AbsPath, blk.RelPath, corpusRoot),
			RelPath:           blk.RelPath,
			AbsPath:           blk.AbsPath,
			StartLine:         blk.StartLine,
			EndLine:           blk.EndLine,
			Score:             blk.Score,
			Lexical:           blk.Lexical,
			Dense:             blk.Dense,
			Text:              blk.Text,
			Clipped:           blk.Clipped,
			Neighbors:         neighborsAt(neighbors, i),
		})
	}
	return sw
}

// structuredResult renders a ContextWindow as an MCP tool result carrying both a
// short text fallback (content) and the typed payload (structuredContent), per
// ADR 0015. structuredContent is the machine channel; the text content keeps MCP
// clients that ignore structuredContent functional.
func structuredResult(win contextwin.ContextWindow, corpusRoot string, neighbors []BlockNeighbors) map[string]interface{} {
	summary := fmt.Sprintf("%d context block(s), ~%d tokens", len(win.Blocks), win.TokenEstimate)
	var status strings.Builder
	writeWindowStatus(&status, win)
	summary += status.String()
	if n := totalNeighbors(neighbors); n > 0 {
		summary += fmt.Sprintf(", %d graph neighbor(s)", n)
	}
	return StructuredResult(summary, newStructuredWindow(win, corpusRoot, neighbors), false)
}

// writeWindowStatus appends independent labels for narrowed source and omitted
// lower-ranked candidates. Keeping them separate prevents "truncated" from
// ambiguously covering both conditions.
func writeWindowStatus(b *strings.Builder, win contextwin.ContextWindow) {
	if win.Clipped {
		b.WriteString(" (source clipped to fit budget)")
	}
	if win.Truncated {
		b.WriteString(" (lower-ranked context omitted to fit budget)")
	}
}

// totalNeighbors counts every annotated neighbor across every block.
func totalNeighbors(neighbors []BlockNeighbors) int {
	total := 0
	for i := range neighbors {
		total += neighbors[i].Total()
	}
	return total
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
