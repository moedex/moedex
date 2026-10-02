package servecmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	server "moedex/internal/serve"
	"moedex/internal/snapshot"
)

// The producer owns semantic JSON validation. These tests need only opaque
// manifest-bound bytes to exercise serving's streaming integrity boundary.
func publishSemanticReloadFixture(t *testing.T, indexDir, id string) string {
	t.Helper()
	tx, err := snapshot.Begin(indexDir, id)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Abort()
	m, err := snapshot.StageLegacy(tx, makeShardDir(t, id), snapshot.Producer{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tx.Dir(), filepath.FromSlash(snapshot.SemanticPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("offline semantic attachment"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := snapshot.CaptureArtifact(tx.Dir(), snapshot.SemanticPath)
	if err != nil {
		t.Fatal(err)
	}
	m.Components[snapshot.SemanticComponent] = snapshot.Component{
		Format: snapshot.SemanticFormat, Version: snapshot.SemanticVersion,
		InputFingerprint: m.CorpusFingerprint,
		Metadata:         map[string]string{"coverage": snapshot.SemanticCoverage, "usage": snapshot.SemanticUsage},
		Artifacts:        []snapshot.Artifact{a},
	}
	if err := tx.Commit(m); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(snapshot.SnapshotPath(indexDir, id), "serve")
}

func corruptReloadAttachment(t *testing.T, indexDir, id string) {
	t.Helper()
	path := filepath.Join(snapshot.SnapshotPath(indexDir, id), filepath.FromSlash(snapshot.SemanticPath))
	if err := os.WriteFile(path, []byte("Offline semantic attachment"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPReloadResolvesCurrentAndRetainsCorpusOnSemanticCorruption(t *testing.T) {
	indexDir := t.TempDir()
	firstDir := publishSemanticReloadFixture(t, indexDir, "first")
	initial, err := server.Open(firstDir)
	if err != nil {
		t.Fatal(err)
	}
	holder := newCorpusHolder(initial)
	t.Cleanup(func() {
		s := holder.acquire()
		s.release()
		_ = s.c.Close()
	})
	cfg := httpConfig{indexDir: indexDir, shardDir: firstDir}
	publishSemanticReloadFixture(t, indexDir, "broken")
	corruptReloadAttachment(t, indexDir, "broken")
	if next, err := openHTTPReload(cfg); err == nil || next != nil {
		if next != nil {
			_ = next.Close()
		}
		t.Fatalf("corrupt CURRENT opened: corpus=%p err=%v", next, err)
	}
	active := holder.acquire()
	if active.c != initial {
		t.Error("failed preparation replaced active corpus")
	}
	if matches, _, err := active.c.Literal(context.Background(), "first"); err != nil || len(matches) != 1 {
		t.Errorf("retained corpus lost old content: matches=%d err=%v", len(matches), err)
	}
	active.release()

	goodDir := publishSemanticReloadFixture(t, indexDir, "replacement")
	resolved, err := resolveReloadDir(cfg.indexDir, cfg.shardDir, false)
	if err != nil || resolved != goodDir {
		t.Fatalf("CURRENT resolution = %q, %v; want %q", resolved, err, goodDir)
	}
	next, err := openHTTPReload(cfg)
	if err != nil {
		t.Fatal(err)
	}
	old := holder.swap(next)
	old.retire()
	active = holder.acquire()
	if active.c != next || active.c == initial {
		t.Error("valid replacement was not activated")
	}
	if matches, _, err := active.c.Literal(context.Background(), "replacement"); err != nil || len(matches) != 1 {
		t.Errorf("replacement reopened boot content: matches=%d err=%v", len(matches), err)
	}
	active.release()
}

func TestHTTPReloadLegacyDirectoryRemainsSupported(t *testing.T) {
	c, err := openHTTPReload(httpConfig{shardDir: makeShardDir(t, "legacy")})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMCPReloadResolutionRetainsPairOnSemanticCorruption(t *testing.T) {
	ctx := context.Background()
	indexDir := t.TempDir()
	firstDir := publishSemanticReloadFixture(t, indexDir, "first")
	rank, err := server.OpenRank(ctx, firstDir, server.RankConfig{})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := openServingGraph(firstDir)
	if err != nil {
		_ = rank.Close()
		t.Fatal(err)
	}
	holder, err := newServingHolder(rank, graph)
	if err != nil {
		_ = rank.Close()
		_ = graph.Close()
		t.Fatal(err)
	}
	defer holder.Close()
	initial, release, err := holder.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release()

	publishSemanticReloadFixture(t, indexDir, "broken")
	corruptReloadAttachment(t, indexDir, "broken")
	// Both MCP transports use this resolution gate before holder.Reload.
	if dir, err := resolveReloadDir(indexDir, firstDir, false); err == nil || dir != "" {
		t.Fatalf("corrupt MCP replacement resolved: dir=%q err=%v", dir, err)
	}
	active, release, err := holder.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active != initial || active.rank != rank || active.graph != graph {
		t.Error("failed resolution changed the active rank/graph pair")
	}
	release()
	goodDir := publishSemanticReloadFixture(t, indexDir, "replacement")
	dir, err := resolveReloadDir(indexDir, firstDir, false)
	if err != nil || dir != goodDir {
		t.Fatalf("valid MCP replacement resolution = %q, %v", dir, err)
	}
	if openErr, closeErr := holder.Reload(ctx, dir, server.RankConfig{}); openErr != nil || closeErr != nil {
		t.Fatalf("replacement failed: open=%v close=%v", openErr, closeErr)
	}
	active, release, err = holder.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active == initial || active.rank == rank {
		t.Error("valid MCP replacement did not activate")
	}
	release()
}
