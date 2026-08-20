package eval_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	graphcore "moedex/internal/eval"
	"moedex/internal/server"
)

// TestPrivateGraphGoldGate is the self-hosted tier. The reviewed labels and
// mechanically derived floors are supplied as a mounted JSON file so private
// repository identities are not published with the OSS corpus.
func TestPrivateGraphGoldGate(t *testing.T) {
	shards := os.Getenv("MOEDEX_GRAPH_EVAL_SHARDS")
	goldPath := os.Getenv("MOEDEX_GRAPH_GOLD")
	if shards == "" || goldPath == "" {
		t.Skip("set MOEDEX_GRAPH_EVAL_SHARDS and MOEDEX_GRAPH_GOLD for the reviewed private graph gate")
	}
	gold, err := graphcore.LoadGraphGoldFile(goldPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := graphcore.ValidatePrivateGraphGold(gold.Records); err != nil {
		t.Fatal(err)
	}
	if gold.Floors == nil {
		t.Fatal("private graph gold has no reviewed/mechanically-derived floors")
	}
	tools, err := server.OpenGraphTools(shards)
	if err != nil {
		t.Fatal(err)
	}
	defer tools.Close()
	searcher, err := server.OpenRank(context.Background(), shards, server.RankConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer searcher.Close()
	report, err := graphcore.NewGraphRunner(tools.Tools(), searcher).Run(context.Background(), gold.Records, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := gold.Floors.Check(report); err != nil {
		t.Fatal(err)
	}
	var clusterTool interface {
		Call(context.Context, json.RawMessage) (map[string]interface{}, error)
	}
	for _, tool := range tools.Tools() {
		if tool.Name() == "list_clusters" {
			clusterTool = tool
			break
		}
	}
	if clusterTool == nil {
		t.Fatal("production list_clusters handler is not registered")
	}
	started := time.Now()
	clusterResult, err := clusterTool.Call(context.Background(), json.RawMessage(`{"limit":50}`))
	clusterDuration := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if isError, _ := clusterResult["isError"].(bool); isError {
		t.Fatalf("list_clusters returned an error: %v", clusterResult["content"])
	}
	clusterPayload, err := json.Marshal(clusterResult["structuredContent"])
	if err != nil {
		t.Fatal(err)
	}
	var clusterStatus struct {
		Available bool `json:"available"`
	}
	if err := json.Unmarshal(clusterPayload, &clusterStatus); err != nil || !clusterStatus.Available {
		t.Fatalf("list_clusters sidecar unavailable: %s (%v)", clusterPayload, err)
	}
	t.Logf("private graph gate: n=%d recall=%.4f precision=%.4f MRR=%.4f NDCG=%.4f UDCG-watch=%.4f tier-precision=%v mean-tool-latency=%s cluster-latency=%s",
		len(report.Queries), report.MeanRecall, report.MeanPrec, report.MeanMRR, report.MeanNDCG, report.MeanUDCG, report.PerTierPrecision, report.MeanToolDuration, clusterDuration)
}
