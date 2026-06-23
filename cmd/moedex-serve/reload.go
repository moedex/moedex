package main

import (
	"context"
	"sync"

	"moedex/internal/contextwin"
	"moedex/internal/server"
)

// Hot reload swaps the served corpus under live traffic without dropping a
// request. The hazard is the mmap'd retrieval Corpus: querying a shard after it
// is unmapped segfaults, so an old snapshot must not be closed until every
// in-flight query that holds it has finished.
//
// The holders below implement a reference-counted snapshot swap. A query path
// calls acquire() (which loads the current snapshot AND increments its refcount
// under one lock, so it can never attach to a snapshot already being retired) and
// release() when done. A reload builds a fresh snapshot, swap()s it in, and
// retires the old one — retire() blocks on the old snapshot's WaitGroup until its
// readers drain, then closes it. A failed reload never swaps, so the daemon keeps
// serving the last good snapshot.

// corpusSnapshot is one generation of the retrieval Corpus plus the refcount of
// queries currently using it.
type corpusSnapshot struct {
	c  *server.Corpus
	wg sync.WaitGroup
}

func (s *corpusSnapshot) release() { s.wg.Done() }

// retire waits for in-flight queries to finish, then unmaps the old corpus.
func (s *corpusSnapshot) retire() { s.wg.Wait(); _ = s.c.Close() }

// corpusHolder hot-swaps a *server.Corpus for the -http retrieval daemon.
type corpusHolder struct {
	mu  sync.Mutex
	cur *corpusSnapshot
}

func newCorpusHolder(c *server.Corpus) *corpusHolder {
	return &corpusHolder{cur: &corpusSnapshot{c: c}}
}

// acquire returns the live snapshot with its refcount incremented; the caller
// MUST release() it. Holding the lock across the Add guarantees acquire never
// races a swap into attaching to a snapshot that is about to be retired.
func (h *corpusHolder) acquire() *corpusSnapshot {
	h.mu.Lock()
	s := h.cur
	s.wg.Add(1)
	h.mu.Unlock()
	return s
}

// swap installs c as the live snapshot and returns the previous one for the
// caller to retire().
func (h *corpusHolder) swap(c *server.Corpus) *corpusSnapshot {
	ns := &corpusSnapshot{c: c}
	h.mu.Lock()
	old := h.cur
	h.cur = ns
	h.mu.Unlock()
	return old
}

// rankSnapshot is one generation of the ranked corpus. Unlike Corpus it holds no
// mmap (RankCorpus loads blob content into the heap), so a stale reference is
// merely GC'able rather than unsafe; the refcount is kept for symmetry and so a
// future resource-holding RankCorpus stays correct.
type rankSnapshot struct {
	rc *server.RankCorpus
	wg sync.WaitGroup
}

func (s *rankSnapshot) release() { s.wg.Done() }
func (s *rankSnapshot) retire()  { s.wg.Wait() } // nothing to close; drop for GC

// rankHolder hot-swaps a *server.RankCorpus and satisfies mcp.ContextSearcher, so
// the MCP server delegates every search to the current generation.
type rankHolder struct {
	mu  sync.Mutex
	cur *rankSnapshot
}

func newRankHolder(rc *server.RankCorpus) *rankHolder {
	return &rankHolder{cur: &rankSnapshot{rc: rc}}
}

func (h *rankHolder) acquire() *rankSnapshot {
	h.mu.Lock()
	s := h.cur
	s.wg.Add(1)
	h.mu.Unlock()
	return s
}

func (h *rankHolder) swap(rc *server.RankCorpus) *rankSnapshot {
	ns := &rankSnapshot{rc: rc}
	h.mu.Lock()
	old := h.cur
	h.cur = ns
	h.mu.Unlock()
	return old
}

// SearchContext implements mcp.ContextSearcher against the live generation,
// holding a reference for the duration so a concurrent reload cannot retire the
// snapshot out from under the search.
func (h *rankHolder) SearchContext(ctx context.Context, query string, tokenBudget, topK int) (contextwin.ContextWindow, error) {
	s := h.acquire()
	defer s.release()
	return s.rc.SearchContext(ctx, query, tokenBudget, topK)
}
