package mcp

import "net/http"

// HTTPHandler exposes the official SDK's stateless Streamable HTTP transport.
// The SDK owns protocol negotiation, per-request envelope/header validation,
// and cancellation propagation. Moedex supplies its one-MiB body cap and keeps
// authentication, deadlines, panic recovery, and the daemon's outer
// concurrency limiter in the existing middleware chain.
func (s *Server) HTTPHandler() http.Handler { return s.officialHTTPHandler() }
