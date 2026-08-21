package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/graph/cluster"
	"moedex/internal/graph/diskgraph"
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
	if _, _, err := server.BuildGraph(dir); err != nil {
		t.Fatal(err)
	}
	if err := cluster.Save(server.ClusterPath(dir), cluster.Sidecar{
		Version: cluster.SidecarVersion, Generation: diskgraph.FirstGeneration,
		Status: cluster.StatusAvailable, ObservedNodes: 1, ObservedEdges: 1, EligibleNodes: 1, EligibleEdges: 1, Cap: server.DefaultClusterMaxNodes,
		Clusters: []cluster.Cluster{{
			ClusterID: 1, Label: "billing", MemberCount: 1,
			Members: []cluster.Node{{ID: "cluster-sha:23", Name: "StandaloneService", Repo: "billing", Path: "service.go", Line: 3}},
		}},
	}); err != nil {
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
	for _, want := range []string{`"cluster_id":1`, `"label":"billing"`, `"member_count":1`, `"total":1`} {
		if !strings.Contains(called.Body.String(), want) {
			t.Errorf("list_clusters response missing %s: %s", want, called.Body.String())
		}
	}
	if strings.Contains(called.Body.String(), `"members"`) {
		t.Errorf("list_clusters summary unexpectedly included members: %s", called.Body.String())
	}

	detail := postMCP(t, handler, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_clusters","arguments":{"cluster_id":1,"offset":0,"limit":1}}}`, "")
	if detail.Code != 200 {
		t.Fatalf("list_clusters detail status = %d, want 200: %s", detail.Code, detail.Body.String())
	}
	for _, want := range []string{`"cluster_id":1`, `"total_members":1`, `"members":[`, `"name":"StandaloneService"`} {
		if !strings.Contains(detail.Body.String(), want) {
			t.Errorf("list_clusters detail response missing %s: %s", want, detail.Body.String())
		}
	}
}
