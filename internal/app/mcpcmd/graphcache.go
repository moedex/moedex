package mcpcmd

// graphcache.go stages an ad-hoc `-repo` ingest through the same on-disk shard
// + sidecar + graph layout `moedex index build`/`graph build` produce, so the
// stdio server can wire in the same graph/symbol MCP tools the warm daemon
// (internal/app/servecmd) exposes — not just search_context. The staged
// directory is keyed by the sorted set of ingested blob SHAs and kept under
// moedex's mutable-state home (internal/config.AppDir), so an unchanged repo
// reuses its build across sessions and a changed one gets a fresh directory.
// There is no eviction yet: stale ad-hoc caches accumulate under
// mcp-adhoc-shards until something (a future `moedex doctor`/`config` sweep)
// prunes them.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"moedex/internal/config"
	"moedex/internal/diskstore"
	graphbuild "moedex/internal/graph/build"
	graphserve "moedex/internal/graph/serve"
	"moedex/internal/index"
	"moedex/internal/ingest"
	"moedex/internal/serve"
)

// adhocCacheKey hashes the sorted set of blob SHAs ingest.Repo produced — the
// same content identity graphbuild's incremental refresh keys off — into a
// stable directory name: unchanged content, same key, same directory.
func adhocCacheKey(files []ingest.File) string {
	shas := make([]string, len(files))
	for i, f := range files {
		shas[i] = f.SHA
	}
	sort.Strings(shas)
	h := sha256.New()
	for _, sha := range shas {
		h.Write([]byte(sha))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// adhocGraphDir returns the cache directory for this ingest and whether it
// already holds a finished graph build.
func adhocGraphDir(files []ingest.File) (dir string, ready bool) {
	dir = config.DefaultHomePath("mcp-adhoc-shards", adhocCacheKey(files))
	_, err := os.Stat(graphbuild.GraphPath(dir))
	return dir, err == nil
}

// buildAdhocGraphTools stages ix into dir as a single-shard corpus (skipping
// the stage entirely when a finished build is already cached there), then
// opens the graph toolset from it. Callers own the result and must Close it.
func buildAdhocGraphTools(dir string, ix *index.Index, ready bool) (*graphserve.GraphToolset, error) {
	if !ready {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("adhoc graph cache: %w", err)
		}
		if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
			return nil, fmt.Errorf("adhoc graph cache: save shard: %w", err)
		}
		if _, _, err := serve.BuildSidecars(dir); err != nil {
			return nil, fmt.Errorf("adhoc graph cache: build sidecars: %w", err)
		}
		if _, _, err := graphbuild.RefreshGraph(dir); err != nil {
			return nil, fmt.Errorf("adhoc graph cache: build graph: %w", err)
		}
	}
	return graphserve.OpenGraphTools(dir)
}
