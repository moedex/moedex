package server

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
// Content is addressed by SHA, so "unchanged blob" means byte-identical content,
// which means identical occurrences and identical verification.
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
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"

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
	Cluster         cluster.BuildReport
	Counts          GraphBuildCounts
}

// RefreshGraph rebuilds dir's graph incrementally against the graph already
// there, recomputing only the names the content delta made stale. It falls back
// to a full build — and says so in the returned stats — whenever there is no
// usable prior graph.
//
// The result is identical to BuildGraph's for the same shard set, edge for edge
// and in the same order; only the per-edge generation stamps differ.
func RefreshGraph(dir string) (path string, stats GraphRefreshStats, err error) {
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
		path, err = sweep.rebuildAll(dir, &stats)
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
	legacyRawCandidates := sweep.hasRawCandidateSources(previous)
	if len(added) == 0 && len(removed) == 0 && !legacyRawCandidates {
		stats.Unchanged = true
		stats.Generation = previous.Generation()
		stats.NamesCarried = len(sweep.names)
		stats.EdgesCarried = previous.NumEdges()
		stats.Cluster, err = ensureClusterSidecar(dir, previous.Generation())
		if err == nil {
			stats.Counts, err = measureGraphBuildCounts(dir, 0)
		}
		return GraphPath(dir), stats, err
	}
	if legacyRawCandidates {
		stats.Reason = "legacy raw Candidate edges require cleanup"
	}

	dirty := sweep.dirtyNames(previous, added, removed)
	stats.Generation = previous.Generation() + 1

	carried := make(map[string][]carriedGraphEdge)
	var carriedUnnamed []carriedGraphEdge
	for node := 0; node < previous.NumNodes(); node++ {
		key, first, count, ok := previous.NodeAt(node)
		if !ok {
			break
		}
		for i := first; i < first+count; i++ {
			name := previous.EdgeName(i)
			if name != "" {
				// A per-name edge from the candidates.GenerateCandidates sweep:
				// carry it forward unless the delta marked its name dirty, or the
				// name no longer exists anywhere in the corpus to regenerate.
				if _, stale := dirty[name]; stale {
					continue
				}
				if _, ok := sweep.eligible[name]; !ok {
					stats.EdgesDropped++
					continue
				}
				edge, ok := previous.EdgeAt(i)
				if !ok {
					continue
				}
				if edge.Confidence == graph.Candidate && !sweep.sourceResolvesToSymbol(key) {
					stats.EdgesDropped++
					sweep.suppressedRawCandidates.Add(1)
					continue
				}
				carried[name] = append(carried[name], carriedGraphEdge{node: uint32(node), edge: uint32(i)})
				continue
			}
			// An edge from one of the whole-corpus passes (HTTP, manifest,
			// hierarchy, injection, queries, renders, LSP calls, similarity) —
			// none of these have a symbol name, so the per-name dirty/eligible
			// check above can never keep them. The six tag-independent passes
			// are always regenerated fresh below (see addWholeCorpusEdges), so
			// their previous copies are neither carried nor dropped here: they
			// are simply superseded. LSP-call and SIMILAR_TO edges need runtime
			// resources (a language-server pool, an embedder) this incremental
			// path does not have, so they are carried forward instead, but only
			// when neither endpoint's blob was removed by this delta — an
			// unmodified pair of blobs cannot have produced a different edge.
			edge, ok := previous.EdgeAt(i)
			if !ok {
				continue
			}
			if edge.Type != diskgraph.EdgeCalls && edge.Type != diskgraph.EdgeSimilarTo {
				continue
			}
			if _, gone := removed[key.BlobSHA]; gone {
				stats.EdgesDropped++
				continue
			}
			if _, gone := removed[edge.TargetBlob]; gone {
				stats.EdgesDropped++
				continue
			}
			carriedUnnamed = append(carriedUnnamed, carriedGraphEdge{node: uint32(node), edge: uint32(i)})
		}
	}

	var dirtyNames []string
	for _, name := range sweep.names {
		if _, stale := dirty[name]; stale {
			dirtyNames = append(dirtyNames, name)
		}
	}

	recomputed, err := sweep.computeEdgesParallel(dirtyNames, stats.Generation)
	if err != nil {
		return "", stats, err
	}
	recomputedByName := make(map[string][]graphKeyEdge, len(dirtyNames))
	for i, name := range dirtyNames {
		recomputedByName[name] = recomputed[i]
	}

	builder := diskgraph.NewBuilder()
	builder.SetGeneration(stats.Generation)
	sweep.recordCorpusRoster(builder)
	if err := sweep.addDefinitionNodes(builder); err != nil {
		return "", stats, err
	}
	emit := newGraphEmitter(builder)
	for _, name := range sweep.names {
		if edges, stale := recomputedByName[name]; stale {
			stats.NamesRecomputed++
			before := builder.NumEdges()
			for i := range edges {
				if err := emit.Add(edges[i].Key, edges[i].Edge); err != nil {
					return "", stats, err
				}
			}
			stats.EdgesRecomputed += int(builder.NumEdges() - before)
			continue
		}
		stats.NamesCarried++
		for _, c := range carried[name] {
			key, _, _, ok := previous.NodeAt(int(c.node))
			if !ok {
				return "", stats, fmt.Errorf("server: graph refresh lost node %d of the previous graph", c.node)
			}
			edge, ok := previous.EdgeAt(int(c.edge))
			if !ok {
				return "", stats, fmt.Errorf("server: graph refresh lost edge %d of the previous graph", c.edge)
			}
			if err := builder.AddEdge(key, edge); err != nil {
				return "", stats, err
			}
			stats.EdgesCarried++
		}
	}

	// Carry forward the LSP-call / SIMILAR_TO edges whose endpoints this delta
	// left untouched — see the loop above for why these two families alone are
	// carried rather than regenerated.
	for _, c := range carriedUnnamed {
		key, _, _, ok := previous.NodeAt(int(c.node))
		if !ok {
			return "", stats, fmt.Errorf("server: graph refresh lost node %d of the previous graph", c.node)
		}
		edge, ok := previous.EdgeAt(int(c.edge))
		if !ok {
			return "", stats, fmt.Errorf("server: graph refresh lost edge %d of the previous graph", c.edge)
		}
		if err := builder.AddEdge(key, edge); err != nil {
			return "", stats, err
		}
		stats.EdgesCarried++
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
		stats.Cluster, err = buildClusterSidecar(dir)
	}
	if err == nil {
		stats.Counts, err = measureGraphBuildCounts(dir, int(sweep.suppressedRawCandidates.Load()))
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

func (s *graphSweep) hasRawCandidateSources(previous *diskgraph.Graph) bool {
	resolved := make(map[diskgraph.Key]bool)
	checked := make(map[diskgraph.Key]bool)
	found := false
	previous.EachEdge(func(source diskgraph.Key, edge diskgraph.Edge) bool {
		if edge.Confidence != graph.Candidate {
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
func (s *graphSweep) rebuildAll(dir string, stats *GraphRefreshStats) (string, error) {
	builder := diskgraph.NewBuilder()
	builder.SetGeneration(stats.Generation)
	s.recordCorpusRoster(builder)
	emit := newGraphEmitter(builder)

	if _, err := s.buildAllEdges(builder, emit, stats.Generation, GraphBuildOptions{}); err != nil {
		return "", err
	}

	stats.NamesRecomputed = len(s.names)
	stats.EdgesRecomputed = int(builder.NumEdges())
	stats.BlobsAdded = len(s.identity)
	path, err := saveGraph(builder, dir)
	if err == nil {
		stats.Cluster, err = buildClusterSidecar(dir)
	}
	if err == nil {
		stats.Counts, err = measureGraphBuildCounts(dir, int(s.suppressedRawCandidates.Load()))
	}
	return path, err
}

// computeEdgesParallel fans out computeEdgesForName across GOMAXPROCS workers.
// Results are returned in the same order as names for deterministic output.
// See computeParallel for the panic-recovery and fail-fast behavior this
// relies on.
func (s *graphSweep) computeEdgesParallel(names []string, generation uint64) ([][]graphKeyEdge, error) {
	return computeParallel(names, runtime.GOMAXPROCS(0), func(name string) ([]graphKeyEdge, error) {
		return s.computeEdgesForName(name, generation)
	})
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
//     (cmd/moedex-serve's withRecover) layers.
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
		previous.EachEdge(func(key diskgraph.Key, edge diskgraph.Edge) bool {
			if _, gone := removed[key.BlobSHA]; gone {
				mark(edge.Name)
				return true
			}
			if _, gone := removed[edge.TargetBlob]; gone {
				mark(edge.Name)
			}
			return true
		})
	}
	return dirty
}
