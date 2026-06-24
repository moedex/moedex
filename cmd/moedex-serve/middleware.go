package main

import (
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// HTTP hardening is layered as decorators over the whole mux. The chain runs
// outermost-first: recover(accessLog(timeout(auth(mux)))). recover is outermost
// so it catches panics from any inner layer; accessLog wraps the writer to
// capture status/bytes and feeds the metrics; timeout bounds the HTTP response
// AND cancels r.Context(), which the corpus scan observes (see withTimeout);
// auth gates everything except the open /healthz and /metrics probes.

// openPaths bypass auth: liveness and the metrics scrape must work without a
// token (decision 3).
var openPaths = map[string]bool{"/healthz": true, "/metrics": true}

// respRecorder wraps http.ResponseWriter to capture the status code and byte
// count, and to report whether anything was written yet — withRecover relies on
// wroteHeader so it never double-writes over a handler that already responded.
type respRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (r *respRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.status = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *respRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		// Mirror net/http: a bare Write implies 200.
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// withRecover is the outermost layer: it turns a handler panic into a logged
// 500 instead of crashing the daemon. It writes a JSON 500 only if nothing has
// been written yet, so a handler that panics mid-stream keeps the status it
// already committed (the bytes are simply truncated). Mirrors mcp.handleSafe.
func withRecover(next http.Handler, m *metrics) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, ok := w.(*respRecorder)
		if !ok {
			rec = &respRecorder{ResponseWriter: w, status: http.StatusOK}
			w = rec
		}
		defer func() {
			if v := recover(); v != nil {
				m.incPanic()
				slog.Error("panic", "method", r.Method, "path", r.URL.Path,
					"panic", v, "stack", string(debug.Stack()))
				if !rec.wroteHeader {
					writeJSON(rec, http.StatusInternalServerError, map[string]string{"error": "internal error"})
				}
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withAccessLog records each request's outcome: it ensures the writer is a
// respRecorder, times the call, emits one structured access line, and feeds the
// latency/code-class metrics.
func withAccessLog(next http.Handler, m *metrics) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, ok := w.(*respRecorder)
		if !ok {
			rec = &respRecorder{ResponseWriter: w, status: http.StatusOK}
			w = rec
		}
		start := time.Now()
		next.ServeHTTP(w, r)
		dur := time.Since(start)
		slog.Info("access",
			"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"bytes", rec.bytes, "dur_ms", float64(dur.Microseconds())/1000.0,
			"remote", r.RemoteAddr)
		m.observe(rec.status, dur.Seconds())
	})
}

// withTimeout bounds the HTTP response with http.TimeoutHandler (503 on expiry).
// On expiry it cancels r.Context(), which Corpus.Regex/Literal thread into the
// search path: the scan's hot loops check cancellation on a stride, so an expired
// request stops burning CPU promptly rather than running to completion. The abort
// is observed within cancelCheckStride candidates (or one 16-line verify chunk),
// not instantly — bounded, not immediate.
func withTimeout(next http.Handler, d time.Duration) http.Handler {
	return http.TimeoutHandler(next, d, `{"error":"request timeout"}`)
}

// withAuth gates non-open paths behind a bearer token when one is configured.
// /healthz and /metrics always pass. With no token configured it is a no-op.
func withAuth(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" || openPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		const prefix = "Bearer "
		got := r.Header.Get("Authorization")
		if !strings.HasPrefix(got, prefix) || strings.TrimPrefix(got, prefix) != token {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// chain composes the hardening decorators into the single handler the server
// runs. Order: recover(accessLog(timeout(auth(mux)))).
func chain(mux http.Handler, token string, timeout time.Duration, m *metrics) http.Handler {
	h := withAuth(mux, token)
	h = withTimeout(h, timeout)
	h = withAccessLog(h, m)
	h = withRecover(h, m)
	return h
}

// resolveAddr applies the loopback default (decision 1): an addr with an EMPTY
// host (":8080" or "8080") always binds 127.0.0.1, regardless of token. An
// explicit host ("0.0.0.0:8080", "192.168.x:8080") is honored verbatim.
func resolveAddr(addr, token string) string {
	_ = token // intentionally not used: loopback default is unconditional
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// No colon at all (e.g. "8080"): treat the whole string as the port.
		return net.JoinHostPort("127.0.0.1", addr)
	}
	if host == "" {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr
}
