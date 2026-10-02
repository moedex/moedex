package graphbuild

// graphrefresh.go is phase 12 of docs/GRAPH-LAYER-PLAN.md: a CAS-aware
// incremental rebuild of the graph adjacency file.
//
// # Why the delta unit is a NAME, not a blob
//
// The graph is generated one name at a time (candidates.GenerateCandidates), and
// the edges for a name are a pure function of that name's occurrences across the
// corpus. So the sound partition is by name:
//
//	E(name) is unchanged  <=  no blob holding an occurrence of name was added or removed
//
// The corpus roster also includes repository/path contexts and the type-binding
// policy version. Byte-identical content can bind differently after a move or
// when copied into another repository, so those changes must invalidate it too.
//
// # Where the two halves of the dirty set come from
//
//   - ADDED blobs are scanned for their maximal identifier runs
//     (candidates.EachIdentifier) plus the names the symbol extractors recorded.
//   - REMOVED blobs no longer have content to scan, so their names come from the
//     PREVIOUS graph: every edge whose source or target was that blob.
//
// # Generations
//
// Each edge records the generation it was last computed in. A carried-forward
// edge keeps its older stamp, so "when was this edge last verified against the
// corpus" is answerable per edge.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"moedex/internal/graph"
	"moedex/internal/graph/candidates"
	"moedex/internal/graph/cluster"
	"moedex/internal/graph/diskgraph"
)

// GraphRefreshStats reports what an incremental graph refresh actually did.
type GraphRefreshStats struct {
	Generation         uint64
	PreviousGeneration uint64

	FullRebuild bool
	Reason      string
	Unchanged   bool

	CorpusBlobs  int
	BlobsAdded   int
	BlobsRemoved int

	NamesEligible   int
	NamesRecomputed int
	NamesCarried    int

	EdgesRecomputed int
	EdgesCarried    int
	EdgesDropped    int
	Schedule        GraphScheduleStats
	Cluster         cluster.BuildReport
	Counts          GraphBuildCounts
	LSP             LSPGraphStats
}

// RefreshGraph rebuilds dir's graph incrementally against the graph already
// there, recomputing only the names the content delta made stale. It falls back
// to a full build — and says so in the returned stats — whenever there is no
// usable prior graph.
//
// The result is identical to BuildGraph's for the same shard set, edge for edge
// and in the same order; only the per-edge generation stamps differ.
func RefreshGraph(dir string) (path string, stats GraphRefreshStats, err error) {
	return RefreshGraphWithOptions(dir, GraphBuildOptions{})
}

// RefreshGraphWithOptions is RefreshGraph plus the optional tagged passes used
// by BuildGraphWithOptions. The default wrapper remains byte-for-byte
// pass-through in builds without those tags.
func RefreshGraphWithOptions(dir string, opts GraphBuildOptions) (path string, stats GraphRefreshStats, err error) {
	if math.IsNaN(opts.LSPRequestsPerSecond) || math.IsInf(opts.LSPRequestsPerSecond, 0) || opts.LSPRequestsPerSecond < 0 {
		return "", stats, fmt.Errorf("server: graph LSP requests/second must be finite and non-negative")
	}
	if opts.LSPConcurrency < 0 {
		return "", stats, fmt.Errorf("server: graph LSP concurrency must be non-negative")
	}
	if opts.LSPRequestTimeout < 0 {
		return "", stats, fmt.Errorf("server: graph LSP request timeout must be non-negative")
	}
	if opts.LSPStats != nil {
		*opts.LSPStats = LSPGraphStats{}
	}
	sweep, err := openGraphSweep(dir)
	if err != nil {
		return "", stats, err
	}
	defer func() {
		if closeErr := sweep.Close(); err == nil {
			err = closeErr
		}
	}()
	stats.CorpusBlobs = len(sweep.identity)
	stats.NamesEligible = len(sweep.names)

	previous, reason := openPreviousGraph(dir)
	if previous == nil {
		stats.FullRebuild = true
		stats.Reason = reason
		stats.Generation = diskgraph.FirstGeneration
		path, err = sweep.rebuildAll(dir, &stats, opts)
		return path, stats, err
	}
	defer func() {
		if closeErr := previous.Close(); err == nil {
			err = closeErr
		}
	}()
	stats.PreviousGeneration = previous.Generation()

	added, removed := sweep.contentDelta(previous)
	stats.BlobsAdded, stats.BlobsRemoved = len(added), len(removed)
	legacyQualityEdges := sweep.hasLegacyQualityEdges(previous)
	if len(added) == 0 && len(removed) == 0 && !legacyQualityEdges {
		stats.Unchanged = true
		stats.Generation = previous.Generation()
		stats.NamesCarried = len(sweep.names)
		stats.EdgesCarried = previous.NumEdges()
		stats.Cluster, stats.Counts, err = finalizeGraph(dir, sweep, previous, 0, 0)
		return GraphPath(dir), stats, err
	}
	if legacyQualityEdges {
		stats.Reason = "legacy graph-quality edges require cleanup"
	}

	dirty := sweep.dirtyNames(previous, added, removed)
	stats.Generation = previous.Generation() + 1

	carried := make(map[string][]carriedGraphRecord)
	var carriedUnnamed []carriedGraphRecord
	imported := newImportedTargetSets(previous)
	previous.EachRecord(func(record diskgraph.PhysicalRecord) bool {
		key, edge := record.Source, record.Edge
		count := 1
		if record.Factored {
			count = previous.TargetCount(record.Targets)
			if record.Exclude >= 0 {
				count--
			}
		}
		if edge.Name != "" {
			if _, stale := dirty[edge.Name]; stale {
				return true
			}
			if _, eligible := sweep.eligible[edge.Name]; !eligible {
				stats.EdgesDropped += count
				return true
			}
			if edge.Confidence == graph.Candidate && !sweep.sourceResolvesToSymbol(key) {
				stats.EdgesDropped += count
				sweep.suppressedRawCandidates.Add(int64(count))
				return true
			}
			if record.Factored {
				targets := imported.get(record.Targets)
				if edge.Confidence == graph.Pattern {
					signature := "repos:" + strings.Join(sweep.reposBySHA[key.BlobSHA], "\x00")
					targets = imported.scoped(record.Targets, signature, func(target diskgraph.Key) bool { return sweep.blobSHAsShareRepository(key.BlobSHA, target.BlobSHA) })
				}
				group := imported.source(record, targets)
				dropped := count - group.count()
				stats.EdgesDropped += dropped
				sweep.suppressedCrossRepoPatterns.Add(int64(dropped))
				if group.count() > 0 {
					carried[edge.Name] = append(carried[edge.Name], carriedGraphRecord{group: &group})
				}
			} else {
				if edge.Confidence == graph.Pattern && !sweep.blobSHAsShareRepository(key.BlobSHA, edge.TargetBlob) {
					stats.EdgesDropped++
					sweep.suppressedCrossRepoPatterns.Add(1)
					return true
				}
				carried[edge.Name] = append(carried[edge.Name], carriedGraphRecord{source: key, edge: edge})
			}
			return true
		}
		if edge.Type != diskgraph.EdgeCalls && edge.Type != diskgraph.EdgeSimilarTo {
			return true
		}
		_, sourceChanged := removed[key.BlobSHA]
		if edge.Type == diskgraph.EdgeSimilarTo {
			sourceChanged = len(sweep.sites[key.BlobSHA]) == 0
		}
		if sourceChanged {
			stats.EdgesDropped += count
			return true
		}
		if edge.Type == diskgraph.EdgeSimilarTo && edge.Confidence == graph.Candidate {
			edge.Confidence = graph.Pattern
			record.Edge = edge
		}
		keep := func(target diskgraph.Key) bool {
			if edge.Type == diskgraph.EdgeSimilarTo {
				return len(sweep.sites[target.BlobSHA]) > 0
			}
			_, gone := removed[target.BlobSHA]
			return !gone
		}
		if record.Factored {
			targets := imported.scoped(record.Targets, fmt.Sprintf("removed:%d", edge.Type), keep)
			group := imported.source(record, targets)
			stats.EdgesDropped += count - group.count()
			if group.count() > 0 {
				carriedUnnamed = append(carriedUnnamed, carriedGraphRecord{group: &group})
			}
		} else if keep(diskgraph.Key{BlobSHA: edge.TargetBlob, SymbolOffset: edge.TargetOffset}) {
			carriedUnnamed = append(carriedUnnamed, carriedGraphRecord{source: key, edge: edge})
		} else {
			stats.EdgesDropped++
		}
		return true
	})

	var dirtyNames []string
	for _, name := range sweep.names {
		if _, stale := dirty[name]; stale {
			dirtyNames = append(dirtyNames, name)
		}
	}

	recomputed, schedule, err := sweep.computeFactoredNames(dirtyNames, stats.Generation)
	if err != nil {
		return "", stats, err
	}
	stats.Schedule = schedule
	recomputedByName := make(map[string][]factoredSource, len(dirtyNames))
	for i, name := range dirtyNames {
		recomputedByName[name] = recomputed[i].groups
	}
	crossRelevant := make(map[string]bool)
	for i, name := range dirtyNames {
		if recomputed[i].crossShard {
			crossRelevant[name] = true
		}
	}
	refreshCtx := opts.Context
	if refreshCtx == nil {
		refreshCtx = context.Background()
	}
	refreshOpts := opts
	refreshOpts.lspPatternGroupsOnly = true
	var callGroups []patternCallGroup
	if lspGraphAvailable() {
		callGroups = factoredPatternCallGroups(recomputed)
	}
	reconciliation, err := collectLSPPatternReconciliation(refreshCtx, sweep.merged, sweep.idxs, callGroups, crossRelevant, refreshOpts)
	if err != nil {
		return "", stats, err
	}
	recomputed = nil

	builder := diskgraph.NewBuilder()
	builder.SetGeneration(stats.Generation)
	sweep.recordCorpusRoster(builder)
	if err := sweep.addDefinitionNodes(builder); err != nil {
		return "", stats, err
	}
	emit := newGraphEmitter(builder)
	compact := newFactoredEmitter(builder)
	for _, name := range sweep.names {
		if groups, stale := recomputedByName[name]; stale {
			stats.NamesRecomputed++
			before := builder.NumEdges()
			for _, group := range groups {
				if reconciliation.prunes(group.template()) {
					continue
				}
				if err := compact.add(group); err != nil {
					return "", stats, err
				}
			}
			stats.EdgesRecomputed += int(builder.NumEdges() - before)
			delete(recomputedByName, name)
			continue
		}
		stats.NamesCarried++
		for _, record := range carried[name] {
			if err := record.add(emit, compact); err != nil {
				return "", stats, err
			}
			stats.EdgesCarried += record.count()
		}
		delete(carried, name)
	}
	for _, record := range carriedUnnamed {
		if err := record.add(emit, compact); err != nil {
			return "", stats, err
		}
		stats.EdgesCarried += record.count()
	}

	if err := addReconciledLSPCalls(builder, emit.seen, reconciliation, stats.Generation); err != nil {
		return "", stats, err
	}
	stats.LSP = reconciliation.stats
	if opts.LSPStats != nil {
		*opts.LSPStats = reconciliation.stats
	}

	// The six tag-independent whole-corpus passes are cheap pure functions of
	// the current corpus and never consult the previous graph, so they are
	// always regenerated fresh here instead of being diffed — see
	// addWholeCorpusEdges.
	before := builder.NumEdges()
	if _, err := sweep.addWholeCorpusEdges(builder, emit.seen); err != nil {
		return "", stats, err
	}
	stats.EdgesRecomputed += int(builder.NumEdges() - before)

	path, err = saveGraph(builder, dir)
	if err == nil {
		stats.Cluster, stats.Counts, err = finalizeGraph(dir, sweep, nil, int(sweep.suppressedRawCandidates.Load()), int(sweep.suppressedCrossRepoPatterns.Load()))
	}
	return path, stats, err
}

func (s *graphSweep) sourceResolvesToSymbol(key diskgraph.Key) bool {
	if key.SymbolOffset > uint64(^uint(0)>>1) {
		return false
	}
	for _, site := range s.sites[key.BlobSHA] {
		if _, ok := s.merged.Enclosing(site.shard, site.blob, int(key.SymbolOffset)); ok {
			return true
		}
	}
	return false
}

func (s *graphSweep) hasLegacyQualityEdges(previous *diskgraph.Graph) bool {
	resolved := make(map[diskgraph.Key]bool)
	checked := make(map[diskgraph.Key]bool)
	found := false
	imported := newImportedTargetSets(previous)
	previous.EachRecord(func(record diskgraph.PhysicalRecord) bool {
		source, edge := record.Source, record.Edge
		if edge.Type == diskgraph.EdgeSimilarTo && edge.Confidence == graph.Candidate {
			found = true
			return false
		}
		if edge.Confidence == graph.Pattern && edge.Name != "" {
			if record.Factored {
				signature := "repos:" + strings.Join(s.reposBySHA[source.BlobSHA], "\x00")
				original := imported.get(record.Targets)
				filtered := imported.scoped(record.Targets, signature, func(target diskgraph.Key) bool { return s.blobSHAsShareRepository(source.BlobSHA, target.BlobSHA) })
				if len(filtered.keys) != len(original.keys) {
					found = true
					return false
				}
			} else if !s.blobSHAsShareRepository(source.BlobSHA, edge.TargetBlob) {
				found = true
				return false
			}
		}
		// Whole-corpus type passes intentionally retain ambiguous bindings as
		// unnamed Candidate edges, including top-level DI registration sites.
		// Only the per-name candidate sweep's raw occurrences are legacy noise.
		if edge.Confidence != graph.Candidate || edge.Name == "" {
			return true
		}
		if !checked[source] {
			resolved[source] = s.sourceResolvesToSymbol(source)
			checked[source] = true
		}
		if !resolved[source] {
			found = true
			return false
		}
		return true
	})
	return found
}

type carriedGraphEdge struct {
	node uint32
	edge uint32
}

// CarryGraphSeed hard-links srcDir's graph into dstDir so a refresh that
// rebuilt the shard dir from scratch still has a previous generation to diff
// against.
func CarryGraphSeed(srcDir, dstDir string) bool {
	src := GraphPath(srcDir)
	info, err := os.Lstat(src)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return os.Link(src, GraphPath(dstDir)) == nil
}

func openPreviousGraph(dir string) (*diskgraph.Graph, string) {
	path := GraphPath(dir)
	g, err := diskgraph.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "no previous graph"
		}
		return nil, fmt.Sprintf("previous graph unusable: %v", err)
	}
	if g.NumNodes() > math.MaxUint32 || g.NumEdges() > math.MaxUint32 {
		reason := fmt.Sprintf("previous graph too large to carry forward (%d nodes, %d edges)", g.NumNodes(), g.NumEdges())
		_ = g.Close()
		return nil, reason
	}
	return g, ""
}

// rebuildAll is the from-scratch fallback RefreshGraph takes when there is no
// usable previous graph to diff against. It calls the exact same buildAllEdges
// pass BuildGraphWithOptions does — every edge family, not just the per-name
// sweep — so this path and a full BuildGraph produce the same graph, as the
// package doc for RefreshGraph promises.
func (s *graphSweep) rebuildAll(dir string, stats *GraphRefreshStats, opts GraphBuildOptions) (string, error) {
	builder := diskgraph.NewBuilder()
	builder.SetGeneration(stats.Generation)
	s.recordCorpusRoster(builder)
	emit := newGraphEmitter(builder)

	report, err := s.buildAllEdges(builder, emit, stats.Generation, opts)
	if err != nil {
		return "", err
	}
	stats.Schedule = report.Schedule
	if opts.LSPStats != nil {
		stats.LSP = *opts.LSPStats
	}

	stats.NamesRecomputed = len(s.names)
	stats.EdgesRecomputed = int(builder.NumEdges())
	stats.BlobsAdded = len(s.identity)
	path, err := saveGraph(builder, dir)
	if err == nil {
		stats.Cluster, stats.Counts, err = finalizeGraph(dir, s, nil, int(s.suppressedRawCandidates.Load()), int(s.suppressedCrossRepoPatterns.Load()))
	}
	return path, err
}

const graphCandidateBatchEdges = 32 * 1024

type graphNameResult struct {
	Edges      []graphKeyEdge
	CrossShard bool
}

type graphBatchTask struct {
	nameIndex  int
	batchIndex int
	start      int
	end        int
	estimate   uint64
}

// candidateSourceBatches divides sources into contiguous ranges whose candidate
// fan-out stays near maxEdges. A source is indivisible because all definitions
// for it must retain target order, so a name with more definitions than maxEdges
// has one source per batch.
func candidateSourceBatches(sources, definitions, maxEdges int) [][2]int {
	if sources <= 0 || definitions <= 0 {
		return nil
	}
	if maxEdges < 1 {
		maxEdges = 1
	}
	perBatch := maxEdges / definitions
	if perBatch < 1 {
		perBatch = 1
	}
	ranges := make([][2]int, 0, (sources+perBatch-1)/perBatch)
	for start := 0; start < sources; start += perBatch {
		end := start + perBatch
		if end > sources {
			end = sources
		}
		ranges = append(ranges, [2]int{start, end})
	}
	return ranges
}

// computeEdgesParallel first prepares each name once, then schedules bounded
// source batches through a shared queue. A common name can therefore occupy many
// workers instead of becoming the one-core tail of a corpus build. Results are
// reassembled by name and source range, preserving production ordering exactly.
func (s *graphSweep) computeEdgesParallel(names []string, generation uint64) ([]graphNameResult, GraphScheduleStats, error) {
	return s.computeEdgesParallelBatched(names, generation, graphCandidateBatchEdges)
}

func (s *graphSweep) computeEdgesParallelBatched(names []string, generation uint64, maxBatchEdges int) ([]graphNameResult, GraphScheduleStats, error) {
	workers := runtime.GOMAXPROCS(0)
	stats := GraphScheduleStats{Workers: workers, Names: len(names)}
	sourcesBefore, pairsBefore, retainedBefore := s.sourcesVerified.Load(), s.candidatePairs.Load(), s.retainedPairs.Load()
	prepareStart := time.Now()
	// Build the complete text arm once for the names this build/refresh needs.
	// Always start from the original corpus so a repeated preparation releases
	// the prior table and a budget fallback cannot retain a stale table budget.
	if s.baseCorpus == nil {
		s.baseCorpus = s.corpus
	}
	s.corpus = s.baseCorpus
	log.Printf("server: graph occurrence scan started names=%d budget_bytes=%d", len(names), graphTextOccurrenceBytes)
	scanStart := time.Now()
	s.corpus, stats.TextOccurrences = candidates.WithTextOccurrences(s.baseCorpus, names, graphTextOccurrenceBytes)
	stats.TextScanElapsed = time.Since(scanStart)
	log.Printf("server: graph occurrence scan complete built=%t names=%d/%d blobs=%d content_bytes=%d occurrences=%d retained_bytes=%d budget_bytes=%d fallback=%q elapsed=%s",
		stats.TextOccurrences.Built, stats.TextOccurrences.IndexedNames, stats.TextOccurrences.RequestedNames,
		stats.TextOccurrences.BlobsScanned, stats.TextOccurrences.ContentBytes, stats.TextOccurrences.Occurrences,
		stats.TextOccurrences.RetainedBytes, stats.TextOccurrences.BudgetBytes, stats.TextOccurrences.FallbackReason, stats.TextScanElapsed.Round(time.Millisecond))
	var preparedNames atomic.Int64
	var preparationLogTime atomic.Int64
	preparationLogTime.Store(time.Now().UnixNano())
	prepared, err := computeParallel(names, workers, func(name string) (*preparedGraphName, error) {
		result, err := s.prepareGraphName(candidates.PrepareName(s.corpus, name))
		completed := preparedNames.Add(1)
		now := time.Now().UnixNano()
		previous := preparationLogTime.Load()
		if now-previous >= int64(30*time.Second) && preparationLogTime.CompareAndSwap(previous, now) {
			log.Printf("server: graph preparation progress %d/%d names complete; sources_verified=%d elapsed=%s",
				completed, len(names), s.sourcesVerified.Load()-sourcesBefore, time.Since(prepareStart).Round(time.Second))
		}
		return result, err
	})
	stats.PreparationElapsed = time.Since(prepareStart)
	if err != nil {
		return nil, stats, err
	}

	results := make([]graphNameResult, len(names))
	parts := make([][][]graphKeyEdge, len(names))
	var tasks []graphBatchTask
	var plannedPairs uint64
	for nameIndex, p := range prepared {
		if p == nil {
			continue
		}
		results[nameIndex].CrossShard = p.CrossShard()
		for _, source := range p.sources {
			pairs := uint64(p.NumTargets(source.scored.Source))
			plannedPairs += pairs
			if !source.suppressed {
				stats.RetainedPairUpperBound += pairs
			}
		}
		ranges := candidateSourceBatches(p.NumSources(), p.NumDefinitions(), maxBatchEdges)
		parts[nameIndex] = make([][]graphKeyEdge, len(ranges))
		nameUpperBound := uint64(p.NumSources()) * uint64(p.NumDefinitions())
		if nameUpperBound > stats.HeaviestNameUpperBound {
			stats.HeaviestName = names[nameIndex]
			stats.HeaviestNameSources = p.NumSources()
			stats.HeaviestNameDefinitions = p.NumDefinitions()
			stats.HeaviestNameUpperBound = nameUpperBound
			stats.HeaviestNameBatches = len(ranges)
		}
		for batchIndex, bounds := range ranges {
			estimate := uint64(bounds[1]-bounds[0]) * uint64(p.NumDefinitions())
			stats.CandidateUpperBound += estimate
			if estimate > stats.LargestBatchUpperBound {
				stats.LargestBatchUpperBound = estimate
			}
			tasks = append(tasks, graphBatchTask{
				nameIndex: nameIndex, batchIndex: batchIndex,
				start: bounds[0], end: bounds[1], estimate: estimate,
			})
		}
	}
	stats.RegionCache = s.verifierRegions.Stats()
	stats.SourcesVerified = s.sourcesVerified.Load() - sourcesBefore
	stats.Batches = len(tasks)
	if stats.Workers > stats.Batches {
		stats.Workers = stats.Batches
	}
	if stats.HeaviestName != "" {
		log.Printf("server: graph schedule prepared %d names as %d bounded batches on %d workers; heaviest=%q sources=%d definitions=%d upper_bound=%d batches=%d prepare=%s",
			stats.Names, stats.Batches, stats.Workers, stats.HeaviestName,
			stats.HeaviestNameSources, stats.HeaviestNameDefinitions, stats.HeaviestNameUpperBound, stats.HeaviestNameBatches, stats.PreparationElapsed.Round(time.Millisecond))
		log.Printf("server: graph source verification complete: sources=%d candidate_pairs=%d retained_pair_upper_bound=%d (before repository filtering and dedup)", stats.SourcesVerified, plannedPairs, stats.RetainedPairUpperBound)
		log.Printf("server: graph region cache hits=%d misses=%d waits=%d evictions=%d entries=%d bytes=%d budget=%d", stats.RegionCache.Hits, stats.RegionCache.Misses, stats.RegionCache.Waits, stats.RegionCache.Evictions, stats.RegionCache.Entries, stats.RegionCache.Bytes, stats.RegionCache.MaxBytes)
	}

	// Longest batches enter the shared queue first. Stable identity tie-breaks
	// keep the schedule reproducible while result placement remains independent
	// of completion order.
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].estimate != tasks[j].estimate {
			return tasks[i].estimate > tasks[j].estimate
		}
		if tasks[i].nameIndex != tasks[j].nameIndex {
			return tasks[i].nameIndex < tasks[j].nameIndex
		}
		return tasks[i].batchIndex < tasks[j].batchIndex
	})
	computeStart := time.Now()
	if err := s.computeGraphBatchTasks(names, prepared, tasks, parts, generation, stats.Workers); err != nil {
		stats.CandidateComputeElapsed = time.Since(computeStart)
		return nil, stats, err
	}
	stats.CandidateComputeElapsed = time.Since(computeStart)
	stats.SourcesVerified = s.sourcesVerified.Load() - sourcesBefore
	stats.CandidatePairs = s.candidatePairs.Load() - pairsBefore
	stats.RetainedPairs = s.retainedPairs.Load() - retainedBefore
	log.Printf("server: graph fanout verified %d sources; %d candidate pairs, %d retained before dedup", stats.SourcesVerified, stats.CandidatePairs, stats.RetainedPairs)

	for nameIndex := range results {
		var total int
		for _, batch := range parts[nameIndex] {
			total += len(batch)
		}
		results[nameIndex].Edges = make([]graphKeyEdge, 0, total)
		for _, batch := range parts[nameIndex] {
			results[nameIndex].Edges = append(results[nameIndex].Edges, batch...)
		}
	}
	return results, stats, nil
}

func (s *graphSweep) computeGraphBatchTasks(names []string, prepared []*preparedGraphName, tasks []graphBatchTask, parts [][][]graphKeyEdge, generation uint64, workers int) error {
	if len(tasks) == 0 {
		return nil
	}
	if workers > len(tasks) {
		workers = len(tasks)
	}
	if workers < 1 {
		workers = 1
	}

	work := make(chan graphBatchTask, len(tasks))
	for _, task := range tasks {
		work <- task
	}
	close(work)

	var (
		wg        sync.WaitGroup
		failed    atomic.Bool
		completed atomic.Int64
		errOnce   sync.Once
		firstErr  error
		activeMu  sync.Mutex
		active    = make([]string, workers)
	)
	progressStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				activeMu.Lock()
				labels := make([]string, 0, len(active))
				for _, label := range active {
					if label != "" {
						labels = append(labels, label)
					}
				}
				activeMu.Unlock()
				if len(labels) > 6 {
					labels = labels[:6]
				}
				log.Printf("server: graph schedule progress %d/%d batches complete; active=%q",
					completed.Load(), len(tasks), labels)
			case <-progressStop:
				return
			}
		}
	}()
	wg.Add(workers)
	for workerID := range workers {
		go func(workerID int) {
			defer wg.Done()
			for task := range work {
				if failed.Load() {
					continue
				}
				name := names[task.nameIndex]
				label := fmt.Sprintf("%s sources[%d:%d]", name, task.start, task.end)
				activeMu.Lock()
				active[workerID] = label
				activeMu.Unlock()
				edges, err := runRecovered(label, func() ([]graphKeyEdge, error) {
					return s.computePreparedGraphRange(name, prepared[task.nameIndex], task.start, task.end, generation)
				})
				activeMu.Lock()
				active[workerID] = ""
				activeMu.Unlock()
				completed.Add(1)
				if err != nil {
					errOnce.Do(func() { firstErr = err })
					failed.Store(true)
					continue
				}
				parts[task.nameIndex][task.batchIndex] = edges
			}
		}(workerID)
	}
	wg.Wait()
	close(progressStop)
	return firstErr
}

// computeParallel fans fn out across up to workers goroutines, one call per
// entry of items, and returns the results in input order.
//
// Two failure modes are handled that a bare "drain a channel, spawn goroutines"
// pool does not:
//
//   - Panic recovery. fn's call graph (computeEdgesForName reaches every
//     language-specific extractor in the corpus) is not guarded by any
//     recover() anywhere else in that call graph, so a single panic — a nil
//     deref, an index-out-of-range — while processing one item among
//     potentially tens of thousands would otherwise crash the whole
//     moedex-index build/refresh process with no indication of which item
//     was being processed. Each worker recovers a panic from fn into an
//     error naming the offending item, mirroring the recover-per-unit-of-work
//     pattern already used at the MCP (mcp.handleSafe) and HTTP
//     (internal/app/servecmd's withRecover) layers.
//   - Fail-fast. Once any item reports an error (returned or recovered),
//     every worker stops STARTING new items and drains the rest of the work
//     queue without calling fn, instead of running every other
//     already-queued item to completion before the error is even inspected.
//     Items already in flight when the error is observed still run to
//     completion — fn takes no context/cancellation, so an in-flight call
//     cannot be interrupted mid-call — which bounds the wasted work to at
//     most `workers` items instead of the whole remaining backlog.
//
// On any error (returned or recovered), the results collected so far are
// discarded, matching the caller's existing all-or-nothing contract.
func computeParallel[T any](items []string, workers int, fn func(name string) (T, error)) ([]T, error) {
	n := len(items)
	if n == 0 {
		return nil, nil
	}
	if workers > n {
		workers = n
	}
	if workers < 1 {
		workers = 1
	}

	results := make([]T, n)

	work := make(chan int, n)
	for i := range n {
		work <- i
	}
	close(work)

	var (
		wg       sync.WaitGroup
		failed   atomic.Bool
		errOnce  sync.Once
		firstErr error
	)
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for i := range work {
				if failed.Load() {
					continue // an earlier item already failed; drain without computing
				}
				result, err := runRecovered(items[i], func() (T, error) { return fn(items[i]) })
				if err != nil {
					errOnce.Do(func() { firstErr = err })
					failed.Store(true)
					continue
				}
				results[i] = result
			}
		}()
	}
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

// runRecovered calls fn and converts any panic into an error naming item, so
// a panic anywhere in fn's call graph cannot crash the caller. The stack is
// printed immediately (mirrors mcp.handleSafe) so it is not lost even though
// the returned error is just a one-line summary.
func runRecovered[T any](item string, fn func() (T, error)) (result T, err error) {
	defer func() {
		if r := recover(); r != nil {
			debug.PrintStack()
			err = fmt.Errorf("server: panic computing graph edges for %q: %v", item, r)
		}
	}()
	return fn()
}

func (s *graphSweep) contentDelta(previous *diskgraph.Graph) (added []string, removed map[string]struct{}) {
	before := previous.CorpusEntrySet()
	now := make(map[string]struct{}, len(s.identity))
	for sha, token := range s.identity {
		now[token] = struct{}{}
		if _, known := before[token]; !known {
			added = append(added, sha)
		}
	}
	removed = make(map[string]struct{})
	for token := range before {
		if _, present := now[token]; !present {
			removed[rosterSHA(token)] = struct{}{}
		}
	}
	return added, removed
}

func (s *graphSweep) dirtyNames(previous *diskgraph.Graph, added []string, removed map[string]struct{}) map[string]struct{} {
	dirty := make(map[string]struct{})
	mark := func(name string) {
		if i, ok := s.eligible[name]; ok {
			dirty[s.names[i]] = struct{}{}
		}
	}
	for _, sha := range added {
		sites := s.sites[sha]
		if len(sites) == 0 {
			continue
		}
		if b := s.idxs[sites[0].shard].Blob(sites[0].blob); b != nil {
			candidates.EachIdentifier(b.Content, func(run []byte) bool {
				if i, ok := s.eligible[string(run)]; ok {
					dirty[s.names[i]] = struct{}{}
				}
				return true
			})
		}
		for _, site := range sites {
			six := s.merged.ShardIndex(site.shard)
			if six == nil {
				continue
			}
			for _, sym := range six.Symbols(site.blob) {
				mark(sym.Name)
			}
			for _, ref := range six.Refs(site.blob) {
				mark(ref.Name)
			}
		}
	}
	if len(removed) > 0 {
		checked := make(map[diskgraph.TargetSetID]bool)
		targetGone := make(map[diskgraph.TargetSetID]bool)
		previous.EachRecord(func(record diskgraph.PhysicalRecord) bool {
			if _, gone := removed[record.Source.BlobSHA]; gone {
				mark(record.Edge.Name)
				return true
			}
			if record.Factored {
				if !checked[record.Targets] {
					checked[record.Targets] = true
					previous.EachTarget(record.Targets, func(target diskgraph.Key) bool {
						if _, gone := removed[target.BlobSHA]; gone {
							targetGone[record.Targets] = true
							return false
						}
						return true
					})
				}
				if targetGone[record.Targets] {
					mark(record.Edge.Name)
				}
			} else if _, gone := removed[record.Edge.TargetBlob]; gone {
				mark(record.Edge.Name)
			}
			return true
		})
	}

	return dirty
}
