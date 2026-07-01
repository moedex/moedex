//go:build lsp

package navigate

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestPool_WorkspaceSymbol_NoLangNoLiveServers_ReturnsEmpty pins the ADR 0018
// "coverage honesty" rule: a lang-less find_symbol call merges only servers
// ALREADY live for root — it never spawns one purely to search a name. No
// gopls needed: a Pool with zero entries must return (nil, nil) and must not
// create any entry as a side effect.
func TestPool_WorkspaceSymbol_NoLangNoLiveServers_ReturnsEmpty(t *testing.T) {
	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	syms, err := pool.WorkspaceSymbol(ctx, "/some/root", "", "Circle")
	if err != nil {
		t.Fatalf("WorkspaceSymbol: %v", err)
	}
	if len(syms) != 0 {
		t.Errorf("expected no results with no live servers, got %v", syms)
	}
	pool.mu.Lock()
	n := len(pool.entries)
	pool.mu.Unlock()
	if n != 0 {
		t.Errorf("expected no pooled servers spawned by a lang-less call, got %d", n)
	}
}

// TestPool_WorkspaceSymbol_ExplicitLang_SpawnsAndQueries proves the explicit-
// lang path behaves like the file-routed queries: it creates the (lang,root)
// server on demand (rather than requiring it to already be live) and returns
// results from it.
func TestPool_WorkspaceSymbol_ExplicitLang_SpawnsAndQueries(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping pool workspace/symbol test")
	}
	root, shapeFile, _ := writeFixture(t)
	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })

	syms := querySymbolsWithRetryPool(t, func(ctx context.Context) ([]Symbol, error) {
		return pool.WorkspaceSymbol(ctx, root, "go", "Circle")
	})
	if len(syms) == 0 {
		t.Fatal("WorkspaceSymbol returned no results")
	}
	declLine := findPos(t, shapeFile, "type Circle struct", 1).Line
	var found bool
	for _, s := range syms {
		if s.Name == "Circle" && filepath.Base(s.Loc.File) == "shape.go" && s.Loc.Start.Line == declLine {
			found = true
		}
	}
	if !found {
		t.Errorf("Circle decl not found via explicit-lang WorkspaceSymbol: %v", syms)
	}
	pool.mu.Lock()
	_, ok := pool.entries[keyFor("go", root)]
	pool.mu.Unlock()
	if !ok {
		t.Error("explicit-lang WorkspaceSymbol did not spawn/register the (go,root) server")
	}
}

// TestPool_DocumentSymbol_RoutesLikeFileQueries proves DocumentSymbol is
// file-routed exactly like Definition/References/Implementations: it resolves
// (language, root) from the file's extension via workspaceRoot and creates the
// server on demand.
func TestPool_DocumentSymbol_RoutesLikeFileQueries(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping pool documentSymbol test")
	}
	root, shapeFile, _ := writeFixture(t)
	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })

	syms := querySymbolsWithRetryPool(t, func(ctx context.Context) ([]Symbol, error) {
		return pool.DocumentSymbol(ctx, shapeFile)
	})
	if len(syms) == 0 {
		t.Fatal("DocumentSymbol returned no results")
	}
	var foundCircle bool
	for _, s := range syms {
		if s.Name == "Circle" {
			foundCircle = true
		}
	}
	if !foundCircle {
		t.Errorf("Circle not found via pool.DocumentSymbol: %v", syms)
	}
	pool.mu.Lock()
	_, ok := pool.entries[keyFor("go", root)]
	pool.mu.Unlock()
	if !ok {
		t.Error("DocumentSymbol did not spawn/register the (go,root) server")
	}
}

// TestPool_WorkspaceSymbol_MergesLiveServersAcrossLanguages proves the
// polyglot-merge path: with lang omitted, every already-live server for root
// (regardless of which language key it is pooled under) is queried and
// combined. It uses the legacy Server-override mode (cfg.Server set) so BOTH
// pool keys — (go,root) and (typescript,root) — are backed by the same real
// gopls binary (lang selects the pool key but never the launched command in
// this mode), letting the test exercise a genuine two-live-server merge
// without needing a second language server installed.
func TestPool_WorkspaceSymbol_MergesLiveServersAcrossLanguages(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping pool merge test")
	}
	root, _, _ := writeFixture(t)
	pool := NewPool(Config{Server: "gopls"})
	t.Cleanup(func() { _ = pool.Close() })

	// Warm both pool keys before merging, so the merge itself never spawns
	// (matching the "only live servers contribute" contract under test).
	warmSymbols(t, pool, "go", root)
	warmSymbols(t, pool, "typescript", root)

	pool.mu.Lock()
	liveCount := len(pool.entries)
	pool.mu.Unlock()
	if liveCount != 2 {
		t.Fatalf("expected 2 live pool entries for root, got %d", liveCount)
	}

	single, err := pool.WorkspaceSymbol(context.Background(), root, "go", "Circle")
	if err != nil {
		t.Fatalf("single-lang WorkspaceSymbol: %v", err)
	}
	merged, err := pool.WorkspaceSymbol(context.Background(), root, "", "Circle")
	if err != nil {
		t.Fatalf("merged WorkspaceSymbol: %v", err)
	}
	// Two identically-configured live servers answering the same query merge to
	// twice the single-server result count — proof both were actually queried,
	// not just one.
	if len(merged) != 2*len(single) {
		t.Errorf("merged results = %d, want 2x single-server results (%d): merged=%v single=%v", len(merged), 2*len(single), merged, single)
	}
}

// querySymbolsWithRetryPool mirrors querySymbolsWithRetry for Pool-level calls.
func querySymbolsWithRetryPool(t *testing.T, fn func(context.Context) ([]Symbol, error)) []Symbol {
	t.Helper()
	var last []Symbol
	for attempt := 0; attempt < 8; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		syms, err := fn(ctx)
		cancel()
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(syms) > 0 {
			return syms
		}
		last = syms
		time.Sleep(750 * time.Millisecond)
	}
	return last
}

// warmSymbols runs a WorkspaceSymbol query under (lang,root) until it returns
// results, so the merge test above probes merge logic rather than cold-start
// latency (mirrors warm() in pool_test.go for the position-based queries).
func warmSymbols(t *testing.T, pool *Pool, lang, root string) {
	t.Helper()
	for range 12 {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		syms, err := pool.WorkspaceSymbol(ctx, root, lang, "Circle")
		cancel()
		if err == nil && len(syms) > 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("server for (%s,%s) never warmed", lang, root)
}
