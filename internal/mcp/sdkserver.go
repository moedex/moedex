package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	sdkjsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"moedex/internal/version"
)

const catalogCacheTTL = 5 * time.Minute

const serverInstructions = "Use compiler_trace_calls with exact method IDs and explicitly selected contexts when compiler evidence is available; trace_calls returns confidence-scored graph candidates. " +
	"Use search_context with an explicit token_budget to retrieve ranked code context. " +
	"Check list_repos.graph_available before graph traversal; false leaves source discovery available and says nothing about compiler or LSP support. " +
	"Clients reading structuredContent can request format=structured on search_context and read_source to avoid duplicate source in the text fallback. " +
	"Text source excerpts number lines for citations; cite relevant lines and verify narrow ranges with read_source. Structured source text remains unnumbered for hash and compiler-offset verification. " +
	"Track requirements from the user's request and review relevant success, rejection, cancellation, and failure terminal paths before answering; explicitly mark unresolved requirements or paths when evidence is unavailable. Source presence alone does not establish that a requirement is answered. " +
	"For field-projection comparisons, reconcile the requested source fields with destination assignments and filtering predicates. Support negative selection claims with the complete bounded conversion body; a missing search hit or field-name similarity alone is insufficient. " +
	"Maintain a claim-specific citation ledger binding repository, path, blob or snapshot identity, and the complete displayed line span. Clipped or unavailable tails are not citation evidence. Keep interface declarations and implementation bodies in separate claims, and verify each citation against its own source. Range and identity checks do not certify semantic support. " +
	"Pattern is the default graph-confidence floor; request a different min_confidence only when needed. " +
	"Before composing graph results from multiple calls, compare corpus_fingerprint, graph_generation, and graph_build_id in dev.moedex/snapshot. " +
	"Prefer returned blob_sha values for content-level cache keys. " +
	"For navigation, ready_empty means the language server answered with no locations, unsupported means it lacks the method, and unavailable means no authoritative result was produced. " +
	"The format argument is retained for compatibility and changes only the text fallback; structuredContent is always returned."

type officialServer struct{ server *sdkmcp.Server }

func (s *Server) configureOfficial() {
	impl := &sdkmcp.Implementation{
		Name:        s.name,
		Title:       "Moedex code retrieval",
		Description: "Single-node indexed code search, graph traversal, and navigation",
		Version:     version.MCP(),
	}
	s.version = impl.Version
	instructions := serverInstructions
	if s.extraInstructions != "" {
		instructions += "\n\n" + s.extraInstructions
	}
	official := sdkmcp.NewServer(impl, &sdkmcp.ServerOptions{
		Instructions: instructions,
		Capabilities: &sdkmcp.ServerCapabilities{Tools: &sdkmcp.ToolCapabilities{ListChanged: false}},
	})

	sem := make(chan struct{}, s.maxConcurrency)
	official.AddReceivingMiddleware(
		func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
			return func(ctx context.Context, method string, req sdkmcp.Request) (result sdkmcp.Result, err error) {
				defer func() {
					if recovered := recover(); recovered != nil {
						debug.PrintStack()
						result = nil
						err = &sdkjsonrpc.Error{Code: sdkjsonrpc.CodeInternalError, Message: fmt.Sprintf("internal error: %v", recovered)}
					}
				}()
				return next(ctx, method, req)
			}
		},
		func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
			return func(ctx context.Context, method string, req sdkmcp.Request) (sdkmcp.Result, error) {
				if method != "tools/call" {
					return next(ctx, method, req)
				}
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					return nil, &sdkjsonrpc.Error{Code: sdkjsonrpc.CodeInternalError, Message: ctx.Err().Error()}
				}
				callCtx, cancel := context.WithTimeout(ctx, s.requestTimeout)
				defer cancel()
				return next(callCtx, method, req)
			}
		},
		func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
			return func(ctx context.Context, method string, req sdkmcp.Request) (sdkmcp.Result, error) {
				result, err := next(ctx, method, req)
				if err != nil {
					return nil, err
				}
				switch value := result.(type) {
				case *sdkmcp.DiscoverResult:
					value.SupportedVersions = []string{protocolVersion, legacyProtocolVersion}
					value.TTLMs = int(catalogCacheTTL / time.Millisecond)
					value.CacheScope = "public"
				case *sdkmcp.ListToolsResult:
					value.TTLMs = int(catalogCacheTTL / time.Millisecond)
					value.CacheScope = "public"
				}
				return result, nil
			}
		},
	)

	register := func(spec ToolSpecification, call func(context.Context, json.RawMessage) (map[string]interface{}, error)) {
		openWorld, destructive := false, false
		tool := &sdkmcp.Tool{
			Name:         spec.Name,
			Description:  spec.Description,
			InputSchema:  spec.InputSchema,
			OutputSchema: spec.OutputSchema,
			Annotations: &sdkmcp.ToolAnnotations{
				ReadOnlyHint: true, DestructiveHint: &destructive,
				IdempotentHint: true, OpenWorldHint: &openWorld,
			},
		}
		official.AddTool(tool, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			var raw json.RawMessage
			if req != nil && req.Params != nil {
				raw = req.Params.Arguments
			}
			result, err := call(ctx, raw)
			if err != nil {
				return nil, &sdkjsonrpc.Error{Code: sdkjsonrpc.CodeInternalError, Message: err.Error()}
			}
			return toOfficialResult(result), nil
		})
	}

	register(specificationFromDescriptor(toolDescriptor()), func(ctx context.Context, args json.RawMessage) (map[string]interface{}, error) {
		params, err := json.Marshal(map[string]interface{}{"name": "search_context", "arguments": json.RawMessage(args)})
		if err != nil {
			return nil, err
		}
		return s.callTool(ctx, params)
	})
	for _, handler := range s.extraOrder {
		h := handler
		register(h.Specification(), h.Call)
	}
	s.official = &officialServer{server: official}
}

func specificationFromDescriptor(desc map[string]interface{}) ToolSpecification {
	name, _ := desc["name"].(string)
	description, _ := desc["description"].(string)
	input, _ := desc["inputSchema"].(map[string]interface{})
	if input == nil {
		input = map[string]interface{}{"type": "object"}
	}
	return NewToolSpecification(name, description, input)
}

func toOfficialResult(raw map[string]interface{}) *sdkmcp.CallToolResult {
	if raw == nil {
		raw = StructuredToolError("internal_error", "handler returned no result", SnapshotIdentity{Cacheable: false})
	}
	meta := sdkmcp.Meta{}
	if value, ok := raw["_meta"].(map[string]interface{}); ok {
		for key, item := range value {
			meta[key] = item
		}
	}
	structured := raw["structuredContent"]
	if structured == nil {
		structured = map[string]interface{}{"error": map[string]interface{}{"code": "invalid_result", "message": "tool returned no structured content"}}
	}
	isError, _ := raw["isError"].(bool)
	return &sdkmcp.CallToolResult{
		Meta: meta, Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: resultText(raw)}},
		StructuredContent: structured, IsError: isError,
	}
}

func (s *Server) serveOfficial(ctx context.Context, r io.Reader, w io.Writer) error {
	life := newStdioLifecycle(ctx)
	err := s.official.server.Run(ctx, &sdkmcp.IOTransport{
		Reader: io.NopCloser(newCappedMessageReader(r, s.maxRequestBytes, life)),
		Writer: nopWriteCloser{Writer: &trackedResponseWriter{Writer: w, life: life}},
	})
	if errors.Is(err, io.EOF) || (err != nil && strings.Contains(err.Error(), "server is closing: EOF")) {
		return nil
	}
	return err
}

func (s *Server) officialHTTPHandler() http.Handler {
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return s.official.server }, &sdkmcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, MaxRequestBodyBytes: int64(s.maxRequestBytes), PropagateRequestCancellation: true,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
		}
		// Older Moedex clients predate Streamable HTTP content negotiation and
		// omitted Accept. Supplying the protocol's canonical pair only when the
		// header is absent preserves them without weakening explicit validation.
		if r.Method == http.MethodPost && r.Header.Get("MCP-Protocol-Version") == "" {
			// Header-less clients are the initialize-era compatibility lane. Pin
			// that lane to the only legacy revision Moedex serves so the SDK does
			// not fall back to pre-2025-06 behavior (notably JSON-RPC batching).
			r.Header.Set("MCP-Protocol-Version", legacyProtocolVersion)
			accept := r.Header.Get("Accept")
			if accept == "" {
				r.Header.Set("Accept", "application/json, text/event-stream")
			} else if strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/event-stream") {
				r.Header.Set("Accept", accept+", text/event-stream")
			}
		}
		handler.ServeHTTP(w, r)
	})
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// cappedMessageReader feeds the SDK one complete NDJSON message at a time and
// refuses to buffer more than the configured request cap.
type cappedMessageReader struct {
	reader  *bufio.Reader
	max     int
	pending []byte
	err     error
	mu      sync.Mutex
	life    *stdioLifecycle
}

func newCappedMessageReader(r io.Reader, max int, life *stdioLifecycle) *cappedMessageReader {
	return &cappedMessageReader{reader: bufio.NewReaderSize(r, min(max+1, 64<<10)), max: max, life: life}
}

func (r *cappedMessageReader) Read(dst []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pending) == 0 && r.err == nil {
		var line bytes.Buffer
		for {
			part, err := r.reader.ReadSlice('\n')
			if line.Len()+len(part) > r.max {
				r.err = errRequestTooLarge
				break
			}
			line.Write(part)
			if err == nil {
				break
			}
			if !errors.Is(err, bufio.ErrBufferFull) {
				r.err = err
				break
			}
		}
		r.pending = line.Bytes()
		if len(r.pending) > 0 {
			r.life.observeRequest(r.pending)
		}
	}
	if len(r.pending) > 0 {
		n := copy(dst, r.pending)
		r.pending = r.pending[n:]
		return n, nil
	}
	if errors.Is(r.err, io.EOF) {
		r.mu.Unlock()
		r.life.wait()
		r.mu.Lock()
		return 0, io.EOF
	}
	return 0, r.err
}

type stdioLifecycle struct {
	ctx       context.Context
	mu        sync.Mutex
	expected  int
	responded int
	inputDone bool
	done      chan struct{}
	once      sync.Once
}

func newStdioLifecycle(ctx context.Context) *stdioLifecycle {
	return &stdioLifecycle{ctx: ctx, done: make(chan struct{})}
}

func (l *stdioLifecycle) observeRequest(line []byte) {
	var envelope struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(line, &envelope) == nil && len(envelope.ID) > 0 && string(envelope.ID) != "null" {
		l.mu.Lock()
		l.expected++
		l.mu.Unlock()
	}
}

func (l *stdioLifecycle) wait() {
	l.mu.Lock()
	l.inputDone = true
	l.maybeDoneLocked()
	l.mu.Unlock()
	select {
	case <-l.done:
	case <-l.ctx.Done():
	}
}

func (l *stdioLifecycle) response() {
	l.mu.Lock()
	l.responded++
	l.maybeDoneLocked()
	l.mu.Unlock()
}

func (l *stdioLifecycle) maybeDoneLocked() {
	if l.inputDone && l.responded >= l.expected {
		l.once.Do(func() { close(l.done) })
	}
}

type trackedResponseWriter struct {
	io.Writer
	life *stdioLifecycle
	mu   sync.Mutex
	buf  []byte
}

func (w *trackedResponseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.buf = append(w.buf, p...)
	count := bytes.Count(w.buf, []byte{'\n'})
	if count > 0 {
		last := bytes.LastIndexByte(w.buf, '\n')
		w.buf = append(w.buf[:0], w.buf[last+1:]...)
	}
	w.mu.Unlock()
	n, err := w.Writer.Write(p)
	for range count {
		w.life.response()
	}
	return n, err
}
