package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// HTTPHandler exposes the MCP server over the Model Context Protocol's
// "Streamable HTTP" transport: an agent POSTs a single JSON-RPC message and gets
// a single JSON-RPC response back as application/json.
//
// This is the warm shared-daemon surface. One long-lived process loads the index
// once; every agent session connects over the network and is answered in
// milliseconds — instead of each session spawning its own stdio process and
// re-paying the cold-start load. The hot-swap reload underneath (rankHolder) keeps
// the daemon serving the old generation while a SIGHUP rebuilds the new one, so
// the 40s load is paid once per process lifetime, not per session.
//
// The transport is deliberately stateless: no Mcp-Session-Id is issued and every
// POST is dispatched independently through the same handleSafe path the stdio loop
// uses, so both transports answer identically (and the per-tools/call timeout,
// size caps, and panic recovery all apply unchanged). Requests (carrying an id)
// get 200 + a JSON-RPC response; notifications (no id) get 202 with no body; GET
// and other methods get 405 (no server-initiated streams, no session state).
//
// Concurrency bound: unlike Serve (whose read loop gates dispatch behind a
// maxConcurrency-sized semaphore), the handler returned here does not itself
// cap how many POSTs run at once — net/http already hands each request its own
// goroutine, so that decision belongs to the caller's handler chain. Callers
// serving this over the network MUST wrap it in a concurrency limiter (see
// cmd/moedex-serve's withConcurrencyLimit, applied to /mcp exactly as it is to
// /search) so a burst of agent sessions can't fan out into unbounded concurrent
// ranking/context-assembly work. A single POST's own worst case is still
// bounded here: maxRequestBytes caps the body and maxBatchSize caps how many
// JSON-RPC messages one legacy batch array can pack in (see handleHTTPBatch).
func (s *Server) HTTPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRPC(w, http.StatusMethodNotAllowed,
				response{JSONRPC: "2.0", Error: &rpcError{Code: codeInvalidRequest, Message: "only POST is supported on the MCP endpoint"}})
			return
		}
		s.handleHTTPPost(w, r)
	})
}

// handleHTTPPost reads one JSON-RPC message (single object or a legacy batch
// array), dispatches it, and writes the reply. The body is capped at
// maxRequestBytes the same way the stdio loop caps a line, so an oversized request
// can't OOM the decoder.
func (s *Server) handleHTTPPost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(s.maxRequestBytes)))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeRPC(w, http.StatusRequestEntityTooLarge,
				response{JSONRPC: "2.0", Error: &rpcError{Code: codeParseError, Message: "request exceeds maximum size"}})
			return
		}
		writeRPC(w, http.StatusBadRequest,
			response{JSONRPC: "2.0", Error: &rpcError{Code: codeParseError, Message: "could not read request body"}})
		return
	}

	// A top-level array is a (legacy) JSON-RPC batch. Modern MCP no longer
	// batches, but handle it so older clients keep working.
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && trimmed[0] == '[' {
		s.handleHTTPBatch(w, r.Context(), trimmed)
		return
	}

	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPC(w, http.StatusBadRequest,
			response{JSONRPC: "2.0", Error: &rpcError{Code: codeParseError, Message: "parse error: " + err.Error()}})
		return
	}
	resp, respond := s.handleSafe(r.Context(), &req)
	if !respond {
		// Notification: accepted, nothing to return.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPC(w, http.StatusOK, resp)
}

// handleHTTPBatch processes a JSON-RPC batch and returns an array of the replies
// that warrant one (notifications produce none). An all-notification batch yields
// 202 with no body, mirroring the single-message path.
//
// Batch items are dispatched one at a time (not fanned out to worker
// goroutines), so a batch is itself a size-bounded resource: it is capped at
// maxBatchSize messages — each one can run tools/call for up to
// requestTimeout, so an unbounded array would let a single HTTP request (and
// the one concurrency slot it holds) monopolize a core for
// len(batch)*requestTimeout in the worst case. The loop also re-checks
// ctx.Err() before each item so a cancelled or timed-out request (client gone,
// server shutting down) stops dispatching immediately instead of draining the
// rest of the batch.
func (s *Server) handleHTTPBatch(w http.ResponseWriter, ctx context.Context, body []byte) {
	var reqs []request
	if err := json.Unmarshal(body, &reqs); err != nil {
		writeRPC(w, http.StatusBadRequest,
			response{JSONRPC: "2.0", Error: &rpcError{Code: codeParseError, Message: "parse error: " + err.Error()}})
		return
	}
	if len(reqs) > s.maxBatchSize {
		writeRPC(w, http.StatusBadRequest,
			response{JSONRPC: "2.0", Error: &rpcError{Code: codeInvalidRequest,
				Message: fmt.Sprintf("batch of %d messages exceeds maximum of %d", len(reqs), s.maxBatchSize)}})
		return
	}
	resps := make([]response, 0, len(reqs))
	for i := range reqs {
		if ctx.Err() != nil {
			break // cancelled/expired: stop dispatching the rest of the batch
		}
		if resp, respond := s.handleSafe(ctx, &reqs[i]); respond {
			resps = append(resps, resp)
		}
	}
	if len(resps) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resps)
}

// writeRPC encodes a single JSON-RPC response with the given HTTP status.
func writeRPC(w http.ResponseWriter, status int, resp response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}
