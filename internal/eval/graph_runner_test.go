package eval

import (
	"encoding/json"
	"testing"
)

func TestStableGraphNodeKeyRequiresIntentionalNamelessKind(t *testing.T) {
	named := graphEvalNode{ID: "sha:7", Symbol: "Handle"}
	named.Locations = append(named.Locations, struct {
		Repo string `json:"repo"`
		Path string `json:"path"`
		Line int    `json:"line"`
	}{Repo: "orders", Path: "api/handler.go"})
	if got, err := stableGraphNodeKey(named); err != nil || got != "orders/api/handler.go#Handle" {
		t.Fatalf("named stable key = %q, %v", got, err)
	}
	if got, err := stableGraphNodeKey(graphEvalNode{ID: "sha:9", Kind: "Route"}); err != nil || got != "sha:9" {
		t.Fatalf("intentional raw fallback = %q, %v", got, err)
	}
	if _, err := stableGraphNodeKey(graphEvalNode{ID: "sha:11", Kind: "unknown"}); err == nil {
		t.Fatal("unknown nameless node unexpectedly received a node-ID fallback")
	}
}

func TestStableGraphNodeKeysDisambiguateSameFileAndSymbol(t *testing.T) {
	nodes := []graphEvalNode{
		graphEvalNamedNode("type-id", "Cart", "Type", "repo", "Entities/Cart.cs", 10),
		graphEvalNamedNode("method-id", "Cart", "Method", "repo", "Entities/Cart.cs", 20),
	}
	got, err := stableGraphNodeKeys(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "repo/Entities/Cart.cs#Cart" || got[1] != "repo/Entities/Cart.cs#Cart@Method" {
		t.Fatalf("collision keys = %v", got)
	}
	if got[0] == got[1] {
		t.Fatal("same-file Type and Method collapsed to one judgment key")
	}
}

func graphEvalNamedNode(id, symbol, kind, repo, path string, line int) graphEvalNode {
	node := graphEvalNode{ID: id, Symbol: symbol, Kind: kind}
	node.Locations = append(node.Locations, struct {
		Repo string `json:"repo"`
		Path string `json:"path"`
		Line int    `json:"line"`
	}{Repo: repo, Path: path, Line: line})
	return node
}

func TestGraphEvalArgumentsRequireJSONObject(t *testing.T) {
	if _, err := graphEvalArguments(json.RawMessage(`null`), "Pattern"); err == nil {
		t.Fatal("null arguments unexpectedly accepted")
	}
	got, err := graphEvalArguments(json.RawMessage(`{"symbol":"Root"}`), "Verified")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"min_confidence":"Verified","symbol":"Root"}` {
		t.Fatalf("arguments = %s", got)
	}
}

func TestValidatePrivateGraphGoldRequiresBreadthAndDistractors(t *testing.T) {
	records := make([]GraphGold, 30)
	for i := range records {
		records[i] = GraphGold{
			Name: "reviewed", Tool: "graph_neighbors", MinConfidence: "Pattern",
			NodeLabels: map[string]int{"repo/file.go#Root": 1},
			Search:     GraphSearchGold{Query: "Root", TokenBudget: 100},
		}
	}
	for i, category := range requiredPrivateGraphCoverage {
		records[i].Coverage = []string{category}
		switch category {
		case "callers", "callees":
			records[i].Tool = "trace_calls"
		case "consumers":
			records[i].Tool = "trace_consumers"
		case "hierarchy":
			records[i].Tool = "trace_hierarchy"
		case "queries":
			records[i].Tool = "trace_queries"
		case "rendering":
			records[i].Tool = "trace_renders"
		case "impact_analysis":
			records[i].Tool = "impact_analysis"
		}
	}
	records[0].NodeLabels["other/file.go#Distractor"] = -1
	if err := ValidatePrivateGraphGold(records); err != nil {
		t.Fatalf("complete private coverage rejected: %v", err)
	}
	records[0].Coverage = nil
	if err := ValidatePrivateGraphGold(records); err == nil {
		t.Fatal("missing callers coverage accepted")
	}
}

func TestGraphFloorsRequireEveryHardTier(t *testing.T) {
	floors := GraphFloors{Recall: 0.5, MRR: 0.5, NDCG: 0.5, TierPrecision: map[string]float64{"Pattern": 0.5, "Verified": 0.5}}
	report := GraphReport{MeanRecall: 1, MeanMRR: 1, MeanNDCG: 1, PerTierPrecision: map[string]float64{"Pattern": 1, "Verified": 1, "Proven": 1}}
	if err := floors.Check(report); err == nil {
		t.Fatal("missing Proven precision floor accepted")
	}
}

func TestCalibrateGraphFloorsClampsLowBaselinesAtZero(t *testing.T) {
	baseline := GraphReport{
		MeanRecall: .05, MeanMRR: .06, MeanNDCG: .07,
		PerTierPrecision: map[string]float64{"Pattern": .00038, "Verified": .101, "Proven": .4692},
	}
	floors := CalibrateGraphFloors(baseline)
	if floors.Recall != 0 || floors.MRR != 0 || floors.NDCG != 0 || floors.TierPrecision["Pattern"] != 0 {
		t.Fatalf("negative calibrated floors were not clamped: %+v", floors)
	}
	if floors.TierPrecision["Verified"] <= 0 || floors.TierPrecision["Proven"] <= 0 {
		t.Fatalf("positive tier floors were lost: %+v", floors)
	}
	if err := floors.Check(baseline); err != nil {
		t.Fatalf("calibrated floors rejected their own baseline: %v", err)
	}
}

func TestUniqueProductionRankingRetainsFirstOccurrence(t *testing.T) {
	seen := map[string]struct{}{}
	var ranked []string
	for _, key := range []string{"a|calls|b", "a|calls|b", "a|uses_type|b"} {
		var added bool
		ranked, added = appendUniqueRanking(ranked, seen, key)
		if key == "a|calls|b" && len(ranked) == 2 && added {
			t.Fatal("duplicate production edge was retained")
		}
	}
	if len(ranked) != 2 || ranked[0] != "a|calls|b" || ranked[1] != "a|uses_type|b" {
		t.Fatalf("deduped ranking = %v", ranked)
	}
}

func TestCombineGraphMetricsDoesNotInventCrossTypeOrdering(t *testing.T) {
	item := GraphGold{NodeLabels: map[string]int{"node": 1}, EdgeLabels: map[string]int{"edge": 1}}
	nodes := GraphMetricSet{RecallAtK: 1, PrecAtK: 1, MRR: 1, NDCGAtK: 1, UDCGAtK: 1}
	edges := GraphMetricSet{RecallAtK: 0.5, PrecAtK: 0.5, MRR: 0.5, NDCGAtK: 0.5, UDCGAtK: 0.5}
	got := combineGraphMetrics(item, nodes, edges)
	if got.MRR != 0.75 || got.NDCGAtK != 0.75 || got.RecallAtK != 0.75 {
		t.Fatalf("combined independent rankings = %+v, want component mean 0.75", got)
	}
}
