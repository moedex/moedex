package graphserve

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/mcp"
)

// Compare the complete HTTP JSON response, including textual content and the
// public snapshot metadata, rather than only the graph's selected node fields.
func acceptanceCacheResponses(t *testing.T, tools *GraphToolset) []string {
	t.Helper()
	handler := mcp.NewServer(graphFakeSearcher{}, mcp.WithTools(tools.Tools()...)).HTTPHandler()
	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"name":"graph_schema","arguments":{}}`,
		`{"name":"trace_calls","arguments":{"symbol":"Root","hops":2}}`,
		`{"name":"impact_analysis","arguments":{"symbol":"Leaf","depth":2}}`,
		`{"name":"graph_neighbors","arguments":{"symbol":"Root","hops":1,"min_confidence":"Candidate"}}`,
		`{"name":"list_repos","arguments":{}}`,
		`{"name":"list_symbols","arguments":{"repo":"fixture"}}`,
		`{"name":"file_tree","arguments":{"repo":"fixture"}}`,
		`{"name":"read_source","arguments":{"repo":"fixture","path":"root.go"}}`,
	}
	var responses []string
	for _, body := range requests {
		if !strings.Contains(body, `"jsonrpc"`) {
			body = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":` + body + `}`
		}
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		var wire struct {
			Error  json.RawMessage `json:"error"`
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &wire) != nil || len(wire.Error) > 0 || wire.Result.IsError {
			t.Fatalf("request %s failed: %d %s", body, rec.Code, rec.Body.String())
		}
		responses = append(responses, rec.Body.String())
	}
	return responses
}

func acceptanceOpenCached(t *testing.T, dir string) *GraphToolset {
	t.Helper()
	tools, err := OpenGraphToolsStrict(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tools.Close() })
	return tools
}

func TestCacheAcceptanceColdWarmCompleteMCP(t *testing.T) {
	fixture := newGraphFixture(t)
	cold := acceptanceCacheResponses(t, fixture.tools)
	path := filepath.Join(fixture.dir, "graph-catalog.cache")
	// A hit must leave the optional cache untouched. Fixed old mtime makes an
	// accidental rebuild observable without timer assumptions or sleeps.
	stamp := time.Unix(123456789, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	warm := acceptanceOpenCached(t, fixture.dir)
	snap := warm.acquire()
	if snap == nil {
		t.Fatal("missing warm snapshot")
	}
	hits, shards := snap.symbols.cacheHits, snap.symbols.NumShards()
	snap.wg.Done()
	if hits != shards {
		t.Fatalf("warm symbol cache hits=%d want %d", hits, shards)
	}
	if got := acceptanceCacheResponses(t, warm); !reflect.DeepEqual(got, cold) {
		t.Fatal("cold/hit complete MCP responses differ")
	}
	info, err := os.Stat(path)
	if err != nil || !info.ModTime().Equal(stamp) {
		t.Fatalf("warm open rewrote catalog rather than hitting: %v", err)
	}
}

func TestCacheAcceptanceStaleInlineSourceAndContext(t *testing.T) {
	for _, mode := range []string{"same_size_source", "same_size_paths", "first_extension"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newGraphFixture(t)
			before := acceptanceCacheResponses(t, fixture.tools)
			shard := filepath.Join(fixture.dir, "shard-0000.idx")
			ix, err := diskstore.Load(shard)
			if err != nil {
				t.Fatal(err)
			}
			changed := false
			for i := uint64(0); i < uint64(ix.NumBlobs()); i++ {
				blob := ix.Blob(i)
				if blob.SHA != "root-sha" {
					continue
				}
				changed = true
				switch mode {
				case "same_size_source":
					blob.Content = bytes.ReplaceAll(blob.Content, []byte("Root"), []byte("Stem"))
				case "same_size_paths":
					blob.Files[0].RelPath = "stem.go"
					blob.Files[0].AbsPath = filepath.Join(fixture.dir, "stem.go")
				case "first_extension":
					blob.Files[0].RelPath = "root.zz"
					blob.Files[0].AbsPath = filepath.Join(fixture.dir, "root.zz")
				}
			}
			if !changed {
				t.Fatal("missing fixture root")
			}
			oldInfo, err := os.Stat(shard)
			if err != nil {
				t.Fatal(err)
			}
			if err := diskstore.Save(ix, shard); err != nil {
				t.Fatal(err)
			}
			newInfo, err := os.Stat(shard)
			if err != nil {
				t.Fatal(err)
			}
			if newInfo.Size() != oldInfo.Size() {
				t.Fatalf("fixture change altered shard size: %d -> %d", oldInfo.Size(), newInfo.Size())
			}

			updated := acceptanceOpenCached(t, fixture.dir)
			snap := updated.acquire()
			if snap == nil {
				t.Fatal("no updated snapshot")
			}
			defer snap.wg.Done()
			if snap.symbols.cacheHits != 0 {
				t.Fatal("stale source/context reused symbol cache")
			}
			switch mode {
			case "same_size_source":
				if len(snap.bySymbol["Root"]) != 0 || len(snap.bySymbol["Stem"]) != 1 {
					t.Fatal("stale definitions served after same-length source change")
				}
			case "same_size_paths":
				for _, key := range snap.bySymbol["Root"] {
					for _, loc := range snap.nodes[key].Locations {
						if loc.Path != "stem.go" || loc.AbsPath != filepath.Join(fixture.dir, "stem.go") {
							t.Fatalf("stale path %+v", loc)
						}
					}
				}
				if len(snap.byPath[filepath.Join(fixture.dir, "root.go")]) != 0 {
					t.Fatal("stale path index")
				}
			case "first_extension":
				if len(snap.bySymbol["Root"]) != 0 {
					t.Fatal("stale symbols after first-language extension change")
				}
			}
			// The old active snapshot remains valid after replacing an inline shard.
			if got := acceptanceCacheResponses(t, fixture.tools); !reflect.DeepEqual(got, before) {
				t.Fatal("published snapshot changed during cache regeneration")
			}
		})
	}
}

func TestCacheAcceptanceCorruptCatalogFallback(t *testing.T) {
	for _, cacheName := range []string{"graph-catalog.cache", "shard-0000.idx.graph-symbols"} {
		for _, payload := range [][]byte{nil, []byte("not a cache"), bytes.Repeat([]byte{0xff}, 128)} {
			fixture := newGraphFixture(t)
			want := acceptanceCacheResponses(t, fixture.tools)
			path := filepath.Join(fixture.dir, cacheName)
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			reopened := acceptanceOpenCached(t, fixture.dir)
			if got := acceptanceCacheResponses(t, reopened); !reflect.DeepEqual(got, want) {
				t.Fatal("corrupt optional catalog changed MCP response")
			}
		}
	}

}

func TestCacheAcceptanceConcurrentColdPublication(t *testing.T) {
	fixture := newGraphFixture(t)
	want := acceptanceCacheResponses(t, fixture.tools)
	if err := os.Remove(filepath.Join(fixture.dir, "graph-catalog.cache")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(fixture.dir, "shard-0000.idx.graph-symbols")); err != nil {
		t.Fatal(err)
	}
	const workers = 4
	results := make([]*GraphToolset, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() { <-start; results[i], errs[i] = OpenGraphToolsStrict(fixture.dir) })
	}
	close(start)
	wg.Wait()
	for i, tools := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if got := acceptanceCacheResponses(t, tools); !reflect.DeepEqual(got, want) {
			t.Errorf("concurrent publisher %d changed responses", i)
		}
		tools.Close()
	}
	reopened := acceptanceOpenCached(t, fixture.dir)
	if got := acceptanceCacheResponses(t, reopened); !reflect.DeepEqual(got, want) {
		t.Fatal("published concurrent cache cannot be reopened")
	}
}

func TestCacheAcceptanceUnwritableOptionalPaths(t *testing.T) {
	fixture := newGraphFixture(t)
	want := acceptanceCacheResponses(t, fixture.tools)
	// Directory collisions fail optional publication reliably, including when
	// tests run with privileges that make chmod-based permission tests ineffective.
	for _, name := range []string{"graph-catalog.cache", "shard-0000.idx.graph-symbols"} {
		path := filepath.Join(fixture.dir, name)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	reopened := acceptanceOpenCached(t, fixture.dir)
	if got := acceptanceCacheResponses(t, reopened); !reflect.DeepEqual(got, want) {
		t.Fatal("optional cache publication failure changed MCP response")
	}
}
