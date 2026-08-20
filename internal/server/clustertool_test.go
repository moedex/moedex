package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if err := builder.AddEdge(b, diskgraph.Edge{
		Type: diskgraph.EdgeCalls, TargetBlob: isolated.BlobSHA, TargetOffset: isolated.SymbolOffset,
		Confidence: graph.Pattern,
		Evidence:   graph.Evidence{BlobSHA: b.BlobSHA, ByteOffset: b.SymbolOffset, ByteLength: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := builder.Save(GraphPath(dir)); err != nil {
		t.Fatal(err)
	}
	build, err := buildClusterSidecar(dir)
	if err != nil {
		t.Fatal(err)
	}
	if build.EligibleNodes != 2 || build.EligibleEdges != 1 {
		t.Fatalf("cluster eligibility = %+v, want only the Verified/Proven edge endpoints", build)
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
	listed, ok := result["structuredContent"].(clusterListResult)
	if !ok {
		t.Fatalf("structuredContent type = %T, want clusterListResult", result["structuredContent"])
	}
	if len(listed.Clusters) != 1 || listed.Total != 1 {
		t.Fatalf("clusters = %#v, want one eligible connected community", listed)
	}
	if listed.Clusters[0].Label != "accounts" || listed.Clusters[0].MemberCount != 2 {
		t.Errorf("summary = %+v, want accounts pair", listed.Clusters[0])
	}
	detailResult, err := clusterTool.Call(context.Background(), json.RawMessage(`{"cluster_id":1,"limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	detail, ok := detailResult["structuredContent"].(clusterDetailResult)
	if !ok || detail.TotalMembers != 2 || len(detail.Cluster.Members) != 1 {
		t.Fatalf("paginated detail = %#v, want 1 of 2 members", detailResult["structuredContent"])
	}
	emptyResult, err := clusterTool.Call(context.Background(), json.RawMessage(`{"cluster_id":1,"offset":99}`))
	if err != nil {
		t.Fatal(err)
	}
	empty, ok := emptyResult["structuredContent"].(clusterDetailResult)
	if !ok || empty.Cluster.Members == nil || len(empty.Cluster.Members) != 0 {
		t.Fatalf("past-end member page = %#v, want a non-nil empty array", emptyResult["structuredContent"])
	}

	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"cluster_id"`, `"label"`, `"member_count"`} {
		if !strings.Contains(string(body), field) {
			t.Errorf("result %s missing %s", body, field)
		}
	}
}

func TestListClustersToolRejectsInvalidPagination(t *testing.T) {
	tool := &listClustersTool{}
	result, err := tool.Call(context.Background(), json.RawMessage(`{"limit":201}`))
	if err != nil {
		t.Fatal(err)
	}
	if result["isError"] != true {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestClusterBuildOverCapPublishesNoPartialCommunities(t *testing.T) {
	dir := writeClusterPair(t)
	t.Setenv("MOEDEX_GRAPH_CLUSTER_MAX_NODES", "1")
	report, err := buildClusterSidecar(dir)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != cluster.StatusOverCap || report.EligibleNodes != 2 || report.Clusters != 0 {
		t.Fatalf("over-cap report = %+v", report)
	}
	tools, err := OpenGraphTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer tools.Close()
	result, err := graphHandler(t, tools, "list_clusters").Call(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	unavailable, ok := result["structuredContent"].(clusterUnavailableResult)
	if !ok || unavailable.Status != cluster.StatusOverCap || unavailable.Cap != 1 {
		t.Fatalf("over-cap tool result = %#v", result["structuredContent"])
	}
}

func TestListClustersRejectsStaleSidecarAndHonorsCancellation(t *testing.T) {
	dir := writeClusterPair(t)
	if err := cluster.Save(ClusterPath(dir), cluster.Sidecar{
		Version: cluster.SidecarVersion, Generation: 99, Status: cluster.StatusAvailable,
		Cap: DefaultClusterMaxNodes, Clusters: []cluster.Cluster{},
	}); err != nil {
		t.Fatal(err)
	}
	tools, err := OpenGraphTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer tools.Close()
	tool := graphHandler(t, tools, "list_clusters")
	result, err := tool.Call(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	unavailable, ok := result["structuredContent"].(clusterUnavailableResult)
	if !ok || unavailable.Status != "unavailable" || !strings.Contains(unavailable.Guidance, "stale") {
		t.Fatalf("stale sidecar result = %#v", result["structuredContent"])
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Call(canceled, json.RawMessage(`{}`)); err != context.Canceled {
		t.Fatalf("canceled list_clusters error = %v, want context.Canceled", err)
	}
}

func TestPrivateGraphCorpusBehaviorAndLatency(t *testing.T) {
	dir := os.Getenv("MOEDEX_GRAPH_EVAL_SHARDS")
	if dir == "" {
		t.Skip("set MOEDEX_GRAPH_EVAL_SHARDS for corpus-scale cluster latency")
	}
	started := time.Now()
	tools, err := OpenGraphTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	openDuration := time.Since(started)
	defer tools.Close()
	tool := graphHandler(t, tools, "list_clusters")
	started = time.Now()
	summary, err := tool.Call(context.Background(), json.RawMessage(`{"limit":50}`))
	summaryDuration := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if isError, _ := summary["isError"].(bool); isError {
		t.Fatalf("cluster summary unavailable: %v", summary["content"])
	}
	started = time.Now()
	detail, err := tool.Call(context.Background(), json.RawMessage(`{"cluster_id":1,"limit":50}`))
	detailDuration := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if isError, _ := detail["isError"].(bool); isError {
		t.Fatalf("cluster detail unavailable: %v", detail["content"])
	}

	snapshot := tools.acquire()
	if snapshot == nil {
		t.Fatal("graph tools closed during corpus validation")
	}
	candidateSymbol := ""
	snapshot.graph.EachEdge(func(source diskgraph.Key, edge diskgraph.Edge) bool {
		if edge.Confidence == graph.Candidate && snapshot.nodes[source].Symbol != "" {
			candidateSymbol = snapshot.nodes[source].Symbol
			return false
		}
		return true
	})
	snapshot.wg.Done()
	if candidateSymbol == "" {
		t.Fatal("private graph has no named Candidate source to validate confidence filtering")
	}
	args, err := json.Marshal(map[string]interface{}{"symbol": candidateSymbol, "hops": 1})
	if err != nil {
		t.Fatal(err)
	}
	neighborTool := graphHandler(t, tools, "graph_neighbors")
	defaultResult, _ := callGraphTool(t, neighborTool, string(args))
	for _, edge := range defaultResult.Edges {
		if edge.Confidence.Tier == graph.Candidate {
			t.Fatalf("default Pattern floor exposed Candidate edge for %q", candidateSymbol)
		}
	}
	args, err = json.Marshal(map[string]interface{}{"symbol": candidateSymbol, "hops": 1, "min_confidence": "Candidate"})
	if err != nil {
		t.Fatal(err)
	}
	explicitResult, _ := callGraphTool(t, neighborTool, string(args))
	candidateEdges := 0
	for _, edge := range explicitResult.Edges {
		if edge.Confidence.Tier == graph.Candidate {
			candidateEdges++
		}
	}
	if candidateEdges == 0 {
		t.Fatalf("explicit Candidate floor returned no Candidate edge for %q", candidateSymbol)
	}
	t.Logf("private graph corpus: open=%s cluster-summary=%s cluster-detail=%s candidate-symbol=%q default-edges=%d explicit-candidate-edges=%d",
		openDuration, summaryDuration, detailDuration, candidateSymbol, len(defaultResult.Edges), candidateEdges)
}

func writeClusterPair(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ix := index.New()
	contentA := []byte("package pair\nfunc Alpha() {}\n")
	contentB := []byte("package pair\nfunc Beta() {}\n")
	ix.AddFile("pair", "a.go", filepath.Join(dir, "a.go"), "a-sha", contentA)
	ix.AddFile("pair", "b.go", filepath.Join(dir, "b.go"), "b-sha", contentB)
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}
	a := diskgraph.Key{BlobSHA: "a-sha", SymbolOffset: uint64(strings.Index(string(contentA), "Alpha"))}
	b := diskgraph.Key{BlobSHA: "b-sha", SymbolOffset: uint64(strings.Index(string(contentB), "Beta"))}
	builder := diskgraph.NewBuilder()
	if err := builder.AddEdge(a, diskgraph.Edge{Type: diskgraph.EdgeCalls, TargetBlob: b.BlobSHA, TargetOffset: b.SymbolOffset,
		Confidence: graph.Verified, Evidence: graph.Evidence{BlobSHA: a.BlobSHA, ByteOffset: a.SymbolOffset, ByteLength: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := builder.Save(GraphPath(dir)); err != nil {
		t.Fatal(err)
	}
	return dir
}
