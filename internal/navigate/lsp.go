//go:build lsp

// This file is compiled only with `-tags lsp`. It is the real Condition-1
// implementation behind ADR 0017: a minimal LSP client that drives a language
// server (gopls by default) out of process and answers type-resolved
// definition / references / implementation queries.
//
// It pulls NO new go.mod dependencies. The JSON-RPC 2.0 transport — Content-
// Length framed messages over the server's stdin/stdout, a reader goroutine that
// correlates responses by id and answers the handful of server→client requests
// gopls makes during startup — is written entirely against the standard library.
// The language server itself is an external binary supplied on PATH.

package navigate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LSPCompiled reports whether the LSP navigation client was compiled into this
// binary. True in the lsp-tagged build.
const LSPCompiled = true

// Config configures a language-server-backed Navigator.
type Config struct {
	// Server is the language-server command to launch. If non-empty it is used
	// verbatim (with Args), bypassing the registry — the explicit-override path
	// cmd/moedex-nav uses. If empty the command is resolved from the registry by
	// Language (or defaults to "go"/gopls when Language is also empty).
	Server string
	// Args are extra arguments passed to the server command. Consulted only when
	// Server is set (registry specs supply their own args).
	Args []string
	// RootDir is the workspace root the server resolves against. If empty it is
	// derived from the first queried file by walking up to the nearest go.mod.
	RootDir string
	// Language is the canonical registry language to launch (e.g. "go",
	// "typescript", "python", "rust", "cpp") when Server is empty. Optional;
	// empty defaults to "go", reproducing the original gopls-only behavior.
	Language string
	// InitOptions overrides the registry spec's initializationOptions when set
	// (nil leaves the spec default, which is itself nil/omitted for every shipped
	// language). Honored on both the Server-override and registry paths.
	InitOptions map[string]any
	// Env is extra environment for the server process, appended to os.Environ().
	// Empty leaves the process inheriting the parent environment unchanged.
	Env []string
	// RequestTimeout is a default client-side per-request cap applied only when
	// the caller's ctx carries no deadline of its own. 0 means no client-imposed
	// cap. A per-request timeout cancels just that request (sending an LSP
	// $/cancelRequest); it never kills the shared server process.
	RequestTimeout time.Duration
	// Logger is an optional structured debug sink. nil falls back to the
	// MOEDEX_LSP_DEBUG env var (any non-empty value enables a stderr text handler)
	// and otherwise to a discard logger, so call sites never nil-check.
	Logger *slog.Logger

	// --- Pool production-lifecycle knobs (consumed by Pool, build-arm-agnostic) ---

	// IdleTTL is how long a pooled server may sit unused before a lazy sweep
	// (triggered by the next Navigator call or Sweep()) evicts and closes it.
	// 0 disables idle eviction — preserving the original Pool behavior where a
	// server lives until Pool.Close().
	IdleTTL time.Duration
	// MaxServers caps the number of live pooled servers. When a new (root,
	// language) would exceed it, the least-recently-used idle server is evicted
	// to make room. 0 means unbounded (the original behavior). A burst of more
	// than MaxServers brand-new distinct roots arriving at once may transiently
	// overshoot, because an in-flight creation is never force-killed.
	MaxServers int
	// BaseBackoff is the first restart-retry delay after a server dies on the
	// Navigator retry path. 0 => NewPool defaults to 200ms.
	BaseBackoff time.Duration
	// MaxBackoff caps the exponential restart-retry delay. 0 => NewPool defaults
	// to 5s.
	MaxBackoff time.Duration

	// now is an unexported test seam for virtual time. nil => time.Now. It is set
	// only by in-package tests; production callers cannot reach it.
	now func() time.Time
}

// ErrServerDead is the sentinel returned (wrapped) when a request fails because
// the language-server process died — either before the write or while the
// request was in flight. Callers branch with errors.Is(err, ErrServerDead);
// Pool already evicts a server once Alive() reports false.
var ErrServerDead = errors.New("navigate: language server died")

// deadError wraps the underlying reader/write error so errors.Is(err,
// ErrServerDead) is true while the original cause text is preserved.
type deadError struct{ err error }

func (d *deadError) Error() string {
	if d.err == nil {
		return ErrServerDead.Error()
	}
	return ErrServerDead.Error() + ": " + d.err.Error()
}
func (d *deadError) Unwrap() error { return ErrServerDead }

// LSP is a Navigator backed by a live language server speaking LSP over stdio.
type LSP struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	procCancel context.CancelFunc // cancels the process's own context; Close and a wedged write's timeout both use it

	// writeSem is a 1-buffered token channel acting as a context-cancelable
	// mutex serializing frame writes to stdin (F-10): a full/absent token means
	// "held". Unlike sync.Mutex, acquiring it can select against a caller's
	// ctx, so a lane stuck behind a write to a wedged server (one that stopped
	// draining stdin while staying alive) fails on its own deadline instead of
	// blocking forever — and so does Close(), whose shutdown handshake goes
	// through the same writeFrame. Starts with one token buffered (free).
	writeSem chan struct{}

	nextID atomic.Int64

	mu      sync.Mutex // guards pending, docs, docSeq, rootURI, closed, inited
	pending map[int64]chan rpcResponse
	docs    map[string]*docState // didOpen state, one entry per file
	// docSeq is a monotonic per-server counter stamped onto docState.lastUse by
	// every docFor call, giving evictLRUDocsLocked a total order of "most
	// recently touched" without needing wall-clock time (F-18).
	docSeq  uint64
	rootURI string
	closed  bool

	// initOptions is the resolved initializationOptions for this server (from the
	// registry spec or cfg override). nil means the initialize request omits the
	// field entirely — required for gopls, which configures via
	// workspace/configuration and must not receive a null initializationOptions.
	initOptions map[string]any

	// dead is set when the reader loop exits (server process gone / stdout
	// closed). Pool.Navigator uses Alive to evict and restart a crashed server,
	// the lifecycle robustness ADR 0017 Condition 2 demands.
	dead atomic.Bool

	initMu sync.Mutex // serializes the one-time initialize handshake
	inited bool

	// syncKind / posEncoding are the server's negotiated capabilities, captured
	// from the initialize reply during the handshake (under initMu, before
	// c.inited=true) and read-only afterwards — so no extra lock is needed once
	// ensureInit has returned. syncKind is the TextDocumentSyncKind the server
	// supports for the contentChanges WE send (0=None, 1=Full, 2=Incremental).
	// posEncoding gates the byte-based incremental range math: it is only valid
	// for "utf-8" (the encoding we list first); any other value forces the
	// always-correct full-text fallback.
	syncKind    int
	posEncoding string

	// reqTimeout is Config.RequestTimeout copied at NewLSP: a default per-request
	// cap used only when the caller's ctx has no deadline.
	reqTimeout time.Duration
	// log is the resolved structured logger; always non-nil (discard if disabled)
	// so call sites never nil-check.
	log *slog.Logger
}

// docState tracks one open document's live sync state. Its mutex serializes the
// open/change sequence so concurrent lanes sharing a server never double-open a
// file (which gopls rejects) or interleave versioned didChange notifications.
//
// hash is the fnv-1a of the content last pushed to the server; on each query
// ensureFresh re-reads the working tree and, if the hash moved, sends a
// didChange — that is what makes navigation track in-flight edits (ADR 0017
// Condition 3) rather than a one-shot snapshot. overlay, when set, is in-memory
// content that overrides disk entirely (an unsaved buffer), the strongest form
// of live-tree navigation: code that was never committed nor even written out.
type docState struct {
	mu         sync.Mutex
	opened     bool
	version    int
	hash       uint64
	overlay    []byte
	hasOverlay bool
	// synced is the exact byte content last successfully sent to the server (via
	// didOpen or didChange). Incremental (range) didChange diffs the new content
	// against it; hash is the fast-path equality short-circuit checked before any
	// diff is allocated. This holds one heap copy of each open file's text —
	// bounded by the number of open docs, which is what editor-grade incremental
	// sync requires. It is advanced ONLY after a successful notify, so a failed
	// notify leaves the last known-good baseline intact and the next attempt
	// re-diffs from it (never silently corrupting the server's buffer).
	synced []byte

	// lastUse and inFlight are guarded by LSP.mu (NOT ds.mu — they track this
	// docState's place in the server's document set, not its sync content) and
	// exist for the F-18 LRU cap:
	//   - lastUse is the docSeq stamp from the most recent docFor call, giving
	//     evictLRUDocsLocked a recency order.
	//   - inFlight counts callers currently between docFor and their matching
	//     releaseDoc. A docState with inFlight>0 is never evicted — closing that
	//     window is what stops a caller's already-in-hand pointer from racing a
	//     freshly created docState for the same path into two didOpens for one
	//     URI with no didClose between them.
	lastUse  uint64
	inFlight int
}

// --- JSON-RPC wire types (subset of LSP we use) -----------------------------

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcMessage is the catch-all shape for anything the server sends us: a response
// (has id + result/error), a server→client request (has id + method), or a
// notification (has method, no id).
type rpcMessage struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Params json.RawMessage  `json:"params"`
	Result json.RawMessage  `json:"result"`
	Error  *rpcError        `json:"error"`
}

type rpcResponse struct {
	Result json.RawMessage
	Err    *rpcError // protocol-level error from the server
	// errAny carries a transport/lifecycle error (e.g. a typed deadError when the
	// process dies mid-request). When set it takes precedence over Err in call().
	errAny error
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("lsp error %d: %s", e.Code, e.Message) }

// codeMethodNotFound is the JSON-RPC "method not found" code. A server that
// returns it for a navigation method simply doesn't implement that capability;
// locationQuery degrades such a response to empty results rather than an error.
const codeMethodNotFound = -32601

// lspPosition is 0-based; character is a byte offset because we negotiate the
// utf-8 positionEncoding. lspRange/lspLocation mirror the LSP structures.
type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}
type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}
type lspLocation struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}

// NewLSP launches the language server. The handshake runs lazily on the first
// query so the server can be created before any file is known (the Pool sets the
// root explicitly; direct callers may not).
//
// Crucially the server PROCESS is bound to its own context, not to ctx. ctx
// bounds only the startup syscall here; were the process tied to a request's
// ctx, that request's cancel/timeout would kill a server other lanes still share
// — the lifecycle hazard at the heart of ADR 0017 Condition 2. The process lives
// until Close (or its own death).
func NewLSP(ctx context.Context, cfg Config) (*LSP, error) {
	// Server selection rule (preserves every pinned test):
	//  1. cfg.Server set: use it + cfg.Args verbatim (explicit override).
	//  2. else cfg.Language set: resolve command/args/init from the registry.
	//  3. else (both empty): default to the "go"/gopls spec — the original
	//     behavior the Config{} and Config{RootDir:root} test cases rely on.
	var (
		server   string
		args     []string
		initOpts = cfg.InitOptions
		specEnv  []string
	)
	switch {
	case cfg.Server != "":
		server, args = cfg.Server, cfg.Args
	default:
		lang := cfg.Language
		if lang == "" {
			lang = "go"
		}
		spec, ok := defaultRegistry[lang]
		if !ok {
			return nil, fmt.Errorf("navigate: no language server registered for %q", lang)
		}
		server, args = spec.Command, spec.Args
		if initOpts == nil {
			initOpts = spec.InitOptions
		}
		if spec.ResolveEnv != nil {
			specEnv = spec.ResolveEnv() // e.g. DOTNET_ROOT for csharp-ls
		}
	}
	procCtx, procCancel := context.WithCancel(context.WithoutCancel(ctx))
	cmd := exec.CommandContext(procCtx, server, args...)
	// Extra environment is appended to the inherited os.Environ(): caller-supplied
	// cfg.Env first, then the registry spec's resolved env (e.g. DOTNET_ROOT for
	// csharp-ls). Leaving cmd.Env nil (when both are empty) keeps the current
	// inherit-everything behavior. cfg.Server may be an absolute path — exec honors
	// it (LookPath semantics) so callers can pin a specific server build.
	if len(cfg.Env) > 0 || len(specEnv) > 0 {
		cmd.Env = append(os.Environ(), cfg.Env...)
		cmd.Env = append(cmd.Env, specEnv...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		procCancel()
		return nil, fmt.Errorf("navigate: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		procCancel()
		return nil, fmt.Errorf("navigate: stdout pipe: %w", err)
	}
	// Server stderr is suppressed by default — some servers (csharp-ls, clangd)
	// are extremely chatty and would pollute the CLI's stdout/JSON. Surface it
	// only when debug logging is on (Config.Logger set or MOEDEX_LSP_DEBUG), the
	// same switch that governs our own structured logs.
	if cfg.Logger != nil || os.Getenv("MOEDEX_LSP_DEBUG") != "" {
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Start(); err != nil {
		procCancel()
		return nil, fmt.Errorf("navigate: start %s: %w (is it on PATH?)", server, err)
	}

	writeSem := make(chan struct{}, 1)
	writeSem <- struct{}{} // starts unheld (F-10)

	c := &LSP{
		cmd:         cmd,
		stdin:       stdin,
		procCancel:  procCancel,
		writeSem:    writeSem,
		pending:     make(map[int64]chan rpcResponse),
		docs:        make(map[string]*docState),
		rootURI:     pathToURI(cfg.RootDir),
		initOptions: initOpts,
		reqTimeout:  cfg.RequestTimeout,
		log:         resolveLogger(cfg.Logger),
	}
	go c.readLoop(stdout)
	return c, nil
}

// resolveLogger picks the structured debug sink: an explicit Config.Logger wins;
// else MOEDEX_LSP_DEBUG (any non-empty value) enables a stderr text handler at
// debug level; else a discard logger so every call site can log unconditionally
// without a nil check. io.Discard is stdlib — no new dependency.
func resolveLogger(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	if os.Getenv("MOEDEX_LSP_DEBUG") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- public Navigator surface ----------------------------------------------

func (c *LSP) Definition(ctx context.Context, at Pos) ([]Location, error) {
	return c.locationQuery(ctx, "textDocument/definition", at, nil)
}

func (c *LSP) Implementations(ctx context.Context, at Pos) ([]Location, error) {
	return c.locationQuery(ctx, "textDocument/implementation", at, nil)
}

func (c *LSP) References(ctx context.Context, at Pos, includeDecl bool) ([]Location, error) {
	extra := map[string]any{"context": map[string]any{"includeDeclaration": includeDecl}}
	return c.locationQuery(ctx, "textDocument/references", at, extra)
}

// shutdownTimeout bounds the graceful "shutdown" handshake in Close. A wedged
// server that accepts the request but never replies must not hang teardown —
// the kill (procCancel) + reap (cmd.Wait) below is the real teardown; the
// graceful shutdown is strictly best-effort.
const shutdownTimeout = 3 * time.Second

// defaultWriteTimeout bounds a stdin write (writeSem acquisition plus the
// write itself) when the caller's ctx carries no deadline of its own — F-10.
// Mirrors the reqTimeout convention in call(): a default cap applied only
// when ctx has none. Without this, a server that stops draining stdin while
// staying alive (wedged, not dead) hangs every lane sharing it forever,
// including Close().
const defaultWriteTimeout = 5 * time.Second

// bestEffortNotifyTimeout bounds the handful of fire-and-forget notifications
// sent purely as cleanup — a request's $/cancelRequest after its OWN ctx
// already expired, and Close's "exit" — so an already-abandoned lane, or
// teardown, never waits the full defaultWriteTimeout behind a wedged server
// for a message nobody is waiting on a reply to.
const bestEffortNotifyTimeout = 500 * time.Millisecond

func (c *LSP) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	c.dead.Store(true)

	// Best-effort graceful shutdown; then drop stdin, cancel the process context
	// (kills it if it lingers), and reap. Both writes below are ctx-bounded
	// (F-10): a wedged server's stdin can no longer hang this handshake — the
	// write times out, procCancel fires from inside writeFrame, and we fall
	// through to the same kill+reap teardown a healthy shutdown would have led
	// to anyway.
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	_, _ = c.call(ctx, "shutdown", nil)
	cancel()
	exitCtx, exitCancel := context.WithTimeout(context.Background(), bestEffortNotifyTimeout)
	_ = c.notify(exitCtx, "exit", nil)
	exitCancel()
	_ = c.stdin.Close()
	c.procCancel()
	return c.cmd.Wait()
}

// --- query plumbing ---------------------------------------------------------

func (c *LSP) locationQuery(ctx context.Context, method string, at Pos, extra map[string]any) ([]Location, error) {
	if err := c.ensureInit(ctx, at.File); err != nil {
		return nil, err
	}
	if err := c.ensureFresh(ctx, at.File); err != nil {
		return nil, err
	}
	params := map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(at.File)},
		"position":     posToLSP(at),
	}
	for k, v := range extra {
		params[k] = v
	}
	raw, err := c.call(ctx, method, params)
	if err != nil {
		// A server that doesn't implement this method (JSON-RPC MethodNotFound,
		// -32601) is a partial-capability server, not a failure: many real
		// servers support only a subset (sql-language-server has no
		// definition/references at all; cflsp has definition but no references;
		// the CSS/HTML servers vary). Degrade to "no results" so `def` works
		// where supported and `refs`/`impl` come back empty instead of erroring.
		var rpcErr *rpcError
		if errors.As(err, &rpcErr) && rpcErr.Code == codeMethodNotFound {
			return nil, nil
		}
		return nil, err
	}
	// definition can return a single Location or an array; references and
	// implementation return an array. null means "no result".
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var locs []lspLocation
	if err := json.Unmarshal(raw, &locs); err != nil {
		var one lspLocation
		if err2 := json.Unmarshal(raw, &one); err2 != nil {
			return nil, fmt.Errorf("navigate: decode %s result: %w", method, err)
		}
		locs = []lspLocation{one}
	}
	out := make([]Location, 0, len(locs))
	for _, l := range locs {
		out = append(out, Location{
			File:  uriToPath(l.URI),
			Start: lspToPos(uriToPath(l.URI), l.Range.Start),
			End:   lspToPos(uriToPath(l.URI), l.Range.End),
		})
	}
	return out, nil
}

// symNode is a decode union for the two shapes a server can hand back a named
// symbol in: the hierarchical textDocument/documentSymbol reply
// (DocumentSymbol: SelectionRange + nested Children, no own URI — it's the
// queried file) and the flat workspace/symbol / SymbolInformation reply
// (Location: URI + Range, no Children). A single struct absorbs both because
// json.Unmarshal silently leaves absent fields at their zero value, so one
// decode path serves every server ADR 0018 has to support.
type symNode struct {
	Name string `json:"name"`
	Kind int    `json:"kind"`
	// SelectionRange is the identifier span (DocumentSymbol only) — preferred
	// over Range (the whole declaration, often starting at a keyword) so the
	// resolved position lands ON the name, which is what a follow-up
	// find_references/find_definition query needs.
	SelectionRange *lspRange    `json:"selectionRange"`
	Range          *lspRange    `json:"range"`
	Location       *symLocation `json:"location"`
	Children       []symNode    `json:"children"`
}

// symLocation is workspace/symbol's (and flat SymbolInformation's) location
// field. Range is a pointer because LSP 3.17 WorkspaceSymbol permits a
// range-less {uri} location for a cheap, unresolved result.
type symLocation struct {
	URI   string    `json:"uri"`
	Range *lspRange `json:"range"`
}

// symbolKindNames maps the LSP SymbolKind integer enum to its spec name.
var symbolKindNames = map[int]string{
	1: "File", 2: "Module", 3: "Namespace", 4: "Package", 5: "Class",
	6: "Method", 7: "Property", 8: "Field", 9: "Constructor", 10: "Enum",
	11: "Interface", 12: "Function", 13: "Variable", 14: "Constant",
	15: "String", 16: "Number", 17: "Boolean", 18: "Array", 19: "Object",
	20: "Key", 21: "Null", 22: "EnumMember", 23: "Struct", 24: "Event",
	25: "Operator", 26: "TypeParameter",
}

// symbolKindName spells out a raw LSP SymbolKind integer; an unrecognized value
// (a future protocol addition) degrades to "Unknown" rather than panicking.
func symbolKindName(kind int) string {
	if name, ok := symbolKindNames[kind]; ok {
		return name
	}
	return "Unknown"
}

// flattenSymbolNodes converts a decoded symNode tree into flat Symbols.
// defaultURI is the file being queried, used when a node carries no Location
// of its own (the DocumentSymbol shape, which is implicitly scoped to the
// queried file). Range selection prefers SelectionRange (the identifier) over
// Range (the whole declaration) or Location.Range, in that order.
func flattenSymbolNodes(nodes []symNode, defaultURI string) []Symbol {
	out := make([]Symbol, 0, len(nodes))
	for _, n := range nodes {
		uri := defaultURI
		var rng lspRange
		switch {
		case n.SelectionRange != nil:
			rng = *n.SelectionRange
		case n.Range != nil:
			rng = *n.Range
		case n.Location != nil:
			uri = n.Location.URI
			if n.Location.Range != nil {
				rng = *n.Location.Range
			}
		}
		file := uriToPath(uri)
		out = append(out, Symbol{
			Name: n.Name,
			Kind: symbolKindName(n.Kind),
			Loc: Location{
				File:  file,
				Start: lspToPos(file, rng.Start),
				End:   lspToPos(file, rng.End),
			},
		})
		if len(n.Children) > 0 {
			out = append(out, flattenSymbolNodes(n.Children, uri)...)
		}
	}
	return out
}

// decodeSymbolResult shares the null/empty/method-not-found handling
// locationQuery uses, decoding into the symNode union and flattening it.
func decodeSymbolResult(raw json.RawMessage, err error, method, defaultURI string) ([]Symbol, error) {
	if err != nil {
		var rpcErr *rpcError
		if errors.As(err, &rpcErr) && rpcErr.Code == codeMethodNotFound {
			// Partial-capability server (mirrors locationQuery): degrade to no
			// results rather than erroring, so a language without name lookup
			// fails soft (ADR 0018).
			return nil, nil
		}
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var nodes []symNode
	if jerr := json.Unmarshal(raw, &nodes); jerr != nil {
		return nil, fmt.Errorf("navigate: decode %s result: %w", method, jerr)
	}
	return flattenSymbolNodes(nodes, defaultURI), nil
}

// WorkspaceSymbol runs the ADR 0018 name-based lookup (LSP workspace/symbol): a
// fuzzy/substring match over every symbol name in the server's workspace, not
// an exact resolver. Workspace-scoped, so — unlike Definition/References/
// Implementations — it takes no file/position; ensureInit derives the server's
// root the same way those do when RootDir was not supplied at construction (a
// direct caller invoking WorkspaceSymbol as the very first query on a server
// with no RootDir will root at "."; Pool always supplies RootDir explicitly).
func (c *LSP) WorkspaceSymbol(ctx context.Context, query string) ([]Symbol, error) {
	if err := c.ensureInit(ctx, ""); err != nil {
		return nil, err
	}
	raw, err := c.call(ctx, "workspace/symbol", map[string]any{"query": query})
	return decodeSymbolResult(raw, err, "workspace/symbol", "")
}

// DocumentSymbol runs the ADR 0018 file-scoped enumeration (LSP
// textDocument/documentSymbol): every top-level and nested declaration in
// file, flattened into one slice (a server returning the hierarchical
// DocumentSymbol shape has its Children walked by flattenSymbolNodes; a server
// returning the flat SymbolInformation shape needs no walk at all).
func (c *LSP) DocumentSymbol(ctx context.Context, file string) ([]Symbol, error) {
	if err := c.ensureInit(ctx, file); err != nil {
		return nil, err
	}
	if err := c.ensureFresh(ctx, file); err != nil {
		return nil, err
	}
	uri := pathToURI(file)
	raw, err := c.call(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
	return decodeSymbolResult(raw, err, "textDocument/documentSymbol", uri)
}

// ensureInit runs the initialize/initialized handshake exactly once, but is
// RETRYABLE on failure: a transient request-ctx cancel during the first query
// must not permanently poison a shared server (a sync.Once would). The root is
// derived from the first queried file if RootDir was not supplied. initMu
// serializes concurrent first-callers so the handshake happens once.
func (c *LSP) ensureInit(ctx context.Context, file string) error {
	c.initMu.Lock()
	defer c.initMu.Unlock()
	if c.inited {
		return nil
	}
	if c.rootURI == "" {
		// Language-aware fallback for direct NewLSP callers that didn't set
		// RootDir (the Pool always pre-sets it). moduleRoot is Go-only and would
		// mis-root a TS/Py/Rust/C++ server at a go.mod ancestor; workspaceRoot
		// keys off each language's own root markers.
		_, root := workspaceRoot(file)
		c.rootURI = pathToURI(root)
	}
	params := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   c.rootURI,
		"workspaceFolders": []map[string]any{
			{"uri": c.rootURI, "name": "root"},
		},
		// Negotiate utf-8 so server columns are byte columns (ADR 0002).
		"capabilities": map[string]any{
			"general": map[string]any{
				"positionEncodings": []string{"utf-8", "utf-16"},
			},
			"textDocument": map[string]any{
				// linkSupport off so definition returns Location[], not
				// LocationLink[] — one decode path.
				"definition":     map[string]any{"linkSupport": false},
				"implementation": map[string]any{"linkSupport": false},
				"references":     map[string]any{},
				"synchronization": map[string]any{
					"didOpen": true, "didChange": true, "didClose": true,
				},
				// hierarchicalDocumentSymbolSupport asks for the nested
				// DocumentSymbol[] shape (ADR 0018); flattenSymbolNodes flattens
				// it back out. A server that ignores this and returns the flat
				// SymbolInformation[] shape still decodes via symNode's
				// Location fallback.
				"documentSymbol": map[string]any{"hierarchicalDocumentSymbolSupport": true},
			},
			"workspace": map[string]any{
				"symbol": map[string]any{},
			},
		},
	}
	// Only attach initializationOptions when the resolved server actually has
	// some. Omit the field for servers (like gopls) that ship none — sending a
	// null would change gopls startup, which configures via
	// workspace/configuration instead.
	if c.initOptions != nil {
		params["initializationOptions"] = c.initOptions
	}
	c.log.Debug("lsp handshake start", "root", c.rootURI)
	raw, err := c.call(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("navigate: initialize: %w", err)
	}
	var res initializeResult
	if jerr := json.Unmarshal(raw, &res); jerr != nil {
		// A server that returns an undecodable initialize result is anomalous;
		// fall back to the safest sync behavior (Full) rather than failing.
		c.syncKind, c.posEncoding = 1, ""
	} else {
		c.syncKind = parseSyncKind(res.Capabilities.TextDocumentSync)
		c.posEncoding = res.Capabilities.PositionEncoding
		// Many servers (gopls v0.22) honor the client's first-listed
		// positionEncoding without echoing it back in the result. We advertise
		// utf-8 first and the rest of this client (posToLSP byte columns, the
		// passing cross-file tests) already depends on utf-8, so an absent value
		// means "our utf-8 preference was taken", not utf-16. Normalize it so the
		// incremental byte-math gate (posEncoding=="utf-8") matches reality.
		if c.posEncoding == "" {
			c.posEncoding = "utf-8"
		}
	}
	if err := c.notify(ctx, "initialized", map[string]any{}); err != nil {
		return fmt.Errorf("navigate: initialized: %w", err)
	}
	c.inited = true
	c.log.Debug("lsp handshake done", "syncKind", c.syncKind, "posEncoding", c.posEncoding)
	return nil
}

// initializeResult is the slice of the initialize reply we need: the negotiated
// text-document sync capability and the position encoding.
type initializeResult struct {
	Capabilities struct {
		TextDocumentSync json.RawMessage `json:"textDocumentSync"`
		PositionEncoding string          `json:"positionEncoding"`
	} `json:"capabilities"`
}

// parseSyncKind decodes capabilities.textDocumentSync, which is polymorphic: a
// bare TextDocumentSyncKind int (0=None,1=Full,2=Incremental) OR an object
// {openClose:bool, change:int}. Anything absent / ambiguous defaults to Full(1),
// the always-correct fallback — syncLocked treats anything that is not 2 as
// full-text, so a mis-parse can never silently stop us from syncing.
func parseSyncKind(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 1
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		if n == 0 {
			return 1 // None advertised here is treated as Full for our outbound changes
		}
		return n
	}
	var obj struct {
		Change int `json:"change"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Change != 0 {
		return obj.Change
	}
	return 1
}

// maxOpenDocs bounds how many per-file docStates a single server keeps live
// at once (F-18). Before this cap existed, docFor never removed entries and
// nothing ever sent textDocument/didClose, so a pooled server's c.docs — and
// the buffers the external language-server process mirrors for each one —
// grew for the server's entire lifetime, which ordinary daemon operation (any
// root queried at least once per IdleTTL window) can make effectively
// unbounded. 512 is generous enough that a single navigation session over one
// repo rarely triggers eviction, while still capping worst case memory to a
// bounded number of full-text file copies instead of "every file this server
// has ever seen".
const maxOpenDocs = 512

// docVictim is one docState evicted by evictLRUDocsLocked, carried out past
// the c.mu critical section so its textDocument/didClose can be sent without
// holding c.mu across a stdin write — the same lesson F-10 applied to the
// request path, applied here to eviction.
type docVictim struct {
	path string
	ds   *docState
}

// docFor returns the docState for a file, creating it on first use, and marks
// it in-flight so it cannot be evicted until the caller's matching
// releaseDoc. Every docFor call site pairs with exactly one releaseDoc, called
// after the ds.mu-guarded section that follows docFor (F-18).
//
// Each call also stamps a fresh recency counter and, when the live doc count
// exceeds maxOpenDocs, evicts the least-recently-touched eligible docs (never
// abs itself, never one still in-flight elsewhere).
func (c *LSP) docFor(abs string) *docState {
	c.mu.Lock()
	ds := c.docs[abs]
	if ds == nil {
		ds = &docState{}
		c.docs[abs] = ds
	}
	c.docSeq++
	ds.lastUse = c.docSeq
	ds.inFlight++
	victims := c.evictLRUDocsLocked(abs)
	c.mu.Unlock()

	c.closeVictims(victims)
	return ds
}

// releaseDoc marks the caller done with a docState obtained from docFor,
// making it eligible for LRU eviction again (F-18). Call it after releasing
// ds.mu, never before — see docFor.
func (c *LSP) releaseDoc(ds *docState) {
	c.mu.Lock()
	ds.inFlight--
	c.mu.Unlock()
}

// evictLRUDocsLocked removes the least-recently-touched docs from c.docs
// until at most maxOpenDocs remain, skipping except (the path the caller just
// touched) and any docState currently in-flight elsewhere. Must be called
// with c.mu held; it deletes victims from the map under the lock and returns
// them so the caller can send didClose AFTER unlocking (F-18, mirroring how
// Pool's sweepIdleLocked/pickLRULocked hand victims back for the caller to
// close post-unlock).
//
// Skipping in-flight docs is not an optimization — it is what makes eviction
// safe: a caller sitting between docFor and its own use of the returned
// pointer must never have that exact docState vanish out from under it, or a
// second, freshly created docState for the same path could send its own
// didOpen before the first caller's didOpen/didChange has even happened,
// putting two opens on the wire for one URI with no didClose between them.
func (c *LSP) evictLRUDocsLocked(except string) []docVictim {
	var victims []docVictim
	for len(c.docs) > maxOpenDocs {
		var (
			oldestPath string
			oldest     *docState
			oldestSeq  uint64
		)
		for p, ds := range c.docs {
			if p == except || ds.inFlight > 0 {
				continue
			}
			if oldest == nil || ds.lastUse < oldestSeq {
				oldestPath, oldest, oldestSeq = p, ds, ds.lastUse
			}
		}
		if oldest == nil {
			// Soft overshoot: every other doc is in-flight right now. Preserve
			// liveness rather than evict something in use; a later docFor call
			// (once one of them finishes) tries again — the same acceptable
			// transient overshoot Pool.pickLRULocked documents for MaxServers.
			break
		}
		delete(c.docs, oldestPath)
		victims = append(victims, docVictim{path: oldestPath, ds: oldest})
	}
	return victims
}

// closeVictims sends textDocument/didClose for each evicted docState that had
// actually been opened — mirroring an editor closing a tab: the server drops
// its buffer for that URI, and the file's next query docFor's a brand new
// docState and re-didOpens from current content. Best-effort and bounded by
// bestEffortNotifyTimeout like the other fire-and-forget cleanup notifies
// (Close's "exit", call()'s $/cancelRequest) — an eviction must never stall a
// caller behind a wedged server.
func (c *LSP) closeVictims(victims []docVictim) {
	for _, v := range victims {
		v.ds.mu.Lock()
		opened := v.ds.opened
		v.ds.mu.Unlock()
		if !opened {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), bestEffortNotifyTimeout)
		err := c.notify(ctx, "textDocument/didClose", map[string]any{
			"textDocument": map[string]any{"uri": pathToURI(v.path)},
		})
		cancel()
		if err != nil {
			c.log.Debug("lsp notify didClose failed", "path", v.path, "err", err.Error())
		} else {
			c.log.Debug("lsp notify didClose", "path", v.path)
		}
	}
}

// ensureFresh makes the server's buffer for file reflect the current working
// tree before a query runs. On first sight it sends didOpen; afterwards, if the
// content has changed since the last sync (an Execute-phase edit, or an overlay),
// it sends a versioned full-text didChange. This is the live-tree freshness of
// ADR 0017 Condition 3: the SHA-addressed corpus is stale against uncommitted
// edits by construction; this path is not.
func (c *LSP) ensureFresh(ctx context.Context, file string) error {
	abs, err := filepath.Abs(file)
	if err != nil {
		return fmt.Errorf("navigate: abs path: %w", err)
	}
	ds := c.docFor(abs)
	defer c.releaseDoc(ds)
	ds.mu.Lock()
	defer ds.mu.Unlock()

	content, err := c.liveContent(abs, ds)
	if err != nil {
		return err
	}
	return c.syncLocked(ctx, abs, ds, content)
}

// liveContent returns the bytes that represent the file's live state: the
// in-memory overlay if one is set, else the working-tree file on disk.
func (c *LSP) liveContent(abs string, ds *docState) ([]byte, error) {
	if ds.hasOverlay {
		return ds.overlay, nil
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("navigate: read %s: %w", abs, err)
	}
	return content, nil
}

// syncLocked opens (first time) or didChanges (on content drift) the document.
// Caller must hold ds.mu.
func (c *LSP) syncLocked(ctx context.Context, abs string, ds *docState, content []byte) error {
	h := fnv1a(content)
	if !ds.opened {
		ds.version = 1
		if err := c.notify(ctx, "textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri":        pathToURI(abs),
				"languageId": languageID(abs),
				"version":    ds.version,
				"text":       string(content),
			},
		}); err != nil {
			return err
		}
		ds.opened = true
		ds.hash = h
		// didOpen always sends full text and (re)bases the incremental baseline,
		// so a re-open after a dropped change re-synchronizes the server buffer.
		ds.synced = append([]byte(nil), content...)
		c.log.Debug("lsp notify didOpen", "uri", pathToURI(abs), "version", ds.version, "bytes", len(content))
		return nil
	}
	if h == ds.hash {
		return nil // already in sync
	}

	// Build the contentChanges. Take the incremental (single range) path only when
	// the server supports Incremental(2) AND the negotiated encoding is utf-8, so
	// the byte-based range math in diffRange is valid (ADR 0002). Any other case
	// falls back to a full-text change, which is always correct.
	var (
		changes []map[string]any
		kind    string
		delta   int
	)
	if c.syncKind == 2 && c.posEncoding == "utf-8" {
		rng, text, ok := diffRange(ds.synced, content)
		if !ok {
			// Equal under the full byte compare (a hash collision); nothing to send.
			ds.hash = h
			return nil
		}
		changes = []map[string]any{{"range": rng, "text": text}}
		kind, delta = "incremental", len(text)
	}
	if changes == nil {
		// None(0), Full(1), unknown, or non-utf-8 encoding -> full text.
		changes = []map[string]any{{"text": string(content)}}
		kind, delta = "full", len(content)
	}

	ds.version++
	if err := c.notify(ctx, "textDocument/didChange", map[string]any{
		"textDocument": map[string]any{
			"uri":     pathToURI(abs),
			"version": ds.version,
		},
		"contentChanges": changes,
	}); err != nil {
		// On a failed notify, do NOT advance synced/hash/version's baseline (the
		// version bump is harmless; the buffer baseline is what matters): leave
		// ds.synced/ds.hash at the last known-good so the next attempt re-diffs
		// from a state the server actually holds.
		return err
	}
	ds.synced = append([]byte(nil), content...)
	ds.hash = h
	c.log.Debug("lsp notify didChange", "uri", pathToURI(abs), "version", ds.version, "change", kind, "delta", delta)
	return nil
}

// diffRange computes one minimal LSP contentChange replacing a single contiguous
// span of old with the corresponding span of new. The returned range is in
// 0-based LSP positions (line, utf-8 byte character) into OLD, and text is the
// replacement substring of new. ok is false when old==new (the caller skips the
// notification). All math is byte-based — valid only under the negotiated utf-8
// encoding, so a "character" is a byte column within its line.
func diffRange(old, new []byte) (rng lspRange, text string, ok bool) {
	if bytes.Equal(old, new) {
		return lspRange{}, "", false
	}
	// Common byte prefix.
	prefix := 0
	maxPre := min(len(old), len(new))
	for prefix < maxPre && old[prefix] == new[prefix] {
		prefix++
	}
	// Common byte suffix, clamped so prefix+suffix never overruns either buffer
	// (prevents a crossed/negative range on an insert or delete at a boundary).
	suffix := 0
	maxSuf := min(len(old), len(new)) - prefix
	for suffix < maxSuf && old[len(old)-1-suffix] == new[len(new)-1-suffix] {
		suffix++
	}
	startPos := offsetToPos(old, prefix)
	endPos := offsetToPos(old, len(old)-suffix)
	text = string(new[prefix : len(new)-suffix])
	return lspRange{Start: startPos, End: endPos}, text, true
}

// offsetToPos converts a byte offset in buf to a 0-based LSP position. line is
// the count of '\n' before off; character is the bytes since the last '\n' (or
// the start of the buffer). This is the inverse of the byte-column convention
// posToLSP/lspToPos use, so the range diff and position queries share one
// coordinate system (valid under the utf-8 encoding contract, ADR 0002).
func offsetToPos(buf []byte, off int) lspPosition {
	if off > len(buf) {
		off = len(buf)
	}
	line := bytes.Count(buf[:off], []byte{'\n'})
	lastNL := bytes.LastIndexByte(buf[:off], '\n')
	character := off - (lastNL + 1)
	return lspPosition{Line: line, Character: character}
}

// SetOverlay installs in-memory content as the live buffer for file and syncs it
// to the server immediately. Subsequent queries see this content instead of
// disk until DropOverlay — navigation over code that exists only in flight, the
// dirty-buffer case ADR 0017 Condition 3 calls out. The server must be
// initialized first (any prior query, or call it after construction).
func (c *LSP) SetOverlay(ctx context.Context, file string, content []byte) error {
	if err := c.ensureInit(ctx, file); err != nil {
		return err
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return fmt.Errorf("navigate: abs path: %w", err)
	}
	ds := c.docFor(abs)
	defer c.releaseDoc(ds)
	ds.mu.Lock()
	defer ds.mu.Unlock()
	ds.overlay = append([]byte(nil), content...)
	ds.hasOverlay = true
	return c.syncLocked(ctx, abs, ds, ds.overlay)
}

// NotifyChanged tells the server that the given working-tree files changed on
// disk — the Execute-phase signal an orchestrator sends after it edits files.
// For files currently open as buffers it pushes a didChange from disk; for files
// gopls only knows from disk it sends workspace/didChangeWatchedFiles so the
// server invalidates and reloads them. Files under an active overlay are left
// alone (the unsaved buffer is intentionally authoritative).
//
// This is the dependency-free alternative to an in-process file watcher (which
// would pull a new module in, against ADR 0001): the caller already knows what
// it edited, so it says so explicitly.
func (c *LSP) NotifyChanged(ctx context.Context, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	if err := c.ensureInit(ctx, paths[0]); err != nil {
		return err
	}
	var watched []map[string]any
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return fmt.Errorf("navigate: abs path: %w", err)
		}
		ds := c.docFor(abs)
		ds.mu.Lock()
		switch {
		case ds.hasOverlay:
			// Overlay wins; ignore the disk change for this file.
		case ds.opened:
			content, rerr := c.liveContent(abs, ds) // disk
			if rerr == nil {
				err = c.syncLocked(ctx, abs, ds, content)
			} else {
				err = rerr
			}
		default:
			watched = append(watched, map[string]any{"uri": pathToURI(abs), "type": 2})
		}
		ds.mu.Unlock()
		c.releaseDoc(ds)
		if err != nil {
			return err
		}
	}
	if len(watched) > 0 {
		return c.notify(ctx, "workspace/didChangeWatchedFiles", map[string]any{"changes": watched})
	}
	return nil
}

// DropOverlay removes a file's overlay and re-syncs the server to the on-disk
// working-tree content.
func (c *LSP) DropOverlay(ctx context.Context, file string) error {
	abs, err := filepath.Abs(file)
	if err != nil {
		return fmt.Errorf("navigate: abs path: %w", err)
	}
	ds := c.docFor(abs)
	defer c.releaseDoc(ds)
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if !ds.hasOverlay {
		return nil
	}
	ds.overlay = nil
	ds.hasOverlay = false
	content, err := c.liveContent(abs, ds)
	if err != nil {
		return err
	}
	return c.syncLocked(ctx, abs, ds, content)
}

func fnv1a(b []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(b)
	return h.Sum64()
}

// Alive reports whether the underlying server is still running. It becomes false
// once the reader loop observes the process exit / stdout close, or after Close.
func (c *LSP) Alive() bool { return !c.dead.Load() }

// call sends a request and waits for the correlated response or ctx cancel.
//
// Cancellation is per-request and process-safe: on ctx.Done() we drop the
// pending entry and send a best-effort LSP $/cancelRequest so the server stops
// wasted work, but we never touch the process — a sibling lane sharing this
// server is unaffected (the decoupled process context is preserved). A default
// per-request timeout (Config.RequestTimeout) is applied only when the caller's
// ctx has no deadline of its own.
func (c *LSP) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if c.reqTimeout > 0 {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, c.reqTimeout)
			defer cancel()
		}
	}
	id := c.nextID.Add(1)
	ch := make(chan rpcResponse, 1)
	start := time.Now()

	c.mu.Lock()
	if c.closed && method != "shutdown" {
		c.mu.Unlock()
		return nil, fmt.Errorf("navigate: client closed")
	}
	// Fast-fail with the typed sentinel if the server is already known dead, so
	// callers get a consistent errors.Is(err, ErrServerDead) instead of a raw
	// pipe-write error. shutdown during Close is exempt (Close sets dead first).
	if c.dead.Load() && method != "shutdown" {
		c.mu.Unlock()
		return nil, &deadError{}
	}
	c.pending[id] = ch
	c.mu.Unlock()

	// F-10: bounded by ctx (or writeFrame's own defaultWriteTimeout fallback)
	// instead of an unbounded stdin write, so a server that stopped draining
	// stdin can no longer hang every lane sharing it — this write used to run
	// before any ctx-aware select at all.
	if err := c.writeFrame(ctx, rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		c.log.Debug("lsp call write failed", "method", method, "id", id, "err", err.Error())
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		// Best-effort: tell the server to stop the in-flight request. Uses its own
		// short-lived context rather than the already-expired ctx (which would
		// make writeFrame give up immediately, before ever attempting the write) —
		// still bounded, so this cleanup send can never itself hang behind a
		// wedged server and delay the ctx.Err() we are about to return.
		cancelCtx, cancelCancel := context.WithTimeout(context.Background(), bestEffortNotifyTimeout)
		_ = c.notify(cancelCtx, "$/cancelRequest", map[string]any{"id": id})
		cancelCancel()
		c.log.Debug("lsp call canceled", "method", method, "id", id, "dur", time.Since(start).String())
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.errAny != nil {
			c.log.Debug("lsp call dead", "method", method, "id", id, "dur", time.Since(start).String(), "err", resp.errAny.Error())
			return nil, resp.errAny
		}
		if resp.Err != nil {
			c.log.Debug("lsp call err", "method", method, "id", id, "dur", time.Since(start).String(), "err", resp.Err.Error())
			return nil, resp.Err
		}
		c.log.Debug("lsp call ok", "method", method, "id", id, "dur", time.Since(start).String())
		return resp.Result, nil
	}
}

func (c *LSP) notify(ctx context.Context, method string, params any) error {
	return c.writeFrame(ctx, rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
}

// writeFrame serializes v and writes it to the server's stdin, bounding both
// the writeSem acquisition and the write itself by ctx — the F-10 fix. A
// server that stops draining stdin (wedged, but still alive from the OS's
// perspective) used to block here forever, holding the mutex every other lane
// sharing this server (and Close(), whose shutdown handshake runs through
// here too) needed. Now:
//   - ctx.Deadline() absent -> wrapped with defaultWriteTimeout, mirroring the
//     reqTimeout convention in call().
//   - On success, the write completes and the token is released immediately.
//   - On ctx expiry, the connection is declared dead and the process is
//     killed (procCancel) so the abandoned write eventually unblocks (a killed
//     process's closed pipe fails a pending Write) and releases the token in
//     the background. This *LSP is now dead: call()'s dead-check fast-fails
//     every future request on it except "shutdown", which — being routed
//     through this same bounded path — returns promptly too instead of
//     hanging Close().
func (c *LSP) writeFrame(ctx context.Context, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("navigate: marshal frame: %w", err)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultWriteTimeout)
		defer cancel()
	}

	select {
	case <-c.writeSem:
	case <-ctx.Done():
		return fmt.Errorf("navigate: acquire write lock: %w", ctx.Err())
	}

	done := make(chan error, 1)
	go func() {
		_, werr := fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n", len(body))
		if werr == nil {
			_, werr = c.stdin.Write(body)
		}
		done <- werr
	}()

	select {
	case werr := <-done:
		c.writeSem <- struct{}{} // release: the write actually finished
		if werr != nil {
			return fmt.Errorf("navigate: write frame: %w", werr)
		}
		return nil
	case <-ctx.Done():
		// The write is stuck — treat the server as dead and kill it so the
		// abandoned write unblocks. Do NOT release the token yet: a still
		// in-flight write must never race a fresh one on the same stdin. The
		// background goroutine below returns it once the killed process's
		// closed pipe fails the pending write, which is the only path anything
		// could still be waiting on it (every future call on this dead *LSP
		// fails before reaching here, except the exempt "shutdown"/"exit"
		// calls Close() makes, which get their own fresh bounded ctx and so
		// time out here too rather than hanging).
		c.dead.Store(true)
		c.procCancel()
		go func() {
			<-done
			c.writeSem <- struct{}{}
		}()
		return fmt.Errorf("navigate: write frame timed out on a wedged server: %w", ctx.Err())
	}
}

// readLoop parses framed messages and dispatches them: responses to their
// waiting caller, server→client requests to a minimal auto-responder, and
// notifications to the floor.
func (c *LSP) readLoop(stdout io.Reader) {
	// A reader-side panic (e.g. an unforeseen malformed frame) must end this
	// server, not crash the whole host process — every other pooled server and
	// caller runs in the same binary.
	defer func() {
		if p := recover(); p != nil {
			c.dead.Store(true)
			err := fmt.Errorf("navigate: lsp readLoop panic: %v", p)
			c.log.Debug("lsp readLoop died", "err", err.Error())
			c.failAllPending(err)
		}
	}()
	r := bufio.NewReader(stdout)
	for {
		body, err := readFrame(r)
		if err != nil {
			c.dead.Store(true)
			c.log.Debug("lsp readLoop died", "err", err.Error())
			c.failAllPending(err)
			return
		}
		var msg rpcMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			continue // skip undecodable frames rather than tearing down
		}
		switch {
		case msg.ID != nil && msg.Method == "":
			// Response to one of our requests.
			id, ok := numericID(*msg.ID)
			if !ok {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- rpcResponse{Result: msg.Result, Err: msg.Error}
			}
		case msg.ID != nil && msg.Method != "":
			// Server→client request: answer so startup can complete.
			c.answerServerRequest(*msg.ID, msg.Method, msg.Params)
		default:
			// Notification (publishDiagnostics, $/progress, logMessage): ignore.
		}
	}
}

// answerServerRequest replies to the handful of requests a server issues during
// startup. workspace/configuration must return an array whose length matches the
// requested items ("use defaults" = null per item); the LSP spec requires the
// lengths match, and while gopls tolerates an under-length array another server
// may not, so we parse the item count. Everything else takes a null result.
func (c *LSP) answerServerRequest(id json.RawMessage, method string, params json.RawMessage) {
	var result any
	if method == "workspace/configuration" {
		var p struct {
			Items []json.RawMessage `json:"items"`
		}
		n := 1
		if err := json.Unmarshal(params, &p); err == nil && len(p.Items) > 0 {
			n = len(p.Items)
		}
		nulls := make([]any, n) // each element is nil -> server falls back to defaults
		result = nulls
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result}
	c.log.Debug("lsp answer server request", "method", method)
	// Runs synchronously on the readLoop goroutine with no caller-scoped ctx
	// available; writeFrame's own defaultWriteTimeout fallback (F-10) still
	// bounds it, so a wedged server can no longer stall the read loop forever.
	_ = c.writeFrame(context.Background(), resp)
}

func (c *LSP) failAllPending(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		// Wrap as the typed sentinel so callers can branch with
		// errors.Is(err, ErrServerDead); the underlying reader error is preserved.
		ch <- rpcResponse{errAny: &deadError{err: err}}
		delete(c.pending, id)
	}
}

// maxFrameBytes caps a single Content-Length framed message body. A buggy or
// hostile language server (binaries are on PATH, semi-trusted) can otherwise
// declare an arbitrarily large but in-range length and drive the make([]byte,
// contentLen) below into a panic ("makeslice: len out of range") or an OOM;
// real LSP messages never approach this size.
const maxFrameBytes = 256 << 20 // 256 MiB

// maxHeaderLineBytes caps a single header line while readFrame hunts for its
// terminating '\n'. Real Content-Length/header lines are a handful of bytes;
// without this cap a buggy or hostile server that never sends a newline would
// make r.ReadString('\n') buffer the connection's bytes without bound.
const maxHeaderLineBytes = 8 << 10 // 8 KiB

// readHeaderLine reads one '\n'-terminated line, erroring once the
// accumulated, still-unterminated line exceeds maxHeaderLineBytes instead of
// buffering forever.
func readHeaderLine(r *bufio.Reader) (string, error) {
	var line []byte
	for {
		frag, err := r.ReadSlice('\n')
		line = append(line, frag...)
		if len(line) > maxHeaderLineBytes {
			return "", fmt.Errorf("navigate: header line exceeds %d byte cap", maxHeaderLineBytes)
		}
		if err == nil {
			return string(line), nil
		}
		if err != bufio.ErrBufferFull {
			return "", err
		}
	}
}

// readFrame reads one Content-Length framed JSON-RPC message body.
func readFrame(r *bufio.Reader) ([]byte, error) {
	var contentLen int
	for {
		line, err := readHeaderLine(r)
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // end of headers
		}
		if name, val, ok := strings.Cut(line, ":"); ok {
			if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
				n, err := strconv.Atoi(strings.TrimSpace(val))
				if err != nil {
					return nil, fmt.Errorf("navigate: bad Content-Length %q: %w", val, err)
				}
				contentLen = n
			}
		}
	}
	if contentLen <= 0 {
		return nil, fmt.Errorf("navigate: missing Content-Length")
	}
	if contentLen > maxFrameBytes {
		return nil, fmt.Errorf("navigate: Content-Length %d exceeds %d byte cap", contentLen, maxFrameBytes)
	}
	body := make([]byte, contentLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// --- coordinate / path helpers ---------------------------------------------

// posToLSP converts a 1-based byte (line,col) cursor to a 0-based LSP position.
// With utf-8 encoding negotiated, character is a byte offset within the line.
// Pos is documented as 1-based, but exported Pool/LSP methods accept any Pos
// from a direct library caller; Line/Col below 1 clamp to LSP's zero floor
// rather than going negative (a negative LSP position is invalid per spec and
// server behavior on receiving one is undefined).
func posToLSP(p Pos) lspPosition {
	line, col := p.Line, p.Col
	if line < 1 {
		line = 1
	}
	if col < 1 {
		col = 1
	}
	return lspPosition{Line: line - 1, Character: col - 1}
}

func lspToPos(file string, p lspPosition) Pos {
	return Pos{File: file, Line: p.Line + 1, Col: p.Character + 1}
}

func numericID(raw json.RawMessage) (int64, bool) {
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	return 0, false
}

func pathToURI(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	abs = filepath.ToSlash(abs)
	if !strings.HasPrefix(abs, "/") {
		abs = "/" + abs // Windows drive letters; good enough for the spike
	}
	return "file://" + abs
}

func uriToPath(uri string) string {
	s := strings.TrimPrefix(uri, "file://")
	return filepath.FromSlash(s)
}

// languageID maps a file extension to the LSP didOpen languageId. This is a
// DISTINCT concept from the registry language (extToLang / LanguageForPath,
// which picks WHICH server): a .tsx file routes to the "typescript" server but
// carries the "typescriptreact" didOpen languageId; a .m file routes to the
// "cpp" (clangd) server but is "objective-c". Routing uses LanguageForPath; the
// didOpen body uses this.
func languageID(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts", ".mts", ".cts":
		return "typescript"
	case ".tsx":
		return "typescriptreact"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".jsx":
		return "javascriptreact"
	case ".py", ".pyi":
		return "python"
	case ".rs":
		return "rust"
	case ".cs", ".csx":
		return "csharp"
	case ".sql":
		return "sql"
	case ".cfm", ".cfml":
		return "cfml"
	case ".cfc":
		return "cfml" // cfscript components; cflsp treats them as cfml
	case ".scss", ".sass":
		return "scss"
	case ".less":
		return "less"
	case ".css":
		return "css"
	case ".html", ".htm":
		return "html"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".cxx", ".hpp", ".hh", ".hxx":
		return "cpp"
	case ".m":
		return "objective-c"
	case ".mm":
		return "objective-cpp"
	default:
		return "plaintext"
	}
}
