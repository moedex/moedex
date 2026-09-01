package graphbuild

import (
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
// Go route table — produce a served HTTP_CALLS edge because one's URL matches
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
	if got.Confidence != graph.Verified {
		t.Errorf("Confidence = %s, want Verified", got.Confidence)
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
