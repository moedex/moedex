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
	"sync"

	"moedex/internal/graph/candidates"
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
	if len(added) == 0 && len(removed) == 0 {
		stats.Unchanged = true
		stats.Generation = previous.Generation()
		stats.NamesCarried = len(sweep.names)
		stats.EdgesCarried = previous.NumEdges()
		return GraphPath(dir), stats, nil
	}

	dirty := sweep.dirtyNames(previous, added, removed)
	stats.Generation = previous.Generation() + 1

	carried := make(map[string][]carriedGraphEdge)
	for node := 0; node < previous.NumNodes(); node++ {
		_, first, count, ok := previous.NodeAt(node)
		if !ok {
			break
		}
		for i := first; i < first+count; i++ {
			name := previous.EdgeName(i)
			if _, stale := dirty[name]; stale {
				continue
			}
			if _, ok := sweep.eligible[name]; !ok {
				stats.EdgesDropped++
				continue
			}
			carried[name] = append(carried[name], carriedGraphEdge{node: uint32(node), edge: uint32(i)})
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

	path, err = saveGraph(builder, dir)
	return path, stats, err
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

func (s *graphSweep) rebuildAll(dir string, stats *GraphRefreshStats) (string, error) {
	builder := diskgraph.NewBuilder()
	builder.SetGeneration(stats.Generation)
	s.recordCorpusRoster(builder)

	results, err := s.computeEdgesParallel(s.names, stats.Generation)
	if err != nil {
		return "", err
	}

	emit := newGraphEmitter(builder)
	for _, batch := range results {
		for i := range batch {
			if err := emit.Add(batch[i].Key, batch[i].Edge); err != nil {
				return "", err
			}
		}
	}

	stats.NamesRecomputed = len(s.names)
	stats.EdgesRecomputed = int(builder.NumEdges())
	stats.BlobsAdded = len(s.identity)
	return saveGraph(builder, dir)
}

// computeEdgesParallel fans out computeEdgesForName across GOMAXPROCS workers.
// Results are returned in the same order as names for deterministic output.
func (s *graphSweep) computeEdgesParallel(names []string, generation uint64) ([][]graphKeyEdge, error) {
	n := len(names)
	if n == 0 {
		return nil, nil
	}

	workers := runtime.GOMAXPROCS(0)
	if workers > n {
		workers = n
	}

	results := make([][]graphKeyEdge, n)
	errs := make([]error, n)

	work := make(chan int, n)
	for i := range n {
		work <- i
	}
	close(work)

	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for i := range work {
				results[i], errs[i] = s.computeEdgesForName(names[i], generation)
			}
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
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
