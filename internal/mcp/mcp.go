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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"moedex/internal/contextwin"
	"moedex/internal/index"
	"moedex/internal/rank"
)

// protocolVersion is the MCP revision this server speaks.
const protocolVersion = "2024-11-05"

// ContextSearcher is the retrieval dependency the server calls per tool
// invocation. tokenBudget and topK of 0 mean "use defaults".
type ContextSearcher interface {
	SearchContext(ctx context.Context, query string, tokenBudget, topK int) (contextwin.ContextWindow, error)
}

// Server speaks MCP over a reader/writer pair.
type Server struct {
	searcher ContextSearcher
	name     string
	version  string
}

// NewServer builds a Server backed by searcher.
func NewServer(searcher ContextSearcher) *Server {
	return &Server{searcher: searcher, name: "moedex", version: "0.1.0"}
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
	codeMethodNotFound = -32601
	codeInternalError  = -32603
)

// Serve runs the JSON-RPC loop until r is exhausted or ctx is cancelled.
// Notifications (requests without an id) are processed but not answered.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	dec := json.NewDecoder(r)
	enc := json.NewEncoder(w)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var req request
		if err := dec.Decode(&req); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		resp, respond := s.handle(ctx, &req)
		if !respond {
			continue // notification: no reply
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
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
		result, err := s.callTool(ctx, req.Params)
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

	win, err := s.searcher.SearchContext(ctx, p.Arguments.Query, p.Arguments.TokenBudget, p.Arguments.TopK)
	if err != nil {
		return nil, err
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
