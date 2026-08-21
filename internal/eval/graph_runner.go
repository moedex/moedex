package eval

// This file is the graph-quality counterpart to runner.go. It deliberately
// executes registered production MCP ToolHandlers and scores the order they
// return; the evaluator never re-sorts nodes or edges.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"moedex/internal/graph"
	"moedex/internal/mcp"
)

// GraphGold is one graph-tool judgment contract. Stable labels use
// repo/relpath#symbol for named nodes and the graph node ID only for an
// intentionally nameless node. Edge labels use source|edge-type|target.
// Positive grades are relevant; negative grades are reviewed hard distractors.
type GraphGold struct {
	Name          string          `json:"name"`
	Tool          string          `json:"tool"`
	Arguments     json.RawMessage `json:"arguments"`
	MinConfidence string          `json:"min_confidence"`
	Coverage      []string        `json:"coverage"`
	NodeLabels    map[string]int  `json:"node_labels"`
	EdgeLabels    map[string]int  `json:"edge_labels"`
	Search        GraphSearchGold `json:"search_context"`
}

var requiredPrivateGraphCoverage = []string{
	"callers", "callees", "dependencies", "consumers", "hierarchy", "queries",
	"rendering", "impact_analysis", "name_collisions", "cross_repository_distractors",
}

// ValidatePrivateGraphGold enforces the reviewed corpus tier's breadth rather
// than accepting thirty variants of one easy lookup.
func ValidatePrivateGraphGold(records []GraphGold) error {
	if len(records) < 30 {
		return fmt.Errorf("private graph gold has %d records, want at least 30 reviewed queries", len(records))
	}
	covered := make(map[string]bool)
	hasDistractor := false
	for i, record := range records {
		if strings.TrimSpace(record.Name) == "" || strings.TrimSpace(record.Tool) == "" {
			return fmt.Errorf("private graph gold record %d has no name or tool", i)
		}
		if record.MinConfidence == "" {
			return fmt.Errorf("private graph gold %q must record an explicit confidence floor", record.Name)
		}
		if _, err := graph.ParseMinConfidence(record.MinConfidence); err != nil {
			return fmt.Errorf("private graph gold %q: %w", record.Name, err)
		}
		if numRelevant(combinedLabels(record)) == 0 {
			return fmt.Errorf("private graph gold %q has no relevant node or edge label", record.Name)
		}
		if strings.TrimSpace(record.Search.Query) == "" || record.Search.TokenBudget <= 0 {
			return fmt.Errorf("private graph gold %q has no paired search_context query or positive token budget", record.Name)
		}
		for _, grade := range combinedLabels(record) {
			hasDistractor = hasDistractor || grade < 0
		}
		for _, category := range record.Coverage {
			if !graphCoverageToolMatches(category, record.Tool) {
				return fmt.Errorf("private graph gold %q tags %q coverage with incompatible tool %q", record.Name, category, record.Tool)
			}
			covered[category] = true
		}
	}
	if !hasDistractor {
		return fmt.Errorf("private graph gold has no reviewed negative hard-distractor label")
	}
	var missing []string
	for _, category := range requiredPrivateGraphCoverage {
		if !covered[category] {
			missing = append(missing, category)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("private graph gold is missing coverage: %s", strings.Join(missing, ", "))
	}
	return nil
}

func graphCoverageToolMatches(category, tool string) bool {
	switch category {
	case "callers", "callees":
		return tool == "trace_calls"
	case "dependencies":
		return tool == "graph_neighbors" || tool == "impact_analysis"
	case "consumers":
		return tool == "trace_consumers"
	case "hierarchy":
		return tool == "trace_hierarchy"
	case "queries":
		return tool == "trace_queries"
	case "rendering":
		return tool == "trace_renders"
	case "impact_analysis":
		return tool == "impact_analysis"
	case "name_collisions", "cross_repository_distractors":
		return true
	default:
		return false
	}
}

// GraphSearchGold pairs every graph judgment with the search_context budget
// case that an agent would use to retrieve the underlying source.
type GraphSearchGold struct {
	Query       string `json:"query"`
	TokenBudget int    `json:"token_budget"`
	TopK        int    `json:"top_k"`
}

type GraphMetricSet struct {
	RecallAtK float64
	PrecAtK   float64
	MRR       float64
	NDCGAtK   float64
	UDCGAtK   float64
}

type GraphQueryReport struct {
	Name                string
	Tool                string
	RankedNodes         []string
	RankedEdges         []string
	Nodes               GraphMetricSet
	Edges               GraphMetricSet
	Combined            GraphMetricSet
	SearchTokenEstimate int
	SearchTokenBudget   int
	ToolDuration        time.Duration
}

type GraphReport struct {
	K                int
	Queries          []GraphQueryReport
	MeanRecall       float64
	MeanPrec         float64
	MeanMRR          float64
	MeanNDCG         float64
	MeanUDCG         float64
	PerTierPrecision map[string]float64
	MeanToolDuration time.Duration
}

// GraphRunner executes production graph ToolHandlers and an optional production
// ContextSearcher. Searcher is required when a gold record has a paired search.
type GraphRunner struct {
	tools    map[string]mcp.ToolHandler
	searcher mcp.ContextSearcher
}

func NewGraphRunner(tools []mcp.ToolHandler, searcher mcp.ContextSearcher) *GraphRunner {
	byName := make(map[string]mcp.ToolHandler, len(tools))
	for _, tool := range tools {
		if tool != nil {
			byName[tool.Name()] = tool
		}
	}
	return &GraphRunner{tools: byName, searcher: searcher}
}

// Run executes every gold record and aggregates the emitted rankings. k <= 0
// scores each complete production ranking, matching the base metric convention.
func (r *GraphRunner) Run(ctx context.Context, gold []GraphGold, k int) (GraphReport, error) {
	report := GraphReport{K: k, PerTierPrecision: make(map[string]float64)}
	tierHits := make(map[string]int)
	tierReturned := make(map[string]int)
	answerable := 0
	for _, item := range gold {
		qr, tiers, err := r.runOne(ctx, item, k)
		if err != nil {
			return GraphReport{}, fmt.Errorf("graph eval %q: %w", item.Name, err)
		}
		report.Queries = append(report.Queries, qr)
		report.MeanToolDuration += qr.ToolDuration
		if numRelevant(combinedLabels(item)) > 0 {
			answerable++
			report.MeanRecall += qr.Combined.RecallAtK
			report.MeanPrec += qr.Combined.PrecAtK
			report.MeanMRR += qr.Combined.MRR
			report.MeanNDCG += qr.Combined.NDCGAtK
			report.MeanUDCG += qr.Combined.UDCGAtK
		}
		for tier, counts := range tiers {
			tierHits[tier] += counts[0]
			tierReturned[tier] += counts[1]
		}
	}
	if len(report.Queries) > 0 {
		report.MeanToolDuration /= time.Duration(len(report.Queries))
	}
	if answerable > 0 {
		n := float64(answerable)
		report.MeanRecall /= n
		report.MeanPrec /= n
		report.MeanMRR /= n
		report.MeanNDCG /= n
		report.MeanUDCG /= n
	}
	for _, tier := range []string{"Candidate", "Pattern", "Verified", "Proven"} {
		if tierReturned[tier] > 0 {
			report.PerTierPrecision[tier] = float64(tierHits[tier]) / float64(tierReturned[tier])
		} else {
			report.PerTierPrecision[tier] = 0
		}
	}
	return report, nil
}

func (r *GraphRunner) runOne(ctx context.Context, item GraphGold, k int) (GraphQueryReport, map[string][2]int, error) {
	if strings.TrimSpace(item.Search.Query) == "" || item.Search.TokenBudget <= 0 {
		return GraphQueryReport{}, nil, fmt.Errorf("every graph gold record requires a paired search_context query and positive token_budget")
	}
	tool := r.tools[item.Tool]
	if tool == nil {
		return GraphQueryReport{}, nil, fmt.Errorf("production handler %q is not registered", item.Tool)
	}
	floor, err := graph.ParseMinConfidence(item.MinConfidence)
	if err != nil {
		return GraphQueryReport{}, nil, err
	}
	args, err := graphEvalArguments(item.Arguments, floor.String())
	if err != nil {
		return GraphQueryReport{}, nil, err
	}
	started := time.Now()
	result, err := tool.Call(ctx, args)
	toolDuration := time.Since(started)
	if err != nil {
		return GraphQueryReport{}, nil, err
	}
	if isError, _ := result["isError"].(bool); isError {
		return GraphQueryReport{}, nil, fmt.Errorf("tool returned an error: %v", result["content"])
	}
	var payload graphEvalResult
	raw, err := json.Marshal(result["structuredContent"])
	if err != nil {
		return GraphQueryReport{}, nil, fmt.Errorf("marshal structured graph result: %w", err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return GraphQueryReport{}, nil, fmt.Errorf("decode structured graph result: %w", err)
	}

	stableNodeKeys, err := stableGraphNodeKeys(payload.Nodes)
	if err != nil {
		return GraphQueryReport{}, nil, err
	}
	nodeKeys := make(map[string]string, len(payload.Nodes))
	rankedNodes := make([]string, 0, len(payload.Nodes))
	nodeRankingSeen := make(map[string]struct{}, len(payload.Nodes))
	for i, node := range payload.Nodes {
		key := stableNodeKeys[i]
		nodeKeys[node.ID] = key
		rankedNodes, _ = appendUniqueRanking(rankedNodes, nodeRankingSeen, key)
	}
	rankedEdges := make([]string, 0, len(payload.Edges))
	edgeRankingSeen := make(map[string]struct{}, len(payload.Edges))
	tiers := make(map[string][2]int)
	for _, edge := range payload.Edges {
		source, sourceOK := nodeKeys[edge.Source]
		target, targetOK := nodeKeys[edge.Target]
		if !sourceOK || !targetOK {
			return GraphQueryReport{}, nil, fmt.Errorf("edge %s -> %s references a node absent from production ordering", edge.Source, edge.Target)
		}
		key := source + "|" + edge.Type + "|" + target
		var added bool
		rankedEdges, added = appendUniqueRanking(rankedEdges, edgeRankingSeen, key)
		if !added {
			continue
		}
		counts := tiers[edge.Confidence.Tier]
		if item.EdgeLabels[key] >= 1 {
			counts[0]++
		}
		counts[1]++
		tiers[edge.Confidence.Tier] = counts
	}

	nodeMetrics := metricSet(rankedNodes, item.NodeLabels, k)
	edgeMetrics := metricSet(rankedEdges, item.EdgeLabels, k)
	qr := GraphQueryReport{
		Name: item.Name, Tool: item.Tool,
		RankedNodes: rankedNodes, RankedEdges: rankedEdges,
		Nodes: nodeMetrics, Edges: edgeMetrics,
		Combined: combineGraphMetrics(item, nodeMetrics, edgeMetrics), ToolDuration: toolDuration,
	}
	if item.Search.Query != "" {
		if r.searcher == nil {
			return GraphQueryReport{}, nil, fmt.Errorf("paired search_context case has no production searcher")
		}
		win, err := r.searcher.SearchContext(ctx, item.Search.Query, item.Search.TokenBudget, item.Search.TopK)
		if err != nil {
			return GraphQueryReport{}, nil, fmt.Errorf("paired search_context: %w", err)
		}
		qr.SearchTokenEstimate = win.TokenEstimate
		qr.SearchTokenBudget = item.Search.TokenBudget
		if item.Search.TokenBudget > 0 && win.TokenEstimate > item.Search.TokenBudget {
			return GraphQueryReport{}, nil, fmt.Errorf("paired search_context token estimate %d exceeds budget %d", win.TokenEstimate, item.Search.TokenBudget)
		}
	}
	return qr, tiers, nil
}

// combineGraphMetrics averages the independently ordered node and edge result
// sets that have positive judgments. It never invents a cross-type ordering.
func combineGraphMetrics(item GraphGold, nodes, edges GraphMetricSet) GraphMetricSet {
	var out GraphMetricSet
	n := 0.0
	if numRelevant(item.NodeLabels) > 0 {
		addGraphMetrics(&out, nodes)
		n++
	}
	if numRelevant(item.EdgeLabels) > 0 {
		addGraphMetrics(&out, edges)
		n++
	}
	if n > 0 {
		out.RecallAtK /= n
		out.PrecAtK /= n
		out.MRR /= n
		out.NDCGAtK /= n
		out.UDCGAtK /= n
	}
	return out
}

func addGraphMetrics(dst *GraphMetricSet, src GraphMetricSet) {
	dst.RecallAtK += src.RecallAtK
	dst.PrecAtK += src.PrecAtK
	dst.MRR += src.MRR
	dst.NDCGAtK += src.NDCGAtK
	dst.UDCGAtK += src.UDCGAtK
}

func graphEvalArguments(raw json.RawMessage, floor string) (json.RawMessage, error) {
	args := make(map[string]any)
	if len(strings.TrimSpace(string(raw))) != 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("decode arguments: %w", err)
		}
	}
	if args == nil {
		return nil, fmt.Errorf("arguments must be a JSON object")
	}
	args["min_confidence"] = floor
	out, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode arguments: %w", err)
	}
	return out, nil
}

func metricSet(ranked []string, labels map[string]int, k int) GraphMetricSet {
	return GraphMetricSet{
		RecallAtK: RecallAtK(ranked, labels, k), PrecAtK: PrecisionAtK(ranked, labels, k),
		MRR: MRR(ranked, labels), NDCGAtK: NDCGAtK(ranked, labels, k), UDCGAtK: UDCGAtK(ranked, labels, k),
	}
}

func combinedLabels(item GraphGold) map[string]int {
	out := make(map[string]int, len(item.NodeLabels)+len(item.EdgeLabels))
	for key, grade := range item.NodeLabels {
		out["node:"+key] = grade
	}
	for key, grade := range item.EdgeLabels {
		out["edge:"+key] = grade
	}
	return out
}

type graphEvalResult struct {
	Nodes []graphEvalNode `json:"nodes"`
	Edges []struct {
		Source     string `json:"source"`
		Target     string `json:"target"`
		Type       string `json:"type"`
		Confidence struct {
			Tier string `json:"tier"`
		} `json:"confidence"`
	} `json:"edges"`
}

type graphEvalNode struct {
	ID        string `json:"id"`
	Symbol    string `json:"symbol"`
	Kind      string `json:"kind"`
	Locations []struct {
		Repo string `json:"repo"`
		Path string `json:"path"`
		Line int    `json:"line"`
	} `json:"locations"`
}

func appendUniqueRanking(ranked []string, seen map[string]struct{}, key string) ([]string, bool) {
	if _, duplicate := seen[key]; duplicate {
		return ranked, false
	}
	seen[key] = struct{}{}
	return append(ranked, key), true
}

// stableGraphNodeKeys preserves the human-authored repo/path#symbol key for
// unique nodes while making collisions injective. A Type keeps the legacy base
// key when a file also defines a same-named constructor/method; the other nodes
// receive deterministic kind/line/ID suffixes. Existing gold remains readable,
// and distinct graph nodes can no longer silently share one judgment.
func stableGraphNodeKeys(nodes []graphEvalNode) ([]string, error) {
	bases := make([]string, len(nodes))
	groups := make(map[string][]int, len(nodes))
	for i, node := range nodes {
		base, err := stableGraphNodeKey(node)
		if err != nil {
			return nil, err
		}
		bases[i] = base
		groups[base] = append(groups[base], i)
	}
	out := append([]string(nil), bases...)
	for base, indexes := range groups {
		if len(indexes) < 2 {
			continue
		}
		canonical := indexes[0]
		for _, idx := range indexes[1:] {
			if nodeCollisionOrder(nodes[idx]) < nodeCollisionOrder(nodes[canonical]) {
				canonical = idx
			}
		}
		kindCounts := make(map[string]int)
		for _, idx := range indexes {
			if idx != canonical {
				kindCounts[nodes[idx].Kind]++
			}
		}
		used := map[string]struct{}{base: {}}
		for _, idx := range indexes {
			if idx == canonical {
				continue
			}
			node := nodes[idx]
			suffix := node.Kind
			if suffix == "" {
				suffix = "node"
			}
			if kindCounts[node.Kind] > 1 && len(node.Locations) > 0 {
				suffix += ":" + strconv.Itoa(node.Locations[0].Line)
			}
			key := base + "@" + suffix
			if _, collision := used[key]; collision {
				key += ":" + node.ID
			}
			used[key] = struct{}{}
			out[idx] = key
		}
	}
	return out, nil
}

func nodeCollisionOrder(node graphEvalNode) string {
	priority := "1"
	if node.Kind == "Type" || node.Kind == "Table" {
		priority = "0"
	}
	line := 0
	if len(node.Locations) > 0 {
		line = node.Locations[0].Line
	}
	return priority + "\x00" + node.Kind + "\x00" + fmt.Sprintf("%012d", line) + "\x00" + node.ID
}

func stableGraphNodeKey(node graphEvalNode) (string, error) {
	if node.Symbol == "" {
		if node.ID == "" {
			return "", fmt.Errorf("intentionally nameless node has no node ID fallback")
		}
		switch node.Kind {
		case "File", "Route", "Occurrence":
		default:
			return "", fmt.Errorf("nameless node %q has unsupported kind %q; node-ID fallback is reserved for intentional raw nodes", node.ID, node.Kind)
		}
		return node.ID, nil
	}
	if len(node.Locations) == 0 || node.Locations[0].Path == "" {
		return "", fmt.Errorf("named node %q has no stable repo/path location", node.Symbol)
	}
	location := node.Locations[0]
	path := filepath.ToSlash(filepath.Join(location.Repo, location.Path))
	return strings.TrimPrefix(path, "./") + "#" + node.Symbol, nil
}

// GraphFloors contains the hard portions of a reviewed graph baseline.
// Candidate precision and UDCG intentionally remain watches.
type GraphFloors struct {
	Recall        float64            `json:"recall"`
	MRR           float64            `json:"mrr"`
	NDCG          float64            `json:"ndcg"`
	TierPrecision map[string]float64 `json:"tier_precision"`
}

type GraphGoldFile struct {
	Records []GraphGold  `json:"records"`
	Floors  *GraphFloors `json:"floors,omitempty"`
}

func LoadGraphGoldFile(path string) (GraphGoldFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return GraphGoldFile{}, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var gold GraphGoldFile
	if err := dec.Decode(&gold); err != nil {
		return GraphGoldFile{}, err
	}
	if len(gold.Records) == 0 {
		return GraphGoldFile{}, fmt.Errorf("graph gold has no records")
	}
	return gold, nil
}

// CalibrateGraphFloors applies the fixed mechanical rule to a reviewed baseline.
func CalibrateGraphFloors(baseline GraphReport) GraphFloors {
	return GraphFloors{
		Recall: math.Max(0, baseline.MeanRecall-0.07),
		MRR:    math.Max(0, baseline.MeanMRR-0.07),
		NDCG:   math.Max(0, baseline.MeanNDCG-0.07),
		TierPrecision: map[string]float64{
			"Pattern":  math.Max(0, baseline.PerTierPrecision["Pattern"]-0.10),
			"Verified": math.Max(0, baseline.PerTierPrecision["Verified"]-0.10),
			"Proven":   math.Max(0, baseline.PerTierPrecision["Proven"]-0.10),
		},
	}
}

func (f GraphFloors) Check(report GraphReport) error {
	for name, floor := range map[string]float64{"recall": f.Recall, "MRR": f.MRR, "NDCG": f.NDCG} {
		if math.IsNaN(floor) || math.IsInf(floor, 0) || floor < 0 || floor > 1 {
			return fmt.Errorf("invalid %s graph floor %.4f", name, floor)
		}
	}
	if report.MeanRecall < f.Recall || report.MeanMRR < f.MRR || report.MeanNDCG < f.NDCG {
		return fmt.Errorf("aggregate graph metrics below floors: recall %.4f/%.4f, MRR %.4f/%.4f, NDCG %.4f/%.4f",
			report.MeanRecall, f.Recall, report.MeanMRR, f.MRR, report.MeanNDCG, f.NDCG)
	}
	for _, tier := range []string{"Pattern", "Verified", "Proven"} {
		floor, ok := f.TierPrecision[tier]
		if !ok || math.IsNaN(floor) || math.IsInf(floor, 0) || floor < 0 || floor > 1 {
			return fmt.Errorf("invalid or missing %s precision floor", tier)
		}
		if report.PerTierPrecision[tier] < floor {
			return fmt.Errorf("%s precision %.4f below floor %.4f", tier, report.PerTierPrecision[tier], floor)
		}
	}
	return nil
}

// HermeticGraphFloors are mechanically derived from the reviewed
// testdata/graph-corpus baseline after production-key deduplication (1.0 for
// Recall, MRR, NDCG, and Pattern/Verified/Proven precision).
func HermeticGraphFloors() GraphFloors {
	return GraphFloors{
		Recall: 0.93, MRR: 0.93, NDCG: 0.93,
		TierPrecision: map[string]float64{"Pattern": 0.90, "Verified": 0.90, "Proven": 0.90},
	}
}
