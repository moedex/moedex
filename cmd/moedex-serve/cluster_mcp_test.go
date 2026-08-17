package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/mcp"
	"moedex/internal/server"
)

func TestMCPHTTPListsAndCallsListClusters(t *testing.T) {
	dir := t.TempDir()
	ix := index.New()
	content := []byte("package service\n\nfunc StandaloneService() {}\n")
	ix.AddFile("billing", "service.go", filepath.Join(dir, "billing", "service.go"), "cluster-sha", content)
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.BuildGraphSidecar(dir); err != nil {
		t.Fatal(err)
	}

	ranked, err := server.OpenRank(context.Background(), dir, server.RankConfig{TopK: 5})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ranked.Close() })
	graphTools, err := server.OpenGraphTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = graphTools.Close() })
	handler := mcp.NewServer(newRankHolder(ranked), mcp.WithTools(graphTools.Tools()...)).HTTPHandler()

	listed := postMCP(t, handler, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "")
	if listed.Code != 200 || !strings.Contains(listed.Body.String(), `"list_clusters"`) {
		t.Fatalf("tools/list did not expose list_clusters: status=%d body=%s", listed.Code, listed.Body.String())
	}
	called := postMCP(t, handler, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_clusters","arguments":{}}}`, "")
	if called.Code != 200 {
		t.Fatalf("list_clusters status = %d, want 200: %s", called.Code, called.Body.String())
	}
	for _, want := range []string{`"cluster_id":1`, `"label":"billing"`, `"member_count":1`, `"members"`} {
		if !strings.Contains(called.Body.String(), want) {
			t.Errorf("list_clusters response missing %s: %s", want, called.Body.String())
		}
	}
}
