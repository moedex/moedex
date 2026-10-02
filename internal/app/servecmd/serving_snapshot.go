package servecmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"moedex/internal/contextwin"
	graphserve "moedex/internal/graph/serve"
	"moedex/internal/mcp"
	"moedex/internal/semanticindex"
	server "moedex/internal/serve"
	"moedex/internal/snapshot"
)

var errServingClosed = errors.New("serving snapshot unavailable: holder closed")

// servingSnapshot owns the entire source/graph pair. Its graph toolset is never
// reloaded independently. A lease keeps both components alive through retrieval,
// annotation and rendering; standalone tools hold the same pair for their call.
type servingSnapshot struct {
	rank                                                               *server.RankCorpus
	graph                                                              *graphserve.GraphToolset
	compiler                                                           *semanticindex.Index
	compilerSnapshotID, compilerArtifactSHA, compilerCorpusFingerprint string
	tools                                                              map[string]mcp.ToolHandler
	readers                                                            sync.WaitGroup
}

func newServingSnapshot(rank *server.RankCorpus, graph *graphserve.GraphToolset) *servingSnapshot {
	s := &servingSnapshot{rank: rank, graph: graph, tools: make(map[string]mcp.ToolHandler)}
	for _, tool := range graph.Tools() {
		s.tools[tool.Name()] = tool
	}
	return s
}

func (s *servingSnapshot) retire() error {
	s.readers.Wait()
	var compilerErr error
	if s.compiler != nil {
		compilerErr = s.compiler.Close()
	}
	return errors.Join(compilerErr, closeServingComponents(s.graph, s.rank))
}

func closeServingComponents(graph, rank io.Closer) error {
	return errors.Join(graph.Close(), rank.Close())
}

// servingHolder publishes whole immutable pairs under mu. lifecycle serializes
// preparation, publication, retirement and shutdown without blocking acquisition
// while a replacement opens or old readers drain. No cross-request pin is implied.
type servingHolder struct {
	mu        sync.Mutex
	lifecycle sync.Mutex
	cur       *servingSnapshot
	closed    bool
	closeErr  error
}

func newServingHolder(rank *server.RankCorpus, graph *graphserve.GraphToolset) (*servingHolder, error) {
	s := newServingSnapshot(rank, graph)
	if err := s.validate(); err != nil {
		return nil, err
	}
	return &servingHolder{cur: s}, nil
}

func (s *servingSnapshot) validate() error {
	rankSource, graphSource := s.rank.CorpusFingerprint(), s.graph.CorpusFingerprint()
	if rankSource != "" && graphSource != "" && rankSource != graphSource {
		return errors.New("source and graph changed during snapshot preparation")
	}
	return nil
}

// publish is the sole whole-pair swap. A mismatched prepared pair never becomes
// visible. Caller holds lifecycle and owns retiring either next on error or old
// on success; validation uses captured identities, not mutable disk paths.
func (h *servingHolder) publish(next *servingSnapshot) (*servingSnapshot, error) {
	if err := next.validate(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, errServingClosed
	}
	old := h.cur
	h.cur = next
	return old, nil
}

func (h *servingHolder) acquire(ctx context.Context) (*servingSnapshot, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.cur == nil {
		return nil, nil, errServingClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	s := h.cur
	s.readers.Add(1)
	var once sync.Once
	return s, func() { once.Do(s.readers.Done) }, nil
}

func (h *servingHolder) AcquireContextSession(ctx context.Context) (mcp.ContextSession, error) {
	s, release, err := h.acquire(ctx)
	if err != nil {
		return mcp.ContextSession{}, err
	}
	return mcp.ContextSession{Searcher: s.rank, Graph: s.graph, Release: release, CorpusRoot: s.rank.CorpusRoot()}, nil
}

func (h *servingHolder) SearchContext(ctx context.Context, query string, budget, topK int) (contextwin.ContextWindow, error) {
	s, release, err := h.acquire(ctx)
	if err != nil {
		return contextwin.ContextWindow{}, err
	}
	defer release()
	return s.rank.SearchContext(ctx, query, budget, topK)
}

func (h *servingHolder) SearchContextWithSnapshot(ctx context.Context, query string, budget, topK int) (mcp.ContextSearchResult, error) {
	s, release, err := h.acquire(ctx)
	if err != nil {
		return mcp.ContextSearchResult{}, err
	}
	defer release()
	return s.rank.SearchContextWithSnapshot(ctx, query, budget, topK)
}

// acquireRank is used only by short metrics scrapes, which still must keep the
// mapped rank corpus alive until every gauge has been read.
func (h *servingHolder) acquireRank(ctx context.Context) (*server.RankCorpus, func(), error) {
	s, release, err := h.acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	return s.rank, release, nil
}

func (h *rankHolder) acquireRank(ctx context.Context) (*server.RankCorpus, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	s := h.acquire()
	return s.rc, s.release, nil
}

// openServingGraph permits an explicitly absent optional graph, but an existing
// unreadable/corrupt artifact must fail preparation rather than silently degrade.
func openServingGraph(dir string) (*graphserve.GraphToolset, error) {
	if _, err := os.Stat(server.GraphPath(dir)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			slog.Info("graph artifact absent; preparing source-only serving snapshot")
			return graphserve.OpenSourceTools(dir)
		}
		return nil, err
	}
	return graphserve.OpenGraphToolsStrict(dir)
}

// Reload prepares a complete pair before the sole publication point. Open errors
// retain the old pair; close errors mean publication succeeded but reclaiming the
// retired pair failed. New requests can acquire immediately after publication,
// even while this call waits for older leases to finish.
func (h *servingHolder) Reload(ctx context.Context, dir string, cfg server.RankConfig) (openErr, closeErr error) {
	return h.reload(ctx, dir, cfg, nil)
}

func (h *servingHolder) ReloadResolved(ctx context.Context, resolved snapshot.Resolved, cfg server.RankConfig) (openErr, closeErr error) {
	return h.reload(ctx, resolved.ShardDir(), cfg, &resolved)
}

func (h *servingHolder) reload(ctx context.Context, dir string, cfg server.RankConfig, resolved *snapshot.Resolved) (openErr, closeErr error) {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	if err := ctx.Err(); err != nil {
		return err, nil
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return errServingClosed, nil
	}
	rank, err := server.OpenRank(ctx, dir, cfg)
	if err != nil {
		return err, nil
	}
	graph, err := openServingGraph(dir)
	if err != nil {
		return errors.Join(err, rank.Close()), nil
	}
	next := newServingSnapshot(rank, graph)
	if resolved != nil {
		if err := next.openCompiler(*resolved); err != nil {
			return errors.Join(err, next.retire()), nil
		}
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, next.retire()), nil
	}
	old, err := h.publish(next)
	if err != nil {
		return errors.Join(err, next.retire()), nil
	}
	return nil, old.retire()
}

func (h *servingHolder) Close() error {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return h.closeErr
	}
	h.closed = true
	old := h.cur
	h.cur = nil
	h.mu.Unlock()
	if old != nil {
		h.closeErr = old.retire()
	}
	return h.closeErr
}

// Tools captures only process-stable specifications. Every call resolves its
// handler in the leased current pair, never in the boot generation's toolset.
func (h *servingHolder) Tools() []mcp.ToolHandler {
	s, release, err := h.acquire(context.Background())
	if err != nil {
		return nil
	}
	defer release()
	var tools []mcp.ToolHandler
	for _, tool := range s.graph.Tools() {
		tools = append(tools, &servingTool{holder: h, spec: tool.Specification()})
	}
	tools = append(tools, mcp.CompilerTools(h)...)
	return tools
}

type servingTool struct {
	holder *servingHolder
	spec   mcp.ToolSpecification
}

func (t *servingTool) Name() string                         { return t.spec.Name }
func (t *servingTool) Specification() mcp.ToolSpecification { return t.spec }
func (t *servingTool) Call(ctx context.Context, arguments json.RawMessage) (map[string]interface{}, error) {
	s, release, err := t.holder.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	tool := s.tools[t.Name()]
	if tool == nil {
		return nil, fmt.Errorf("serving tool unavailable: %s", t.Name())
	}
	return tool.Call(ctx, arguments)
}

// startServingReloads owns its signal subscription and worker lifetime. Cleanup
// cancels any preparation, stops accepting signals and joins the worker before
// the holder is closed by the caller's next deferred cleanup.
func startServingReloads(parent context.Context, reload func(context.Context)) func() {
	ctx, cancel := context.WithCancel(parent)
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				if ctx.Err() == nil {
					reload(ctx)
				}
			}
		}
	}()
	return func() { signal.Stop(hup); cancel(); <-done }
}
