package servecmd

import (
	"crypto/subtle"
	"expvar"
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
		// withRecover is outermost in chain (below), so w here is always the raw
		// net/http ResponseWriter and this branch always wraps it; the ok branch
		// is dead under the current order. Kept so withRecover stays correct on
		// its own if a future caller wraps something that's already a
		// respRecorder (e.g. reused outside chain, or the order changes).
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
		// withAccessLog runs inside withRecover in chain (below), so w here is
		// already a *respRecorder and this branch always takes the ok path; the
		// wrap (!ok) branch is dead under the current order. Kept so
		// withAccessLog stays correct standalone (e.g. in a test, or if it's
		// ever moved outside withRecover).
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

// defaultSearchMaxConcurrency bounds concurrent /search requests by default,
// mirroring mcp.defaultMaxConcurrency (8): both guard a CPU-bound scan that can
// otherwise saturate every core under concurrent broad queries.
const defaultSearchMaxConcurrency = 8

// defaultMCPMaxConcurrency bounds concurrent /mcp requests by default. It
// mirrors mcp.defaultMaxConcurrency (8), the bound the stdio transport already
// applies to its own dispatch loop — the HTTP transport (mcp.Server.HTTPHandler)
// does not enforce that bound itself (net/http hands every POST its own
// goroutine), so the caller must, exactly as for /search below.
const defaultMCPMaxConcurrency = 8

// withConcurrencyLimit bounds the number of requests reaching next
// concurrently via a buffered channel acting as a semaphore. Once n requests
// are in flight, the next one is rejected immediately with 503 rather than
// queuing — both /search (a full cross-shard regex/literal scan) and /mcp (a
// ranked search + context-assembly pass) are CPU-bound work that can pin a
// core for up to the request timeout, so an unbounded queue is its own
// resource-exhaustion vector. n<=0 disables the limit (passthrough). rejected
// is incremented on every reject (a distinct counter per route so a saturated
// /mcp doesn't masquerade as /search load, or vice versa); msg is the caller-
// specific text in the 503 body. Mirrors mcp.WithMaxConcurrency, the stdio
// transport's equivalent guard.
func withConcurrencyLimit(next http.Handler, n int, rejected *expvar.Int, msg string) http.Handler {
	if n <= 0 {
		return next
	}
	sem := make(chan struct{}, n)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case sem <- struct{}{}:
		default:
			rejected.Add(1)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": msg})
			return
		}
		defer func() { <-sem }()
		next.ServeHTTP(w, r)
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

// bearerPrefix is the scheme prefix on the Authorization header. It is public
// (not secret), so checking it with a short-circuiting strings.HasPrefix
// leaks nothing; only the token comparison itself needs to be constant-time.
const bearerPrefix = "Bearer "

// bearerTokenMatches reports whether header carries "Bearer <token>" exactly,
// comparing the token in constant time so response timing cannot be used to
// recover it byte-by-byte.
func bearerTokenMatches(header, token string) bool {
	if !strings.HasPrefix(header, bearerPrefix) {
		return false
	}
	got := strings.TrimPrefix(header, bearerPrefix)
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// withAuth gates non-open paths behind a bearer token when one is configured.
// /healthz and /metrics always pass. With no token configured it is a no-op.
func withAuth(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" || openPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		if !bearerTokenMatches(r.Header.Get("Authorization"), token) {
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
