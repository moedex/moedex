package server

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/ingest"
	"moedex/internal/navigate"
)

// PatternCallAuditOptions bounds the detailed sample returned by
// AuditPatternCalls. Aggregate counts always cover the complete current shard
// set; the seed only changes which stable call-site identities appear in Sample.
type PatternCallAuditOptions struct {
	SampleLimit              int
	RouteLimit               int
	Seed                     uint64
	ResolveSampleLimit       int
	ResolveWallTimeout       time.Duration
	ResolveRequestTimeout    time.Duration
	ResolveConcurrency       int
	ResolveRequestsPerSecond float64
}

// PatternCallTargetDistribution describes same-name fan-out per exact call
// site after repository filtering and before LSP reconciliation.
type PatternCallTargetDistribution struct {
	P50       int            `json:"p50"`
	P95       int            `json:"p95"`
	Max       int            `json:"max"`
	Histogram map[string]int `json:"histogram"`
}

// PatternCallContextCounts accounts for every repository/file context in
// which a grouped source blob is present. Current means the indexed bytes still
// match a supported worktree file and workspace privacy permits LSP access.
type PatternCallContextCounts struct {
	Current     int `json:"current"`
	Unsupported int `json:"unsupported"`
	Unavailable int `json:"unavailable"`
	Restricted  int `json:"restricted"`
}

// PatternCallRouteAudit breaks the census down by language-server route.
type PatternCallRouteAudit struct {
	Language                    string                   `json:"language"`
	Workspace                   string                   `json:"workspace"`
	Sites                       int                      `json:"sites"`
	Contexts                    PatternCallContextCounts `json:"contexts"`
	ProjectedReferenceRequests  int                      `json:"projected_reference_requests"`
	ProjectedDefinitionRequests int                      `json:"projected_definition_requests"`
}

// PatternCallAuditSample is a deterministic bounded view of a call group.
type PatternCallAuditSample struct {
	SourceBlob         string `json:"source_blob"`
	EvidenceOffset     uint64 `json:"evidence_offset"`
	Name               string `json:"name"`
	Targets            int    `json:"targets"`
	RepositoryContexts int    `json:"repository_contexts"`
}

// PatternCallAuditReport is a non-mutating census of the current Pattern call
// topology and the request counts of the two exact-resolution directions.
type PatternCallAuditReport struct {
	PatternCallEdges            int                           `json:"pattern_call_edges"`
	DistinctCallSites           int                           `json:"distinct_call_sites"`
	DistinctDefinitions         int                           `json:"distinct_definitions"`
	MultiRepositorySites        int                           `json:"multi_repository_sites"`
	SitesWithExactLSPCoverage   int                           `json:"sites_with_exact_lsp_coverage"`
	ProjectedReferenceRequests  int                           `json:"projected_reference_requests"`
	ProjectedDefinitionRequests int                           `json:"projected_definition_requests"`
	TargetsPerSite              PatternCallTargetDistribution `json:"targets_per_site"`
	Contexts                    PatternCallContextCounts      `json:"contexts"`
	Routes                      []PatternCallRouteAudit       `json:"routes,omitempty"`
	RoutesTotal                 int                           `json:"routes_total"`
	RoutesTruncated             bool                          `json:"routes_truncated"`
	Sample                      []PatternCallAuditSample      `json:"sample,omitempty"`
	ResolverSample              *PatternCallResolverSample    `json:"resolver_sample,omitempty"`
}

// PatternCallResolverSample records a bounded, non-persisting LSP sample. It
// uses the production reconciliation path but never emits or saves graph edges.
type PatternCallResolverSample struct {
	Sites                  int     `json:"sites"`
	ReferenceRequests      int     `json:"reference_requests"`
	ResolvedResponses      int     `json:"resolved_responses"`
	EmptyResponses         int     `json:"ready_empty_responses"`
	UnsupportedResponses   int     `json:"unsupported_responses"`
	UnavailableResponses   int     `json:"unavailable_responses"`
	SkippedRequests        int     `json:"skipped_requests"`
	ReconciledSites        int     `json:"reconciled_sites"`
	RetainedUncertainSites int     `json:"retained_uncertain_sites"`
	ProvenCalls            int     `json:"proven_calls"`
	MeanRequestMillis      float64 `json:"mean_request_ms"`
	MaxRequestMillis       float64 `json:"max_request_ms"`
	WallMillis             int64   `json:"wall_ms"`
}

type patternCallSiteKey struct {
	sourceBlob string
	evidence   uint64
	name       string
}

func lspRouteKey(language, workspace string) string { return language + "\x00" + workspace }

type patternCallGroup struct {
	key       patternCallSiteKey
	source    diskgraph.Key
	edges     []graphKeyEdge
	edgeCount int
	targets   []diskgraph.Key
}

func patternCallGroups(results []graphNameResult) []patternCallGroup {
	byKey := make(map[patternCallSiteKey]*patternCallGroup)
	for i := range results {
		for _, candidate := range results[i].Edges {
			edge := candidate.Edge
			if edge.Type != diskgraph.EdgeCalls || edge.Confidence != graph.Pattern || edge.Name == "" {
				continue
			}
			key := patternCallSiteKey{sourceBlob: edge.Evidence.BlobSHA, evidence: edge.Evidence.ByteOffset, name: edge.Name}
			group := byKey[key]
			if group == nil {
				group = &patternCallGroup{key: key, source: candidate.Key}
				byKey[key] = group
			}
			group.edges = append(group.edges, candidate)
		}
	}
	out := make([]patternCallGroup, 0, len(byKey))
	for _, group := range byKey {
		targetSet := make(map[diskgraph.Key]struct{}, len(group.edges))
		for _, candidate := range group.edges {
			targetSet[diskgraph.Key{BlobSHA: candidate.Edge.TargetBlob, SymbolOffset: candidate.Edge.TargetOffset}] = struct{}{}
		}
		group.targets = make([]diskgraph.Key, 0, len(targetSet))
		for target := range targetSet {
			group.targets = append(group.targets, target)
		}
		sort.Slice(group.targets, func(i, j int) bool { return graphKeyLess(group.targets[i], group.targets[j]) })
		sort.Slice(group.edges, func(i, j int) bool { return graphKeyEdgeLess(group.edges[i], group.edges[j]) })
		group.edgeCount = len(group.edges)
		out = append(out, *group)
	}
	sort.Slice(out, func(i, j int) bool { return patternCallSiteLess(out[i].key, out[j].key) })
	return out
}

func (g patternCallGroup) numEdges() int {
	if g.edgeCount > 0 {
		return g.edgeCount
	}
	return len(g.edges)
}

// persistedPatternCallGroups scans the graph that agents actually query. The
// audit used to regenerate every candidate edge and then discard every non-call
// result, which made a read-only census cost as much as a complete graph build
// and inherit its pathological long tail. The persisted graph already contains
// the exact same-repository, confidence-filtered population the audit measures.
func persistedPatternCallGroups(ctx context.Context, graphFile *diskgraph.Graph) ([]patternCallGroup, map[patternCallSiteKey]struct{}, error) {
	byKey := make(map[patternCallSiteKey]*patternCallGroup)
	covered := make(map[patternCallSiteKey]struct{})
	var scanErr error
	graphFile.EachEdge(func(source diskgraph.Key, edge diskgraph.Edge) bool {
		if err := ctx.Err(); err != nil {
			scanErr = err
			return false
		}
		if edge.Type != diskgraph.EdgeCalls || edge.Name == "" {
			return true
		}
		key := patternCallSiteKey{sourceBlob: edge.Evidence.BlobSHA, evidence: edge.Evidence.ByteOffset, name: edge.Name}
		if edge.Confidence == graph.Proven {
			covered[key] = struct{}{}
			return true
		}
		if edge.Confidence != graph.Pattern {
			return true
		}
		group := byKey[key]
		if group == nil {
			group = &patternCallGroup{key: key, source: source}
			byKey[key] = group
		}
		group.edgeCount++
		group.targets = append(group.targets, diskgraph.Key{BlobSHA: edge.TargetBlob, SymbolOffset: edge.TargetOffset})
		return true
	})
	if scanErr != nil {
		return nil, nil, scanErr
	}

	out := make([]patternCallGroup, 0, len(byKey))
	for _, group := range byKey {
		sort.Slice(group.targets, func(i, j int) bool { return graphKeyLess(group.targets[i], group.targets[j]) })
		write := 0
		for _, target := range group.targets {
			if write > 0 && group.targets[write-1] == target {
				continue
			}
			group.targets[write] = target
			write++
		}
		group.targets = group.targets[:write]
		out = append(out, *group)
	}
	sort.Slice(out, func(i, j int) bool { return patternCallSiteLess(out[i].key, out[j].key) })
	return out, covered, nil
}

func patternCallSiteLess(a, b patternCallSiteKey) bool {
	if a.sourceBlob != b.sourceBlob {
		return a.sourceBlob < b.sourceBlob
	}
	if a.evidence != b.evidence {
		return a.evidence < b.evidence
	}
	return a.name < b.name
}

func graphKeyLess(a, b diskgraph.Key) bool {
	if a.BlobSHA != b.BlobSHA {
		return a.BlobSHA < b.BlobSHA
	}
	return a.SymbolOffset < b.SymbolOffset
}

func graphKeyEdgeLess(a, b graphKeyEdge) bool {
	if a.Key != b.Key {
		return graphKeyLess(a.Key, b.Key)
	}
	at := diskgraph.Key{BlobSHA: a.Edge.TargetBlob, SymbolOffset: a.Edge.TargetOffset}
	bt := diskgraph.Key{BlobSHA: b.Edge.TargetBlob, SymbolOffset: b.Edge.TargetOffset}
	return graphKeyLess(at, bt)
}

// AuditPatternCalls computes a complete census without writing a graph or any
// sidecar. LSP remains off unless ResolveSampleLimit is positive; that optional
// sample uses production resolution but never emits or persists its results.
func AuditPatternCalls(ctx context.Context, dir string, opts PatternCallAuditOptions) (report PatternCallAuditReport, err error) {
	if opts.SampleLimit < 0 {
		return report, fmt.Errorf("server: graph call audit sample limit must be non-negative")
	}
	if opts.ResolveSampleLimit < 0 {
		return report, fmt.Errorf("server: graph call audit resolver sample limit must be non-negative")
	}
	if opts.RouteLimit < 0 {
		return report, fmt.Errorf("server: graph call audit route limit must be non-negative")
	}
	sweep, err := openGraphSweep(dir)
	if err != nil {
		return report, err
	}
	defer func() {
		if closeErr := sweep.Close(); err == nil {
			err = closeErr
		}
	}()
	if err := ctx.Err(); err != nil {
		return report, err
	}
	graphFile, err := diskgraph.Open(GraphPath(dir))
	if err != nil {
		return report, err
	}
	defer func() {
		if closeErr := graphFile.Close(); err == nil {
			err = closeErr
		}
	}()
	if !sweep.matchesCorpusRoster(graphFile) {
		return report, fmt.Errorf("server: graph call audit requires a graph built from the current shard corpus")
	}
	groups, covered, err := persistedPatternCallGroups(ctx, graphFile)
	if err != nil {
		return report, err
	}
	report = sweep.patternCallAudit(groups, covered, opts)
	if opts.ResolveSampleLimit == 0 {
		return report, nil
	}
	if !lspGraphAvailable() {
		return report, fmt.Errorf("server: graph call resolver sample requires an lsp-tagged build")
	}

	sampled := stablePatternCallGroups(groups, opts.ResolveSampleLimit, opts.Seed)
	started := time.Now()
	resolveCtx := ctx
	if opts.ResolveWallTimeout > 0 {
		var cancel context.CancelFunc
		resolveCtx, cancel = context.WithTimeout(ctx, opts.ResolveWallTimeout)
		defer cancel()
	}
	reconciliation, err := collectLSPPatternReconciliation(resolveCtx, sweep.merged, sweep.idxs, sampled, nil, GraphBuildOptions{
		LSPRequestTimeout:    opts.ResolveRequestTimeout,
		LSPConcurrency:       opts.ResolveConcurrency,
		LSPRequestsPerSecond: opts.ResolveRequestsPerSecond,
		lspPatternGroupsOnly: true,
	})
	if err != nil {
		return report, err
	}
	stats := reconciliation.stats
	report.ResolverSample = &PatternCallResolverSample{
		Sites: len(sampled), ReferenceRequests: stats.ReferenceRequests,
		ResolvedResponses: stats.ResolvedReferenceRequests, EmptyResponses: stats.EmptyReferenceRequests,
		UnsupportedResponses: stats.UnsupportedReferenceRequests, UnavailableResponses: stats.UnavailableReferenceRequests,
		SkippedRequests: stats.SkippedReferenceRequests,
		ReconciledSites: stats.ReconciledCallGroups, RetainedUncertainSites: stats.RetainedUncertainCallGroups,
		ProvenCalls: len(reconciliation.calls), WallMillis: time.Since(started).Milliseconds(),
		MaxRequestMillis: float64(stats.ReferenceLatencyMax) / float64(time.Millisecond),
	}
	if stats.ReferenceRequests > 0 {
		report.ResolverSample.MeanRequestMillis = float64(stats.ReferenceLatencyTotal) / float64(time.Millisecond) / float64(stats.ReferenceRequests)
	}
	return report, nil
}

func (s *graphSweep) matchesCorpusRoster(graphFile *diskgraph.Graph) bool {
	if graphFile == nil || graphFile.NumCorpusEntries() != len(s.identity) {
		return false
	}
	roster := graphFile.CorpusEntrySet()
	for _, token := range s.identity {
		if _, ok := roster[token]; !ok {
			return false
		}
	}
	return true
}

func (s *graphSweep) patternCallAudit(groups []patternCallGroup, covered map[patternCallSiteKey]struct{}, opts PatternCallAuditOptions) PatternCallAuditReport {
	report := PatternCallAuditReport{DistinctCallSites: len(groups)}
	report.TargetsPerSite.Histogram = map[string]int{"1": 0, "2-4": 0, "5-16": 0, "17-64": 0, "65+": 0}
	targets := make(map[diskgraph.Key]struct{})
	routes := make(map[string]*PatternCallRouteAudit)
	referencesByRoute := make(map[string]map[diskgraph.Key]struct{})
	contextsBySHA := make(map[string][]callGroupContext)
	targetCurrent := make(map[string]bool)
	counts := make([]int, 0, len(groups))
	for _, group := range groups {
		report.PatternCallEdges += group.numEdges()
		counts = append(counts, len(group.targets))
		for _, target := range group.targets {
			targets[target] = struct{}{}
		}
		switch n := len(group.targets); {
		case n <= 1:
			report.TargetsPerSite.Histogram["1"]++
		case n <= 4:
			report.TargetsPerSite.Histogram["2-4"]++
		case n <= 16:
			report.TargetsPerSite.Histogram["5-16"]++
		case n <= 64:
			report.TargetsPerSite.Histogram["17-64"]++
		default:
			report.TargetsPerSite.Histogram["65+"]++
		}
		contexts, cached := contextsBySHA[group.key.sourceBlob]
		if !cached {
			contexts = s.callGroupContexts(group)
			contextsBySHA[group.key.sourceBlob] = contexts
		}
		if len(s.reposBySHA[group.key.sourceBlob]) > 1 {
			report.MultiRepositorySites++
		}
		for _, context := range contexts {
			route := routes[context.route]
			if route == nil {
				route = &PatternCallRouteAudit{Language: context.language, Workspace: context.workspace}
				routes[context.route] = route
			}
			route.Sites++
			switch context.status {
			case callContextCurrent:
				report.Contexts.Current++
				report.ProjectedDefinitionRequests++
				route.Contexts.Current++
				route.ProjectedDefinitionRequests++
				set := referencesByRoute[context.route]
				if set == nil {
					set = make(map[diskgraph.Key]struct{})
					referencesByRoute[context.route] = set
				}
				for _, target := range group.targets {
					cacheKey := target.BlobSHA + "\x00" + context.repo
					current, checked := targetCurrent[cacheKey]
					if !checked {
						current = s.targetCurrentInRepository(target, context.repo)
						targetCurrent[cacheKey] = current
					}
					if current {
						set[target] = struct{}{}
					}
				}
			case callContextUnsupported:
				report.Contexts.Unsupported++
				route.Contexts.Unsupported++
			case callContextRestricted:
				report.Contexts.Restricted++
				route.Contexts.Restricted++
			default:
				report.Contexts.Unavailable++
				route.Contexts.Unavailable++
			}
		}
	}
	report.DistinctDefinitions = len(targets)
	sort.Ints(counts)
	if len(counts) > 0 {
		report.TargetsPerSite.P50 = percentileInt(counts, 50)
		report.TargetsPerSite.P95 = percentileInt(counts, 95)
		report.TargetsPerSite.Max = counts[len(counts)-1]
	}
	for _, group := range groups {
		if _, ok := covered[group.key]; ok {
			report.SitesWithExactLSPCoverage++
		}
	}
	for key, route := range routes {
		route.ProjectedReferenceRequests = len(referencesByRoute[key])
		report.ProjectedReferenceRequests += route.ProjectedReferenceRequests
		report.Routes = append(report.Routes, *route)
	}
	sort.Slice(report.Routes, func(i, j int) bool {
		if report.Routes[i].Language != report.Routes[j].Language {
			return report.Routes[i].Language < report.Routes[j].Language
		}
		return report.Routes[i].Workspace < report.Routes[j].Workspace
	})
	report.RoutesTotal = len(report.Routes)
	if opts.RouteLimit > 0 && len(report.Routes) > opts.RouteLimit {
		report.Routes = report.Routes[:opts.RouteLimit]
		report.RoutesTruncated = true
	}
	report.Sample = stableCallAuditSample(groups, s, contextsBySHA, opts.SampleLimit, opts.Seed)
	return report
}

func percentileInt(sorted []int, pct int) int {
	if len(sorted) == 0 {
		return 0
	}
	index := (pct*len(sorted) + 99) / 100
	if index < 1 {
		index = 1
	}
	return sorted[index-1]
}

type callContextStatus uint8

const (
	callContextUnavailable callContextStatus = iota
	callContextCurrent
	callContextUnsupported
	callContextRestricted
)

type callGroupContext struct {
	id        string
	repo      string
	language  string
	workspace string
	route     string
	status    callContextStatus
}

func (s *graphSweep) callGroupContexts(group patternCallGroup) []callGroupContext {
	byID := make(map[string]callGroupContext)
	for _, site := range s.sites[group.key.sourceBlob] {
		blob := s.idxs[site.shard].Blob(site.blob)
		if blob == nil {
			continue
		}
		for _, ref := range blob.Files {
			id := ref.Repo + "\x00" + filepath.Clean(ref.AbsPath)
			if _, exists := byID[id]; exists {
				continue
			}
			language, workspace := navigate.WorkspaceRoute(ref.AbsPath)
			context := callGroupContext{
				id: id, repo: ref.Repo, language: language, workspace: workspace,
				route: lspRouteKey(language, workspace), status: callContextUnavailable,
			}
			if _, supported := navigate.LanguageForPath(ref.AbsPath); !supported {
				context.status = callContextUnsupported
				byID[id] = context
				continue
			}
			root, ok := indexedRepoRoot(ref.AbsPath, ref.RelPath)
			if !ok || !currentFileMatches(ref.AbsPath, blob.Content) {
				byID[id] = context
				continue
			}
			allowed, err := ingest.LSPWorkspaceAllowed(root)
			if err != nil {
				byID[id] = context
				continue
			}
			if !allowed {
				context.status = callContextRestricted
			} else {
				context.status = callContextCurrent
			}
			byID[id] = context
		}
	}
	out := make([]callGroupContext, 0, len(byID))
	for _, context := range byID {
		out = append(out, context)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func (s *graphSweep) targetCurrentInRepository(target diskgraph.Key, repo string) bool {
	for _, site := range s.sites[target.BlobSHA] {
		blob := s.idxs[site.shard].Blob(site.blob)
		if blob == nil {
			continue
		}
		for _, ref := range blob.Files {
			if ref.Repo != repo {
				continue
			}
			if _, supported := navigate.LanguageForPath(ref.AbsPath); supported && currentFileMatches(ref.AbsPath, blob.Content) {
				return true
			}
		}
	}
	return false
}

func stableCallAuditSample(groups []patternCallGroup, s *graphSweep, contextsBySHA map[string][]callGroupContext, limit int, seed uint64) []PatternCallAuditSample {
	selected := stablePatternCallGroups(groups, limit, seed)
	if len(selected) == 0 {
		return nil
	}
	out := make([]PatternCallAuditSample, 0, len(selected))
	for _, group := range selected {
		contexts, ok := contextsBySHA[group.key.sourceBlob]
		if !ok {
			contexts = s.callGroupContexts(group)
			contextsBySHA[group.key.sourceBlob] = contexts
		}
		out = append(out, PatternCallAuditSample{
			SourceBlob: group.key.sourceBlob, EvidenceOffset: group.key.evidence,
			Name: group.key.name, Targets: len(group.targets),
			RepositoryContexts: len(contexts),
		})
	}
	return out
}

func stablePatternCallGroups(groups []patternCallGroup, limit int, seed uint64) []patternCallGroup {
	if limit <= 0 || len(groups) == 0 {
		return nil
	}
	type ranked struct {
		hash  uint64
		group patternCallGroup
	}
	rankedGroups := make([]ranked, 0, len(groups))
	for _, group := range groups {
		h := fnv.New64a()
		_, _ = h.Write([]byte(group.key.sourceBlob))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(fmt.Sprintf("%d\x00%s\x00%d", group.key.evidence, group.key.name, seed)))
		rankedGroups = append(rankedGroups, ranked{hash: h.Sum64(), group: group})
	}
	sort.Slice(rankedGroups, func(i, j int) bool {
		if rankedGroups[i].hash != rankedGroups[j].hash {
			return rankedGroups[i].hash < rankedGroups[j].hash
		}
		return patternCallSiteLess(rankedGroups[i].group.key, rankedGroups[j].group.key)
	})
	if limit > len(rankedGroups) {
		limit = len(rankedGroups)
	}
	out := make([]patternCallGroup, 0, limit)
	for _, item := range rankedGroups[:limit] {
		out = append(out, item.group)
	}
	sort.Slice(out, func(i, j int) bool {
		return patternCallSiteLess(out[i].key, out[j].key)
	})
	return out
}

func indexedRepoRoot(absPath, relPath string) (string, bool) {
	rel := filepath.Clean(filepath.FromSlash(relPath))
	if rel == "." || rel == "" || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	root := absPath
	for part := rel; part != "."; part = filepath.Dir(part) {
		root = filepath.Dir(root)
	}
	root = filepath.Clean(root)
	return root, filepath.Clean(filepath.Join(root, rel)) == filepath.Clean(absPath)
}

func currentFileMatches(path string, indexed []byte) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	current = bytes.TrimPrefix(current, []byte{0xEF, 0xBB, 0xBF})
	return bytes.Equal(current, indexed)
}

func cleanAbsolute(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}
