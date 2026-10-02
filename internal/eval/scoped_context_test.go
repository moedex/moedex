package eval_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	graphbuild "moedex/internal/graph/build"
	graphserve "moedex/internal/graph/serve"
	"moedex/internal/index"
	"moedex/internal/mcp"
	"moedex/internal/serve"
)

// These integration tests exercise scope through HTTP MCP, real deduplicated
// shards, production ranking and extracted graph data. Composite-holder reload
// lifetime is tested separately in servecmd, which owns that production wiring.
func TestScopedContextMatchingOccurrenceThroughHTTP(t *testing.T) {
	const shared = "package shared\n// DedupSentinel identifies the shared source bytes.\nfunc Shared() {}\n"
	dir := scopedDedupCorpus(t, map[string]map[string]string{
		"a-outside": {"copy.go": shared},
		"z-target":  {"src/item.ts": shared, "src/item.go": shared, "src2/item.go": shared, "vendor/item.go": shared},
	})
	h := scopedContextHandler(t, dir, false)
	for _, prefix := range []string{"src", "src/", "src/item.go"} {
		result := scopedContextCall(t, h, map[string]any{"query": "DedupSentinel", "repo": "z-target", "path_prefix": prefix, "language": "go", "top_k": 1, "graph_depth": 0})
		assertScopedContextSuccess(t, result)
		if result.Scope["repo"] != "z-target" || result.Scope["path_prefix"] != prefix || result.Scope["language"] != "go" {
			t.Fatalf("effective scope missing from response: %+v", result.Scope)
		}
		if len(result.Blocks) != 1 {
			t.Fatalf("matching shared occurrence missing: %+v", result)
		}
		block := result.Blocks[0]
		if block.Repo != "z-target" || block.RelPath != "src/item.go" || block.BlobSHA != diskstore.GitBlobSHA1([]byte(shared)) {
			t.Fatalf("selected ineligible occurrence from shared bytes: %+v", block)
		}
		if block.AbsPath != filepath.Join(dir, "z-target", "src/item.go") {
			t.Fatalf("absolute provenance differs from selected reference: %+v", block)
		}
	}
	// Repo is exact and case-sensitive; a missing scope is a valid empty answer.
	result := scopedContextCall(t, h, map[string]any{"query": "DedupSentinel", "repo": "Z-TARGET", "graph_depth": 0})
	assertScopedContextSuccess(t, result)
	if len(result.Blocks) != 0 {
		t.Fatalf("case-insensitive repository widening: %+v", result.Blocks)
	}
}

func TestScopedContextOutsideDecoysCannotConsumeTopK(t *testing.T) {
	repos := map[string]map[string]string{
		"a-outside": {},
		"z-target":  {"src/inside.go": "package target\n// ScopeNeedle is handled in this deliberately longer source file.\nfunc Inside() { println(\"allowed\") }\n"},
	}
	// More than the symbol/dense shortlist and requested top-K. Outside exact
	// symbol matches are stronger seeds than the eligible comment-only match.
	for i := 0; i < 96; i++ {
		repos["a-outside"][fmt.Sprintf("decoy%03d.go", i)] = fmt.Sprintf("package decoy\nfunc ScopeNeedle() {}\n// unique %d\n", i)
	}
	dir := scopedDedupCorpus(t, repos)
	h := scopedContextHandler(t, dir, false)
	result := scopedContextCall(t, h, map[string]any{"query": "ScopeNeedle", "repo": "z-target", "path_prefix": "src", "language": "go", "top_k": 1, "graph_depth": 0})
	assertScopedContextSuccess(t, result)
	if len(result.Blocks) != 1 || result.Blocks[0].Repo != "z-target" || result.Blocks[0].RelPath != "src/inside.go" {
		t.Fatalf("outside candidates starved scoped retrieval: %+v", result)
	}
}

func TestScopedContextGraphNeighborsUseMatchingLocations(t *testing.T) {
	const helper = "package target\nfunc Helper() {}\n"
	repos := map[string]map[string]string{
		"a-outside": {"copy.go": helper},
		"z-target": {
			"src/root.go":    "package target\nfunc Entry() { Helper() }\n",
			"src/helper.go":  helper,
			"src/helper.ts":  helper,
			"src2/helper.go": helper,
			"src2/caller.go": "package target\nfunc OutsideCaller() { Entry() }\n",
		},
	}
	var root strings.Builder
	root.WriteString("package target\nfunc Entry() {\n")
	// Exceed the neighbor bucket cap with lexically earlier outside targets.
	// A post-traversal/post-cap filter would lose the allowed Helper.
	for i := 0; i < 40; i++ {
		name := fmt.Sprintf("AOutside%03d", i)
		fmt.Fprintf(&root, " %s()\n", name)
		repos["z-target"][fmt.Sprintf("src2/decoy%03d.go", i)] = fmt.Sprintf("package target\nfunc %s() {}\n", name)
	}
	root.WriteString(" Helper()\n}\n")
	repos["z-target"]["src/root.go"] = root.String()
	dir := scopedDedupCorpus(t, repos)
	h := scopedContextHandler(t, dir, true)
	result := scopedContextCall(t, h, map[string]any{"query": "Entry", "repo": "z-target", "path_prefix": "src", "language": "go", "top_k": 1, "graph_depth": 1})
	assertScopedContextSuccess(t, result)
	if len(result.Blocks) == 0 {
		t.Fatal("missing scoped entry context")
	}
	foundHelper := false
	for _, block := range result.Blocks {
		if block.Repo != "z-target" || !strings.HasPrefix(block.RelPath, "src/") || filepath.Ext(block.RelPath) != ".go" {
			t.Fatalf("out-of-scope block: %+v", block)
		}
		if block.Neighbors == nil {
			t.Fatal("graph annotation missing; test would be vacuous")
		}
		for _, bucket := range []string{"callers", "callees", "consumers", "publishers", "depends_on", "similar_to"} {
			var neighbors []struct {
				Symbol  string `json:"symbol"`
				Repo    string `json:"repo"`
				RelPath string `json:"rel_path"`
			}
			if err := json.Unmarshal(block.Neighbors[bucket], &neighbors); err != nil {
				t.Fatal(err)
			}
			for _, neighbor := range neighbors {
				if neighbor.Repo != "z-target" || !strings.HasPrefix(neighbor.RelPath, "src/") || filepath.Ext(neighbor.RelPath) != ".go" {
					t.Fatalf("out-of-scope neighbor: %+v", neighbor)
				}
				if neighbor.Symbol == "Helper" && neighbor.RelPath == "src/helper.go" {
					foundHelper = true
				}
			}
		}
	}
	if !foundHelper {
		t.Fatalf("shared helper's matching location lost: %+v", result)
	}
	if result.Snapshot.GraphBuildID == "" || result.Snapshot.GraphCorpusFingerprint != "" {
		t.Fatalf("incoherent rank/graph snapshot: %+v", result.Snapshot)
	}
}

func TestScopedContextRejectsInvalidScopeThroughHTTP(t *testing.T) {
	dir := scopedDedupCorpus(t, map[string]map[string]string{"repo": {"file.go": "package p\nfunc Needle() {}\n"}})
	h := scopedContextHandler(t, dir, false)
	for _, scope := range []map[string]any{
		{"path_prefix": "/absolute"}, {"path_prefix": "../escape"}, {"path_prefix": "src/../other"},
		{"path_prefix": "src//nested"}, {"path_prefix": "src\\nested"}, {"path_prefix": "src/*"}, {"path_prefix": "./src"},
		{"language": "not-a-language"},
	} {
		args := map[string]any{"query": "Needle", "graph_depth": 0}
		for key, value := range scope {
			args[key] = value
		}
		result := scopedContextCall(t, h, args)
		if !result.IsError || result.Error.Code != "invalid_scope" || result.Snapshot.Cacheable {
			t.Errorf("invalid scope accepted or cacheable: args=%v result=%+v", args, result)
		}
	}
}

type scopedContextBlock struct {
	Repo      string                     `json:"repo"`
	RelPath   string                     `json:"rel_path"`
	AbsPath   string                     `json:"abs_path"`
	BlobSHA   string                     `json:"blob_sha"`
	Neighbors map[string]json.RawMessage `json:"neighbors"`
}
type scopedContextResponse struct {
	IsError bool
	Blocks  []scopedContextBlock
	Scope   map[string]string
	Error   struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	Snapshot mcp.SnapshotIdentity
}

func scopedContextCall(t *testing.T, h http.Handler, args map[string]any) scopedContextResponse {
	t.Helper()
	args["token_budget"] = 1024
	args["format"] = "structured"
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "search_context", "arguments": args}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var wire struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError    bool `json:"isError"`
			Structured struct {
				Blocks  []scopedContextBlock `json:"blocks"`
				Summary struct {
					Scope map[string]string `json:"scope"`
				} `json:"summary"`
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			} `json:"structuredContent"`
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil || rec.Code != 200 || len(wire.Error) > 0 {
		t.Fatalf("HTTP MCP failure %d: %v: %s", rec.Code, err, rec.Body.String())
	}
	out := scopedContextResponse{IsError: wire.Result.IsError, Blocks: wire.Result.Structured.Blocks, Error: wire.Result.Structured.Error, Scope: wire.Result.Structured.Summary.Scope}
	if err := json.Unmarshal(wire.Result.Meta[mcp.SnapshotMetaKey], &out.Snapshot); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertScopedContextSuccess(t *testing.T, result scopedContextResponse) {
	t.Helper()
	if result.IsError || result.Error.Code != "" || !result.Snapshot.Cacheable {
		t.Fatalf("scope request failed: %+v", result)
	}
}

func scopedContextHandler(t *testing.T, dir string, withGraph bool) http.Handler {
	t.Helper()
	ranker, err := serve.OpenRank(context.Background(), dir, serve.RankConfig{TopK: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ranker.Close(); err != nil {
			t.Error(err)
		}
	})
	var options []mcp.Option
	if withGraph {
		if _, _, err := graphbuild.BuildGraph(dir); err != nil {
			t.Fatal(err)
		}
		tools, err := graphserve.OpenGraphTools(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := tools.Close(); err != nil {
				t.Error(err)
			}
		})
		options = append(options, mcp.WithGraphAnnotator(tools))
	}
	return mcp.NewServer(ranker, options...).HTTPHandler()
}

func scopedDedupCorpus(t *testing.T, repos map[string]map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	writer, err := diskstore.NewContentStoreWriter()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })
	labels := make([]string, 0, len(repos))
	for repo := range repos {
		labels = append(labels, repo)
	}
	sort.Strings(labels)
	for i, repo := range labels {
		ix := index.New()
		paths := make([]string, 0, len(repos[repo]))
		for path := range repos[repo] {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			data := []byte(repos[repo][path])
			ix.AddFile(repo, path, filepath.Join(dir, repo, path), diskstore.GitBlobSHA1(data), data)
		}
		if err := diskstore.SaveDeduped(ix, filepath.Join(dir, fmt.Sprintf("shard-%04d.idx", i)), writer); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Write(filepath.Join(dir, diskstore.ContentStoreName)); err != nil {
		t.Fatal(err)
	}
	return dir
}
