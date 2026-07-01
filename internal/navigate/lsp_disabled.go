//go:build !lsp

// Default build: the LSP-precise navigation arm is NOT compiled, so the core
// stays pure-Go with zero new dependencies (ADR 0001). NewLSP returns a clear
// error telling the caller to rebuild with -tags lsp. See lsp.go for the real
// gopls-backed client.

package navigate

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// LSPCompiled reports whether the LSP navigation client was compiled into this
// binary (i.e. built with -tags lsp). False in the default pure-Go build.
const LSPCompiled = false

// errNoLSP is returned by NewLSP when the binary was built without the lsp tag.
var errNoLSP = errors.New("navigate: LSP navigation unavailable; rebuild with -tags lsp")

// LSP is a stub in the default build. The lsp-tagged build replaces it with a
// real gopls-backed Navigator.
type LSP struct{}

// Config is accepted in both builds so callers can construct it unconditionally.
// Field set MUST match lsp.go's Config exactly so callers compile identically
// across arms.
type Config struct {
	// Server is the language-server command to launch. If empty the command is
	// resolved from the registry by Language (default "go"/gopls).
	Server string
	// Args are extra arguments passed to the server command.
	Args []string
	// RootDir is the workspace root the server resolves against (default: the
	// directory of the first queried file's module).
	RootDir string
	// Language is the canonical registry language to launch when Server is empty
	// (default "go").
	Language string
	// InitOptions overrides the registry spec's initializationOptions when set.
	InitOptions map[string]any
	// Env is extra environment appended to os.Environ() for the server process.
	Env []string
	// RequestTimeout is a default client-side per-request cap (0 = none).
	RequestTimeout time.Duration
	// Logger is an optional structured debug sink (nil falls back to
	// MOEDEX_LSP_DEBUG, then discard).
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

// ErrServerDead mirrors the lsp.go sentinel so callers can branch on
// errors.Is(err, ErrServerDead) in the default build too. NewLSP fails before
// any server starts here, so it is never actually returned in this arm.
var ErrServerDead = errors.New("navigate: language server died")

// NewLSP always fails in the default build.
func NewLSP(ctx context.Context, cfg Config) (*LSP, error) { return nil, errNoLSP }

func (*LSP) Definition(context.Context, Pos) ([]Location, error)      { return nil, errNoLSP }
func (*LSP) References(context.Context, Pos, bool) ([]Location, error) { return nil, errNoLSP }
func (*LSP) Implementations(context.Context, Pos) ([]Location, error) { return nil, errNoLSP }
func (*LSP) Close() error                                             { return nil }

// Alive is part of the surface the Pool relies on. The stub is never stored by
// the Pool (NewLSP fails first), so the value is moot.
func (*LSP) Alive() bool { return false }

func (*LSP) SetOverlay(context.Context, string, []byte) error  { return errNoLSP }
func (*LSP) DropOverlay(context.Context, string) error         { return errNoLSP }
func (*LSP) NotifyChanged(context.Context, ...string) error    { return errNoLSP }

func (*LSP) WorkspaceSymbol(context.Context, string) ([]Symbol, error) { return nil, errNoLSP }
func (*LSP) DocumentSymbol(context.Context, string) ([]Symbol, error)  { return nil, errNoLSP }
