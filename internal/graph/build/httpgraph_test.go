package graphbuild

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
)

// TestBuildGraphPersistsHTTPCallEdges is the phase-10 end-to-end proof at the
// persistence layer: two shards that share no symbol at all — a C# client and a
// Go route table — produce a route-compatible HTTP_CALLS edge because one's URL matches
// the other's route template.
func TestBuildGraphPersistsHTTPCallEdges(t *testing.T) {
	dir := t.TempDir()

	handlerContent := []byte(`package api

import "net/http"

func Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/orders/{id}", getOrder)
}

func getOrder(w http.ResponseWriter, r *http.Request) {}
`)
	callerContent := []byte(`
public class OrderClient
{
    private readonly HttpClient _http;

    public async Task<object> FetchOrder(int id)
    {
        return await _http.GetAsync($"/api/orders/{id}");
    }

    public async Task<object> FetchUser(int id)
    {
        return await _http.GetAsync($"/api/users/{id}");
    }
}
`)

	handlers := index.New()
	handlers.AddFile("orders-api", "api/routes.go", "/orders-api/api/routes.go", "handler-sha", handlerContent)
	if err := diskstore.Save(handlers, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}
	callers := index.New()
	callers.AddFile("billing-svc", "Clients/OrderClient.cs", "/billing-svc/Clients/OrderClient.cs", "caller-sha", callerContent)
	if err := diskstore.Save(callers, filepath.Join(dir, "shard-0001.idx")); err != nil {
		t.Fatal(err)
	}

	path, _, err := BuildGraph(dir)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatalf("open graph: %v", err)
	}
	defer g.Close()

	callerOffset := uint64(strings.Index(string(callerContent), "FetchOrder"))
	evidenceStart := uint64(strings.Index(string(callerContent), `$"/api/orders/{id}"`))
	handlerOffset := uint64(strings.Index(string(handlerContent), "func getOrder") + len("func "))

	var http []diskgraph.Edge
	for _, edge := range g.Load("caller-sha", callerOffset) {
		if edge.Type == diskgraph.EdgeHTTPCalls {
			http = append(http, edge)
		}
	}
	if len(http) != 1 {
		t.Fatalf("FetchOrder HTTP adjacency = %#v, want exactly one HTTP_CALLS edge", http)
	}
	got := http[0]
	if got.TargetBlob != "handler-sha" {
		t.Errorf("TargetBlob = %q, want %q", got.TargetBlob, "handler-sha")
	}
	if got.TargetOffset != handlerOffset {
		t.Errorf("TargetOffset = %d, want %d", got.TargetOffset, handlerOffset)
	}
	if got.Confidence != graph.Pattern {
		t.Errorf("Confidence = %s, want Pattern", got.Confidence)
	}
	if got.Evidence.ByteOffset != evidenceStart {
		t.Errorf("Evidence.ByteOffset = %d, want %d", got.Evidence.ByteOffset, evidenceStart)
	}

	// The /api/users caller shares no route with the corpus and must produce no
	// HTTP edge at all rather than a wrong one.
	userOffset := uint64(strings.Index(string(callerContent), "FetchUser"))
	for _, edge := range g.Load("caller-sha", userOffset) {
		if edge.Type == diskgraph.EdgeHTTPCalls {
			t.Errorf("/api/users produced an HTTP edge: %#v", edge)
		}
	}
}

func TestHTTPRouteCollisionRefreshMatchesCleanBuild(t *testing.T) {
	dir := t.TempDir()
	caller := "public class Client {\n public void Run() { http.GetAsync(\"/api/orders/42\"); http.PostAsync(\"/api/orders/42\", body); }\n}\n"
	handler := func(method, name string) string {
		return fmt.Sprintf("package api\nfunc Register() { mux.HandleFunc(%q, %s) }\nfunc %s() {}\n", method+" /api/orders/{id}", name, name)
	}
	files := []graphFile{
		{repo: "client", path: "Client.cs", content: caller},
		{repo: "orders", path: "get.go", content: handler("GET", "Read")},
		{repo: "writer", path: "post.go", content: handler("POST", "Write")},
	}
	writeGraphShards(t, dir, files, 1)
	if _, _, err := BuildGraph(dir); err != nil {
		t.Fatal(err)
	}
	check := func(path string, patterns, candidates int) {
		t.Helper()
		gotPatterns, gotCandidates := 0, 0
		for _, record := range readGraph(t, path) {
			if record.edge.Type != diskgraph.EdgeHTTPCalls {
				continue
			}
			switch record.edge.Confidence {
			case graph.Pattern:
				gotPatterns++
			case graph.Candidate:
				gotCandidates++
			default:
				t.Errorf("route-only HTTP binding overpromoted: %+v", record)
			}
			if record.edge.Evidence.BlobSHA != diskstore.GitBlobSHA1([]byte(caller)) {
				t.Fatalf("wrong evidence blob: %+v", record)
			}
			evidence, ok := record.edge.Evidence.Bytes([]byte(caller))
			if !ok || string(evidence) != `"/api/orders/42"` {
				t.Fatalf("invalid HTTP evidence: %q, %+v", evidence, record)
			}
		}
		if gotPatterns != patterns || gotCandidates != candidates {
			t.Fatalf("HTTP tiers: Pattern=%d Candidate=%d, want %d/%d", gotPatterns, gotCandidates, patterns, candidates)
		}
	}
	check(GraphPath(dir), 2, 0)
	files = append(files, graphFile{repo: "decoy", path: "get.go", content: handler("GET", "ReadDecoy")})
	for _, remove := range []bool{false, true} {
		if remove {
			files = files[:len(files)-1]
		}
		writeGraphShards(t, dir, files, 1)
		path, stats, err := RefreshGraph(dir)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Unchanged {
			t.Fatal("HTTP destination change did not refresh")
		}
		if remove {
			check(path, 2, 0)
		} else {
			check(path, 1, 2)
		}
		cleanDir := copyShards(t, dir)
		cleanPath, _, err := BuildGraph(cleanDir)
		if err != nil {
			t.Fatal(err)
		}
		requireSameGraph(t, "HTTP collision refresh", withoutGenerations(readGraph(t, path)), withoutGenerations(readGraph(t, cleanPath)))
	}
}
