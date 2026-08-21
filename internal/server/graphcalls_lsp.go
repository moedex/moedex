//go:build lsp

package server

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"moedex/internal/graph"
	"moedex/internal/graph/candidates"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
	"moedex/internal/ingest"
	"moedex/internal/navigate"
	"moedex/internal/symbol"
)

type lspGraphNavigator interface {
	DocumentSymbol(context.Context, string) ([]navigate.Symbol, error)
	ReferencesDetailed(context.Context, navigate.Pos, bool) (navigate.LocationQueryResult, error)
}

type lspIndexedFile struct {
	path     string
	repoRoot string
	route    string
	shard    int
	blobID   uint64
	blob     *index.Blob
}

type lspDefinitionJob struct {
	name        string
	path        string
	repoRoot    string
	route       string
	at          navigate.Pos
	target      diskgraph.Key
	prioritized bool
}

type lspTargetContext struct {
	repoRoot string
	target   diskgraph.Key
}

type lspCallCollection struct {
	calls         []lspConfirmedCall
	authoritative map[lspTargetContext]struct{}
	stats         LSPGraphStats
}

type lspNamedTarget struct {
	name   string
	target diskgraph.Key
}

func collectLSPPatternReconciliation(ctx context.Context, merged *symbol.Corpus, shards []*index.Index, groups []patternCallGroup, crossRelevant map[string]bool, opts GraphBuildOptions) (*lspPatternReconciliation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.lspPatternGroupsOnly && len(groups) == 0 {
		return &lspPatternReconciliation{pruneSites: make(map[patternCallSiteKey]struct{})}, nil
	}
	rate := opts.LSPRequestsPerSecond
	if rate == 0 {
		rate = DefaultLSPRequestsPerSecond
	}
	timeout := opts.LSPRequestTimeout
	if timeout == 0 {
		timeout = DefaultLSPRequestTimeout
	}
	concurrency := opts.LSPConcurrency
	if concurrency == 0 {
		concurrency = DefaultLSPConcurrency()
	}

	pool := navigate.NewPool(navigate.Config{
		RequestTimeout: timeout,
		IdleTTL:        2 * timeout,
	})
	// Pool teardown is best-effort after every child has been reaped. Some
	// servers (including gopls versions that exit on the cancelled process
	// context after acknowledging shutdown) report a non-zero process status;
	// that must not discard already-confirmed graph results.
	defer func() { _ = pool.Close() }()

	pacer := newLSPRequestPacer(rate)
	var wantedGroups []patternCallGroup
	if opts.lspPatternGroupsOnly {
		wantedGroups = groups
	}
	collection, err := collectLSPCallEdgesConcurrentDetailed(ctx, pool, merged, shards, crossRelevant, timeout, pacer, concurrency, wantedGroups)
	if err != nil {
		return nil, err
	}
	if opts.lspPatternGroupsOnly {
		allowed := make(map[patternCallSiteKey]struct{}, len(groups))
		for _, group := range groups {
			allowed[group.key] = struct{}{}
		}
		filtered := collection.calls[:0]
		for _, call := range collection.calls {
			key := patternCallSiteKey{sourceBlob: call.evidence.BlobSHA, evidence: call.evidence.ByteOffset, name: call.name}
			if _, ok := allowed[key]; ok {
				filtered = append(filtered, call)
			}
		}
		collection.calls = filtered
	}
	reconciliation := reconcilePatternCallGroups(groups, collection, sourceContextCountsFromShards(shards))
	return &reconciliation, nil
}

func reconcilePatternCallGroups(groups []patternCallGroup, collection lspCallCollection, sourceContexts map[string]int) lspPatternReconciliation {
	reconciliation := lspPatternReconciliation{
		calls:      collection.calls,
		pruneSites: make(map[patternCallSiteKey]struct{}),
		stats:      collection.stats,
	}
	exactBySite := make(map[patternCallSiteKey][]lspConfirmedCall)
	definitionSet := make(map[diskgraph.Key]struct{})
	for _, call := range collection.calls {
		key := patternCallSiteKey{sourceBlob: call.evidence.BlobSHA, evidence: call.evidence.ByteOffset, name: call.name}
		exactBySite[key] = append(exactBySite[key], call)
	}
	for _, group := range groups {
		reconciliation.stats.CallGroups++
		reconciliation.stats.PatternCallEdges += group.numEdges()
		contexts := sourceContexts[group.key.sourceBlob]
		if contexts == 0 {
			contexts = 1
		}
		reconciliation.stats.ProjectedDefinitionRequests += contexts
		for _, target := range group.targets {
			definitionSet[target] = struct{}{}
		}
		exact := exactBySite[group.key]
		if len(exact) == 0 || sourceContexts[group.key.sourceBlob] != 1 {
			reconciliation.stats.RetainedUncertainCallGroups++
			continue
		}
		repoRoot := exact[0].repoRoot
		if repoRoot == "" {
			reconciliation.stats.RetainedUncertainCallGroups++
			continue
		}
		complete := true
		for _, call := range exact[1:] {
			if call.repoRoot != repoRoot {
				complete = false
				break
			}
		}
		if complete {
			for _, target := range group.targets {
				if _, ok := collection.authoritative[lspTargetContext{repoRoot: repoRoot, target: target}]; !ok {
					complete = false
					break
				}
			}
		}
		if !complete {
			reconciliation.stats.RetainedUncertainCallGroups++
			continue
		}
		reconciliation.pruneSites[group.key] = struct{}{}
		reconciliation.stats.ReconciledCallGroups++
		reconciliation.stats.PrunedPatternEdges += group.numEdges()
	}
	if reconciliation.stats.ProjectedReferenceRequests == 0 {
		reconciliation.stats.ProjectedReferenceRequests = len(definitionSet)
	}
	return reconciliation
}

func sourceContextCountsFromShards(shards []*index.Index) map[string]int {
	sets := make(map[string]map[string]struct{})
	for _, shard := range shards {
		if shard == nil {
			continue
		}
		for id := uint64(0); id < uint64(shard.NumBlobs()); id++ {
			blob := shard.Blob(id)
			if blob == nil || blob.SHA == "" {
				continue
			}
			set := sets[blob.SHA]
			if set == nil {
				set = make(map[string]struct{})
				sets[blob.SHA] = set
			}
			for _, file := range blob.Files {
				set[file.Repo+"\x00"+cleanAbsolute(file.AbsPath)] = struct{}{}
			}
		}
	}
	out := make(map[string]int, len(sets))
	for sha, set := range sets {
		out[sha] = len(set)
	}
	return out
}

func collectLSPCallEdges(ctx context.Context, nav lspGraphNavigator, merged *symbol.Corpus, shards []*index.Index, crossRelevant map[string]bool, timeout time.Duration, pacer *lspRequestPacer) ([]lspConfirmedCall, LSPGraphStats, error) {
	return collectLSPCallEdgesConcurrent(ctx, nav, merged, shards, crossRelevant, timeout, pacer, 1)
}

func collectLSPCallEdgesConcurrent(ctx context.Context, nav lspGraphNavigator, merged *symbol.Corpus, shards []*index.Index, crossRelevant map[string]bool, timeout time.Duration, pacer *lspRequestPacer, concurrency int) ([]lspConfirmedCall, LSPGraphStats, error) {
	collection, err := collectLSPCallEdgesConcurrentDetailed(ctx, nav, merged, shards, crossRelevant, timeout, pacer, concurrency, nil)
	return collection.calls, collection.stats, err
}

func collectLSPCallEdgesConcurrentDetailed(ctx context.Context, nav lspGraphNavigator, merged *symbol.Corpus, shards []*index.Index, crossRelevant map[string]bool, timeout time.Duration, pacer *lspRequestPacer, concurrency int, wantedGroups []patternCallGroup) (lspCallCollection, error) {
	var stats LSPGraphStats
	collection := lspCallCollection{authoritative: make(map[lspTargetContext]struct{})}
	if concurrency < 1 {
		concurrency = 1
	}
	files, restricted, err := lspEligibleFiles(merged, shards)
	stats.RestrictedWorkspaces = restricted
	if err != nil {
		collection.stats = stats
		return collection, err
	}
	stats.EligibleFiles = len(files)
	if len(files) == 0 {
		collection.stats = stats
		return collection, nil
	}
	byPath := make(map[string]lspIndexedFile, len(files))
	for _, file := range files {
		byPath[file.path] = file
	}

	jobsByKey := make(map[string]lspDefinitionJob)
	if wantedGroups != nil {
		jobsByKey = lspDefinitionJobsForGroups(files, wantedGroups, crossRelevant)
	} else {
		fileGroups := groupLSPFiles(files)
		documentResults := make([]lspDocumentResult, len(fileGroups))
		documentCounters := &lspPhaseCounters{}
		stopDocumentProgress := startLSPPhaseProgress("documentSymbol", len(files), len(fileGroups), concurrency, documentCounters)
		runLSPFileGroups(ctx, concurrency, fileGroups, documentResults, func(group []lspIndexedFile) lspDocumentResult {
			return collectLSPDocumentGroup(ctx, nav, group, byPath, merged, crossRelevant, nil, timeout, pacer, documentCounters)
		})
		stopDocumentProgress()
		for _, result := range documentResults {
			stats.DocumentRequests += result.requests
			stats.FailedRequests += result.failed
			if result.err != nil {
				collection.stats = stats
				return collection, result.err
			}
			for key, job := range result.jobs {
				jobsByKey[key] = job
			}
		}
	}

	jobs := make([]lspDefinitionJob, 0, len(jobsByKey))
	for _, job := range jobsByKey {
		jobs = append(jobs, job)
		if job.prioritized {
			stats.PrioritizedSymbols++
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].prioritized != jobs[j].prioritized {
			return jobs[i].prioritized
		}
		if jobs[i].repoRoot != jobs[j].repoRoot {
			return jobs[i].repoRoot < jobs[j].repoRoot
		}
		if jobs[i].path != jobs[j].path {
			return jobs[i].path < jobs[j].path
		}
		if jobs[i].target.SymbolOffset != jobs[j].target.SymbolOffset {
			return jobs[i].target.SymbolOffset < jobs[j].target.SymbolOffset
		}
		return jobs[i].name < jobs[j].name
	})
	stats.DiscoveredSymbols = len(jobs)
	stats.ProjectedReferenceRequests = len(jobs)

	referenceGroups := groupLSPDefinitionJobs(jobs)
	referenceResults := make([]lspReferenceResult, len(referenceGroups))
	referenceCounters := &lspPhaseCounters{}
	stopReferenceProgress := startLSPPhaseProgress("findReferences", len(jobs), len(referenceGroups), concurrency, referenceCounters)
	runLSPReferenceGroups(ctx, concurrency, referenceGroups, referenceResults, func(group []lspDefinitionJob) lspReferenceResult {
		return collectLSPReferenceGroup(ctx, nav, group, byPath, merged, timeout, pacer, referenceCounters)
	})
	stopReferenceProgress()
	unique := make(map[string]lspConfirmedCall)
	for _, result := range referenceResults {
		stats.ReferenceRequests += result.requests
		stats.FailedRequests += result.failed
		stats.ResolvedReferenceRequests += result.resolved
		stats.EmptyReferenceRequests += result.empty
		stats.UnsupportedReferenceRequests += result.unsupported
		stats.UnavailableReferenceRequests += result.unavailable
		stats.SkippedReferenceRequests += result.skipped
		stats.ReferenceLatencyTotal += result.latencyTotal
		if result.latencyMax > stats.ReferenceLatencyMax {
			stats.ReferenceLatencyMax = result.latencyMax
		}
		if result.err != nil {
			collection.stats = stats
			return collection, result.err
		}
		for key, call := range result.calls {
			unique[key] = call
		}
		for target := range result.authoritative {
			collection.authoritative[target] = struct{}{}
		}
	}

	out := make([]lspConfirmedCall, 0, len(unique))
	for _, call := range unique {
		out = append(out, call)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].source.BlobSHA != out[j].source.BlobSHA {
			return out[i].source.BlobSHA < out[j].source.BlobSHA
		}
		if out[i].source.SymbolOffset != out[j].source.SymbolOffset {
			return out[i].source.SymbolOffset < out[j].source.SymbolOffset
		}
		if out[i].evidence.BlobSHA != out[j].evidence.BlobSHA {
			return out[i].evidence.BlobSHA < out[j].evidence.BlobSHA
		}
		if out[i].evidence.ByteOffset != out[j].evidence.ByteOffset {
			return out[i].evidence.ByteOffset < out[j].evidence.ByteOffset
		}
		if out[i].name != out[j].name {
			return out[i].name < out[j].name
		}
		if out[i].target.BlobSHA != out[j].target.BlobSHA {
			return out[i].target.BlobSHA < out[j].target.BlobSHA
		}
		if out[i].target.SymbolOffset != out[j].target.SymbolOffset {
			return out[i].target.SymbolOffset < out[j].target.SymbolOffset
		}
		return out[i].repoRoot < out[j].repoRoot
	})
	collection.calls = out
	collection.stats = stats
	return collection, nil
}

func lspDefinitionJobsForGroups(files []lspIndexedFile, groups []patternCallGroup, crossRelevant map[string]bool) map[string]lspDefinitionJob {
	filesBySHA := make(map[string][]lspIndexedFile)
	for _, file := range files {
		filesBySHA[file.blob.SHA] = append(filesBySHA[file.blob.SHA], file)
	}
	wanted := make(map[lspNamedTarget]map[string]struct{})
	for _, group := range groups {
		sourceRoots := make(map[string]struct{})
		for _, sourceFile := range filesBySHA[group.key.sourceBlob] {
			sourceRoots[sourceFile.repoRoot] = struct{}{}
		}
		for _, target := range group.targets {
			key := lspNamedTarget{name: group.key.name, target: target}
			roots := wanted[key]
			if roots == nil {
				roots = make(map[string]struct{})
				wanted[key] = roots
			}
			for root := range sourceRoots {
				roots[root] = struct{}{}
			}
		}
	}

	jobs := make(map[string]lspDefinitionJob)
	targets := make([]lspNamedTarget, 0, len(wanted))
	for target := range wanted {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].name != targets[j].name {
			return targets[i].name < targets[j].name
		}
		return graphKeyLess(targets[i].target, targets[j].target)
	})
	for _, named := range targets {
		for _, file := range filesBySHA[named.target.BlobSHA] {
			if _, sameContext := wanted[named][file.repoRoot]; !sameContext {
				continue
			}
			if named.target.SymbolOffset > uint64(len(file.blob.Content)) {
				continue
			}
			job := lspDefinitionJob{
				name: named.name, path: file.path, repoRoot: file.repoRoot, route: file.route,
				at:          byteOffsetPos(file.path, file.blob.Content, int(named.target.SymbolOffset)),
				target:      named.target,
				prioritized: crossRelevant[named.name],
			}
			jobs[lspJobKey(job)] = job
		}
	}
	return jobs
}

type lspDocumentResult struct {
	jobs     map[string]lspDefinitionJob
	requests int
	failed   int
	err      error
}

type lspReferenceResult struct {
	calls         map[string]lspConfirmedCall
	authoritative map[lspTargetContext]struct{}
	requests      int
	failed        int
	resolved      int
	empty         int
	unsupported   int
	unavailable   int
	skipped       int
	latencyTotal  time.Duration
	latencyMax    time.Duration
	err           error
}

type lspPhaseCounters struct {
	completed atomic.Int64
	failed    atomic.Int64
}

func lspGraphAvailable() bool { return true }

func startLSPPhaseProgress(phase string, total, routes, workers int, counters *lspPhaseCounters) func() {
	started := time.Now()
	log.Printf("server: LSP %s scheduled %d request(s) across %d route(s) on %d worker(s)", phase, total, routes, min(workers, routes))
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				log.Printf("server: LSP %s progress %d/%d request(s), failures=%d, elapsed=%s",
					phase, counters.completed.Load(), total, counters.failed.Load(), time.Since(started).Round(time.Second))
			case <-done:
				return
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
		log.Printf("server: LSP %s complete %d/%d request(s), failures=%d, elapsed=%s",
			phase, counters.completed.Load(), total, counters.failed.Load(), time.Since(started).Round(time.Millisecond))
	}
}

func groupLSPFiles(files []lspIndexedFile) [][]lspIndexedFile {
	byRoute := make(map[string][]lspIndexedFile)
	for _, file := range files {
		byRoute[file.route] = append(byRoute[file.route], file)
	}
	groups := make([][]lspIndexedFile, 0, len(byRoute))
	for _, group := range byRoute {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if len(groups[i]) != len(groups[j]) {
			return len(groups[i]) > len(groups[j])
		}
		return groups[i][0].route < groups[j][0].route
	})
	return groups
}

func groupLSPDefinitionJobs(jobs []lspDefinitionJob) [][]lspDefinitionJob {
	byRoute := make(map[string][]lspDefinitionJob)
	for _, job := range jobs {
		byRoute[job.route] = append(byRoute[job.route], job)
	}
	groups := make([][]lspDefinitionJob, 0, len(byRoute))
	for _, group := range byRoute {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if len(groups[i]) != len(groups[j]) {
			return len(groups[i]) > len(groups[j])
		}
		return groups[i][0].route < groups[j][0].route
	})
	return groups
}

func runLSPFileGroups(ctx context.Context, concurrency int, groups [][]lspIndexedFile, results []lspDocumentResult, run func([]lspIndexedFile) lspDocumentResult) {
	workers := min(max(concurrency, 1), len(groups))
	if workers == 0 {
		return
	}
	work := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				if ctx.Err() != nil {
					results[i].err = ctx.Err()
					continue
				}
				results[i] = run(groups[i])
			}
		}()
	}
	for i := range groups {
		work <- i
	}
	close(work)
	wg.Wait()
}

func runLSPReferenceGroups(ctx context.Context, concurrency int, groups [][]lspDefinitionJob, results []lspReferenceResult, run func([]lspDefinitionJob) lspReferenceResult) {
	workers := min(max(concurrency, 1), len(groups))
	if workers == 0 {
		return
	}
	work := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				if ctx.Err() != nil {
					results[i].err = ctx.Err()
					continue
				}
				results[i] = run(groups[i])
			}
		}()
	}
	for i := range groups {
		work <- i
	}
	close(work)
	wg.Wait()
}

func collectLSPDocumentGroup(ctx context.Context, nav lspGraphNavigator, files []lspIndexedFile, byPath map[string]lspIndexedFile, merged *symbol.Corpus, crossRelevant map[string]bool, wantedNames map[string]bool, timeout time.Duration, pacer *lspRequestPacer, counters *lspPhaseCounters) lspDocumentResult {
	result := lspDocumentResult{jobs: make(map[string]lspDefinitionJob)}
	for _, file := range files {
		if err := pacer.Wait(ctx); err != nil {
			result.err = err
			return result
		}
		result.requests++
		requestCtx, cancel := requestContext(ctx, timeout)
		syms, queryErr := nav.DocumentSymbol(requestCtx, file.path)
		cancel()
		counters.completed.Add(1)
		if queryErr != nil {
			if ctx.Err() != nil {
				result.err = ctx.Err()
				return result
			}
			result.failed++
			counters.failed.Add(1)
			continue
		}
		if len(syms) == 0 {
			// Partial-capability servers may not implement documentSymbol. The
			// syntactic sidecar remains a safe find-references work list.
			for _, sym := range merged.Symbols(file.shard, file.blobID) {
				if !candidates.Exported(sym.Name) || wantedNames != nil && !wantedNames[sym.Name] || sym.NameStart < 0 || sym.NameEnd <= sym.NameStart {
					continue
				}
				job := lspDefinitionJob{
					name: sym.Name, path: file.path, repoRoot: file.repoRoot, route: file.route,
					at:          byteOffsetPos(file.path, file.blob.Content, sym.NameStart),
					target:      diskgraph.Key{BlobSHA: file.blob.SHA, SymbolOffset: uint64(sym.NameStart)},
					prioritized: crossRelevant[sym.Name],
				}
				result.jobs[lspJobKey(job)] = job
			}
			continue
		}
		for _, sym := range syms {
			if !candidates.Exported(sym.Name) || wantedNames != nil && !wantedNames[sym.Name] {
				continue
			}
			definitionFile, ok := byPath[cleanAbsolute(sym.Loc.File)]
			if !ok || definitionFile.repoRoot != file.repoRoot {
				continue
			}
			start, _, ok := namedSpan(definitionFile.blob.Content, sym.Loc.Start, sym.Loc.End, sym.Name)
			if !ok {
				continue
			}
			job := lspDefinitionJob{
				name: sym.Name, path: definitionFile.path, repoRoot: definitionFile.repoRoot, route: definitionFile.route,
				at:          byteOffsetPos(definitionFile.path, definitionFile.blob.Content, start),
				target:      diskgraph.Key{BlobSHA: definitionFile.blob.SHA, SymbolOffset: uint64(start)},
				prioritized: crossRelevant[sym.Name],
			}
			result.jobs[lspJobKey(job)] = job
		}
	}
	return result
}

func collectLSPReferenceGroup(ctx context.Context, nav lspGraphNavigator, jobs []lspDefinitionJob, byPath map[string]lspIndexedFile, merged *symbol.Corpus, timeout time.Duration, pacer *lspRequestPacer, counters *lspPhaseCounters) lspReferenceResult {
	result := lspReferenceResult{
		calls:         make(map[string]lspConfirmedCall),
		authoritative: make(map[lspTargetContext]struct{}),
	}
jobsLoop:
	for jobIndex, job := range jobs {
		if err := pacer.Wait(ctx); err != nil {
			result.err = err
			return result
		}
		result.requests++
		requestCtx, cancel := requestContext(ctx, timeout)
		started := time.Now()
		query, queryErr := nav.ReferencesDetailed(requestCtx, job.at, false)
		elapsed := time.Since(started)
		cancel()
		result.latencyTotal += elapsed
		if elapsed > result.latencyMax {
			result.latencyMax = elapsed
		}
		counters.completed.Add(1)
		if queryErr != nil {
			if ctx.Err() != nil {
				result.err = ctx.Err()
				return result
			}
			result.unavailable++
			result.failed++
			counters.failed.Add(1)
			result.skipped += len(jobs) - jobIndex - 1
			break jobsLoop
		}
		switch query.Status {
		case navigate.LocationQueryResolved:
			result.resolved++
			result.authoritative[lspTargetContext{repoRoot: job.repoRoot, target: job.target}] = struct{}{}
		case navigate.LocationQueryReadyEmpty:
			result.empty++
			result.authoritative[lspTargetContext{repoRoot: job.repoRoot, target: job.target}] = struct{}{}
		case navigate.LocationQueryUnsupported:
			result.unsupported++
			result.failed++
			counters.failed.Add(1)
			result.skipped += len(jobs) - jobIndex - 1
			break jobsLoop
		case navigate.LocationQueryUnavailable:
			result.unavailable++
			result.failed++
			counters.failed.Add(1)
			result.skipped += len(jobs) - jobIndex - 1
			break jobsLoop
		default:
			result.failed++
			counters.failed.Add(1)
			result.skipped += len(jobs) - jobIndex - 1
			break jobsLoop
		}
		for _, location := range query.Locations {
			callerFile, ok := byPath[cleanAbsolute(location.File)]
			if !ok || callerFile.repoRoot != job.repoRoot {
				continue
			}
			start, end, ok := namedSpan(callerFile.blob.Content, location.Start, location.End, job.name)
			if !ok || !callSyntaxAt(callerFile.blob.Content, end) {
				continue
			}
			enclosing, ok := merged.Enclosing(callerFile.shard, callerFile.blobID, start)
			if !ok || enclosing.NameStart < 0 {
				continue
			}
			call := lspConfirmedCall{
				source:   diskgraph.Key{BlobSHA: callerFile.blob.SHA, SymbolOffset: uint64(enclosing.NameStart)},
				target:   job.target,
				name:     job.name,
				repoRoot: job.repoRoot,
				evidence: graph.Evidence{
					BlobSHA: callerFile.blob.SHA, ByteOffset: uint64(start), ByteLength: uint64(end - start),
				},
			}
			result.calls[lspCallKey(call)] = call
		}
	}
	return result
}

func lspEligibleFiles(merged *symbol.Corpus, shards []*index.Index) ([]lspIndexedFile, int, error) {
	if merged == nil {
		return nil, 0, fmt.Errorf("server: nil symbol corpus for LSP graph pass")
	}
	byPath := make(map[string]lspIndexedFile)
	roots := make(map[string]struct{})
	for shardID, ix := range shards {
		if ix == nil {
			continue
		}
		for blobID := uint64(0); blobID < uint64(ix.NumBlobs()); blobID++ {
			blob := ix.Blob(blobID)
			if blob == nil {
				continue
			}
			for _, ref := range blob.Files {
				path := cleanAbsolute(ref.AbsPath)
				if path == "" {
					continue
				}
				if _, supported := navigate.LanguageForPath(path); !supported {
					continue
				}
				root, ok := indexedRepoRoot(path, ref.RelPath)
				if !ok || !currentFileMatches(path, blob.Content) {
					continue
				}
				if _, duplicate := byPath[path]; duplicate {
					continue
				}
				language, workspace := navigate.WorkspaceRoute(path)
				byPath[path] = lspIndexedFile{
					path: path, repoRoot: root, route: lspRouteKey(language, workspace),
					shard: shardID, blobID: blobID, blob: blob,
				}
				roots[root] = struct{}{}
			}
		}
	}

	rootAllowed := make(map[string]bool, len(roots))
	restricted := 0
	rootNames := make([]string, 0, len(roots))
	for root := range roots {
		rootNames = append(rootNames, root)
	}
	sort.Strings(rootNames)
	// Validate every workspace before the first LSP request. One malformed
	// policy aborts the pass fail-closed, and one level-1 path rejects the whole
	// workspace because the external server can scan beyond the queried file.
	for _, root := range rootNames {
		allowed, err := ingest.LSPWorkspaceAllowed(root)
		if err != nil {
			return nil, restricted, fmt.Errorf("server: LSP workspace privacy preflight %s: %w", root, err)
		}
		rootAllowed[root] = allowed
		if !allowed {
			restricted++
		}
	}

	files := make([]lspIndexedFile, 0, len(byPath))
	for _, file := range byPath {
		if rootAllowed[file.repoRoot] {
			files = append(files, file)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, restricted, nil
}

func byteOffsetPos(path string, content []byte, offset int) navigate.Pos {
	if offset < 0 {
		offset = 0
	}
	if offset > len(content) {
		offset = len(content)
	}
	line := 1 + bytes.Count(content[:offset], []byte{'\n'})
	lineStart := bytes.LastIndexByte(content[:offset], '\n') + 1
	return navigate.Pos{File: path, Line: line, Col: offset - lineStart + 1}
}

func byteOffset(content []byte, pos navigate.Pos) (int, bool) {
	if pos.Line < 1 || pos.Col < 1 {
		return 0, false
	}
	lineStart := 0
	for line := 1; line < pos.Line; line++ {
		next := bytes.IndexByte(content[lineStart:], '\n')
		if next < 0 {
			return 0, false
		}
		lineStart += next + 1
	}
	lineEnd := len(content)
	if next := bytes.IndexByte(content[lineStart:], '\n'); next >= 0 {
		lineEnd = lineStart + next
	}
	offset := lineStart + pos.Col - 1
	if offset < lineStart || offset > lineEnd {
		return 0, false
	}
	return offset, true
}

func namedSpan(content []byte, startPos, endPos navigate.Pos, name string) (int, int, bool) {
	start, ok := byteOffset(content, startPos)
	if !ok || name == "" {
		return 0, 0, false
	}
	end, endOK := byteOffset(content, endPos)
	if endOK && end >= start && end <= len(content) {
		if at := identifierOccurrence(content, start, end, name); at >= 0 {
			return at, at + len(name), true
		}
	}
	if start+len(name) <= len(content) && string(content[start:start+len(name)]) == name && identifierBounds(content, start, start+len(name)) {
		return start, start + len(name), true
	}
	return 0, 0, false
}

func identifierOccurrence(content []byte, start, end int, name string) int {
	if start < 0 || end < start || end > len(content) {
		return -1
	}
	window := content[start:end]
	for from := 0; from <= len(window)-len(name); {
		rel := bytes.Index(window[from:], []byte(name))
		if rel < 0 {
			return -1
		}
		at := start + from + rel
		if identifierBounds(content, at, at+len(name)) {
			return at
		}
		from += rel + 1
	}
	return -1
}

func identifierBounds(content []byte, start, end int) bool {
	return (start == 0 || !lspIdentifierByte(content[start-1])) &&
		(end == len(content) || !lspIdentifierByte(content[end]))
}

func lspIdentifierByte(b byte) bool {
	return b == '_' || b == '$' || b >= 0x80 ||
		b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func callSyntaxAt(content []byte, nameEnd int) bool {
	at := skipLSPSpaces(content, nameEnd)
	if at < len(content) && content[at] == '[' {
		var ok bool
		at, ok = skipBalanced(content, at, '[', ']')
		if !ok {
			return false
		}
		at = skipLSPSpaces(content, at)
	}
	if at < len(content) && content[at] == '<' {
		var ok bool
		at, ok = skipBalanced(content, at, '<', '>')
		if !ok {
			return false
		}
		at = skipLSPSpaces(content, at)
	}
	return at < len(content) && content[at] == '('
}

func skipLSPSpaces(content []byte, at int) int {
	for at < len(content) {
		switch content[at] {
		case ' ', '\t', '\r', '\n':
			at++
		default:
			return at
		}
	}
	return at
}

func skipBalanced(content []byte, at int, open, close byte) (int, bool) {
	depth := 0
	for ; at < len(content); at++ {
		switch content[at] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return at + 1, true
			}
		case '\n', '\r', ';', '{', '}':
			return at, false
		}
	}
	return at, false
}

func requestContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

func lspJobKey(job lspDefinitionJob) string {
	return fmt.Sprintf("%s\x00%d\x00%s", job.path, job.target.SymbolOffset, job.name)
}

func lspCallKey(call lspConfirmedCall) string {
	return fmt.Sprintf("%s\x00%d\x00%s\x00%d\x00%s\x00%d\x00%d\x00%s", call.source.BlobSHA, call.source.SymbolOffset, call.target.BlobSHA, call.target.SymbolOffset, call.evidence.BlobSHA, call.evidence.ByteOffset, call.evidence.ByteLength, call.name)
}

type lspRequestPacer struct {
	mu       sync.Mutex
	interval time.Duration
	last     time.Time
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
}

func newLSPRequestPacer(requestsPerSecond float64) *lspRequestPacer {
	return &lspRequestPacer{
		interval: time.Duration(float64(time.Second) / requestsPerSecond),
		now:      time.Now,
		sleep:    sleepLSPRequest,
	}
}

// Wait spaces request starts globally across the entire graph pass. The graph
// scheduler serializes each (language, workspace) route separately while this
// pacer bounds aggregate starts across concurrently active routes.
func (p *lspRequestPacer) Wait(ctx context.Context) error {
	if p == nil || p.interval <= 0 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if !p.last.IsZero() {
		if delay := p.last.Add(p.interval).Sub(now); delay > 0 {
			if err := p.sleep(ctx, delay); err != nil {
				return err
			}
			now = p.now()
		}
	}
	p.last = now
	return nil
}

func sleepLSPRequest(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
