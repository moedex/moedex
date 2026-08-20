package eval_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	graphcore "moedex/internal/eval"
	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
	"moedex/internal/server"
)

func TestHermeticGraphGoldGate(t *testing.T) {
	dir := t.TempDir()
	fixtureRoot := filepath.Join("testdata", "graph-corpus")
	paths := []string{"app/root.go", "app/store.go", "events/consumer.cs", "web/components.ts"}
	ix := index.New()
	contents := make(map[string][]byte)
	for _, rel := range paths {
		content, err := os.ReadFile(filepath.Join(fixtureRoot, rel))
		if err != nil {
			t.Fatal(err)
		}
		contents[rel] = content
		ix.AddFile("hermetic", rel, filepath.Join(dir, rel), "sha-"+strings.ReplaceAll(rel, "/", "-"), content)
	}
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}
	key := func(rel, symbol string) diskgraph.Key {
		off := strings.Index(string(contents[rel]), symbol)
		if off < 0 {
			t.Fatalf("%s lacks %s", rel, symbol)
		}
		return diskgraph.Key{BlobSHA: "sha-" + strings.ReplaceAll(rel, "/", "-"), SymbolOffset: uint64(off)}
	}
	root, save := key("app/root.go", "Root"), key("app/store.go", "Save")
	database := key("app/store.go", "Database")
	event, consumer := key("events/consumer.cs", "OrderSubmitted"), key("events/consumer.cs", "OrderConsumer")
	card, dashboard := key("web/components.ts", "AccountCard"), key("web/components.ts", "Dashboard")
	builder := diskgraph.NewBuilder()
	add := func(source diskgraph.Key, typ diskgraph.EdgeType, target diskgraph.Key, tier graph.ConfidenceTier) {
		t.Helper()
		if err := builder.AddEdge(source, diskgraph.Edge{
			Type: typ, TargetBlob: target.BlobSHA, TargetOffset: target.SymbolOffset, Confidence: tier,
			Evidence: graph.Evidence{BlobSHA: source.BlobSHA, ByteOffset: source.SymbolOffset, ByteLength: 1},
		}); err != nil {
			t.Fatal(err)
		}
	}
	add(root, diskgraph.EdgeCalls, save, graph.Pattern)
	add(root, diskgraph.EdgeCalls, card, graph.Candidate)
	add(save, diskgraph.EdgeUsesType, database, graph.Proven)
	add(consumer, diskgraph.EdgeConsumes, event, graph.Verified)
	add(dashboard, diskgraph.EdgeRenders, card, graph.Verified)
	if err := builder.Save(server.GraphPath(dir)); err != nil {
		t.Fatal(err)
	}

	tools, err := server.OpenGraphTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer tools.Close()
	searcher, err := server.OpenRank(context.Background(), dir, server.RankConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer searcher.Close()

	n := func(rel, symbol string) string { return "hermetic/" + rel + "#" + symbol }
	e := func(source, typ, target string) string { return source + "|" + typ + "|" + target }
	gold := []graphcore.GraphGold{
		{Name: "callers and callees", Tool: "trace_calls", Arguments: []byte(`{"symbol":"Root","hops":1}`),
			MinConfidence: "Pattern", Coverage: []string{"callers", "callees", "cross_repository_distractors"},
			NodeLabels: map[string]int{n("app/root.go", "Root"): 2, n("app/store.go", "Save"): 2, n("web/components.ts", "AccountCard"): -1},
			EdgeLabels: map[string]int{
				e(n("app/root.go", "Root"), "calls", n("app/store.go", "Save")):             2,
				e(n("app/root.go", "Root"), "calls", n("web/components.ts", "AccountCard")): -1,
			},
			Search: graphcore.GraphSearchGold{Query: "Root Save", TokenBudget: 4, TopK: 3}},
		{Name: "event consumers", Tool: "trace_consumers", Arguments: []byte(`{"name":"OrderSubmitted"}`),
			MinConfidence: "Pattern", Coverage: []string{"consumers"},
			NodeLabels: map[string]int{n("events/consumer.cs", "OrderSubmitted"): 2, n("events/consumer.cs", "OrderConsumer"): 2},
			EdgeLabels: map[string]int{e(n("events/consumer.cs", "OrderConsumer"), "consumes", n("events/consumer.cs", "OrderSubmitted")): 2},
			Search:     graphcore.GraphSearchGold{Query: "OrderSubmitted", TokenBudget: 4, TopK: 3}},
		{Name: "component rendering", Tool: "trace_renders", Arguments: []byte(`{"symbol":"Dashboard","hops":1}`),
			MinConfidence: "Pattern", Coverage: []string{"rendering"},
			NodeLabels: map[string]int{n("web/components.ts", "Dashboard"): 2, n("web/components.ts", "AccountCard"): 2},
			EdgeLabels: map[string]int{e(n("web/components.ts", "Dashboard"), "renders", n("web/components.ts", "AccountCard")): 2},
			Search:     graphcore.GraphSearchGold{Query: "Dashboard", TokenBudget: 4, TopK: 3}},
		{Name: "mixed dependency neighborhood", Tool: "graph_neighbors", Arguments: []byte(`{"symbol":"Save","hops":1}`),
			MinConfidence: "Pattern", Coverage: []string{"dependencies"},
			NodeLabels: map[string]int{n("app/store.go", "Save"): 2, n("app/store.go", "Database"): 2, n("app/root.go", "Root"): 1},
			EdgeLabels: map[string]int{
				e(n("app/root.go", "Root"), "calls", n("app/store.go", "Save")):          1,
				e(n("app/store.go", "Save"), "uses_type", n("app/store.go", "Database")): 2,
			}, Search: graphcore.GraphSearchGold{Query: "Database Save", TokenBudget: 4, TopK: 3}},
	}
	report, err := graphcore.NewGraphRunner(tools.Tools(), searcher).Run(context.Background(), gold, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := graphcore.HermeticGraphFloors().Check(report); err != nil {
		t.Fatal(err)
	}
	calibrated := graphcore.CalibrateGraphFloors(report)
	if calibrated.Recall < 0.9299 || calibrated.MRR < 0.9299 || calibrated.TierPrecision["Pattern"] != 0.90 {
		t.Fatalf("mechanical floor calibration drifted: %+v", calibrated)
	}
	t.Logf("graph baseline: recall=%.4f precision=%.4f MRR=%.4f NDCG=%.4f UDCG=%.4f tiers=%v mean-tool-latency=%s",
		report.MeanRecall, report.MeanPrec, report.MeanMRR, report.MeanNDCG, report.MeanUDCG, report.PerTierPrecision, report.MeanToolDuration)
}
