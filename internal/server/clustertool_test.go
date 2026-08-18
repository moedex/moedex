package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/graph"
	"moedex/internal/graph/cluster"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
	"moedex/internal/mcp"
)

func TestListClustersToolReturnsCommunitiesAndSingletons(t *testing.T) {
	dir := t.TempDir()
	ix := index.New()
	add := func(repo, file, sha, name string) diskgraph.Key {
		content := []byte("package service\n\nfunc " + name + "() {}\n")
		ix.AddFile(repo, file, filepath.Join(dir, repo, file), sha, content)
		return diskgraph.Key{BlobSHA: sha, SymbolOffset: uint64(len("package service\n\nfunc "))}
	}
	a := add("accounts", "api.go", "sha-a", "AccountAPI")
	b := add("accounts", "store.go", "sha-b", "AccountStore")
	isolated := add("billing", "worker.go", "sha-c", "BillingWorker")
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}
	builder := diskgraph.NewBuilder()
	if err := builder.AddEdge(a, diskgraph.Edge{
		Type: diskgraph.EdgeCalls, TargetBlob: b.BlobSHA, TargetOffset: b.SymbolOffset,
		Confidence: graph.Proven,
		Evidence:   graph.Evidence{BlobSHA: a.BlobSHA, ByteOffset: a.SymbolOffset, ByteLength: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddNode(b); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddNode(isolated); err != nil {
		t.Fatal(err)
	}
	if err := builder.Save(GraphPath(dir)); err != nil {
		t.Fatal(err)
	}

	tools, err := OpenGraphTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer tools.Close()
	registered := tools.Tools()
	var clusterTool mcp.ToolHandler
	for _, tool := range registered {
		if tool.Name() == "list_clusters" {
			clusterTool = tool
			break
		}
	}
	if clusterTool == nil {
		t.Fatal("list_clusters tool not registered")
	}
	result, err := clusterTool.Call(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	communities, ok := result["structuredContent"].([]cluster.Cluster)
	if !ok {
		t.Fatalf("structuredContent type = %T, want []cluster.Cluster", result["structuredContent"])
	}
	if len(communities) != 2 {
		t.Fatalf("clusters = %#v, want connected pair and isolated singleton", communities)
	}
	var pair, singleton bool
	for _, community := range communities {
		switch community.MemberCount {
		case 2:
			pair = community.Label == "accounts"
		case 1:
			singleton = community.Label == "billing" && community.Members[0].Name == "BillingWorker"
		}
	}
	if !pair || !singleton {
		t.Errorf("communities missing expected labels/members: %#v", communities)
	}

	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"cluster_id"`, `"label"`, `"member_count"`, `"members"`} {
		if !strings.Contains(string(body), field) {
			t.Errorf("result %s missing %s", body, field)
		}
	}
}

func TestListClustersToolRejectsArguments(t *testing.T) {
	tool := &listClustersTool{}
	result, err := tool.Call(context.Background(), json.RawMessage(`{"limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if result["isError"] != true {
		t.Fatalf("unexpected result: %#v", result)
	}
}
