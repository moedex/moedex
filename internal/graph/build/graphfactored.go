package graphbuild

import (
	"fmt"
	"log"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"moedex/internal/graph"
	"moedex/internal/graph/candidates"
	"moedex/internal/graph/diskgraph"
)

type factoredTargets struct {
	keys           []diskgraph.Key
	sorted         []diskgraph.Key
	positions      map[diskgraph.Key]int
	physicalCounts []int
	physicalTotal  int
	firstSites     []candidates.Site
	firstPhysical  []int
	nextPhysical   []int
}

type factoredSource struct {
	source     diskgraph.Key
	prototype  diskgraph.Edge
	targets    *factoredTargets
	exclude    int
	rawEdges   int
	moveTarget int
	moveAfter  int
}

func (g factoredSource) count() int {
	n := len(g.targets.keys)
	if g.exclude >= 0 {
		n--
	}
	return n
}
func (g factoredSource) template() graphKeyEdge {
	return graphKeyEdge{Key: g.source, Edge: g.prototype}
}

type factoredNameResult struct {
	groups                                          []factoredSource
	crossShard                                      bool
	pairs, retained, rawSuppressed, crossSuppressed uint64
	targets                                         []*factoredTargets
}

// factorName replaces the Cartesian product with source templates and shared
// target sets. Source/target physical identities are used for self exclusion
// before content-addressed deduplication, matching the expanded emitter.
func (s *graphSweep) factorName(name string, p *preparedGraphName, generation uint64) (factoredNameResult, error) {
	out := factoredNameResult{}
	if p == nil || p.NumDefinitions() == 0 {
		return out, nil
	}
	out.crossShard = p.CrossShard()
	all := &factoredTargets{positions: make(map[diskgraph.Key]int)}
	ownCounts := make(map[candidates.Site]int, p.NumDefinitions())
	for i := 0; i < p.NumDefinitions(); i++ {
		site := p.DefinitionSite(i)
		blob := s.corpus.Blob(site)
		if blob == nil || site.Start < 0 {
			return out, fmt.Errorf("server: invalid graph target for %q", name)
		}
		key := diskgraph.Key{BlobSHA: blob.SHA, SymbolOffset: uint64(site.Start)}
		pos, ok := all.positions[key]
		if !ok {
			pos = len(all.keys)
			all.positions[key] = pos
			all.keys = append(all.keys, key)
			all.physicalCounts = append(all.physicalCounts, 0)
			first := site
			first.End = 0
			all.firstSites = append(all.firstSites, first)
			all.firstPhysical = append(all.firstPhysical, i)
			all.nextPhysical = append(all.nextPhysical, -1)
		} else {
			physical := site
			physical.End = 0
			if physical != all.firstSites[pos] && all.nextPhysical[pos] < 0 {
				all.nextPhysical[pos] = i
			}
		}
		all.physicalCounts[pos]++
		all.physicalTotal++
		site.End = 0
		ownCounts[site]++
	}
	out.targets = append(out.targets, all)
	repoTargets := make(map[string]*factoredTargets)
	type sourceIdentity struct {
		source    diskgraph.Key
		prototype diskgraph.Edge
		targets   *factoredTargets
		exclude   int
	}
	seen := make(map[sourceIdentity]int)
	for _, source := range p.sources {
		sc := source.scored
		pairs := p.NumTargets(sc.Source)
		out.pairs += uint64(pairs)
		if source.suppressed {
			out.rawSuppressed += uint64(pairs)
			continue
		}
		targets := all
		if sc.Confidence == graph.Pattern && len(s.reposBySHA[source.key.BlobSHA]) > 0 {
			signature := strings.Join(s.reposBySHA[source.key.BlobSHA], "\x00")
			targets = repoTargets[signature]
			if targets == nil {
				targets = &factoredTargets{positions: make(map[diskgraph.Key]int)}
				for i, key := range all.keys {
					if s.blobSHAsShareRepository(source.key.BlobSHA, key.BlobSHA) {
						targets.positions[key] = len(targets.keys)
						targets.keys = append(targets.keys, key)
						targets.physicalCounts = append(targets.physicalCounts, all.physicalCounts[i])
						targets.physicalTotal += all.physicalCounts[i]
					}
				}
				if len(targets.keys) == len(all.keys) {
					targets = all
				} else {
					out.targets = append(out.targets, targets)
				}
				repoTargets[signature] = targets
			}
		}
		physical := targets.physicalTotal
		exclude := -1
		moveTarget, moveAfter := -1, -1
		site := sc.Source
		site.End = 0
		own := ownCounts[site]
		if own > 0 {
			sourceBlob := sc.EvidenceBlob()
			key := diskgraph.Key{BlobSHA: sourceBlob.SHA, SymbolOffset: uint64(site.Start)}
			if pos, ok := targets.positions[key]; ok {
				physical -= own
				if targets.physicalCounts[pos] == own {
					exclude = pos
				} else {
					canonical := all.positions[key]
					if site == all.firstSites[canonical] && all.nextPhysical[canonical] >= 0 {
						next := all.nextPhysical[canonical]
						after := sort.Search(len(targets.keys), func(i int) bool { return all.firstPhysical[all.positions[targets.keys[i]]] > next }) - 1
						if after > pos {
							moveTarget, moveAfter = pos, after
						}
					}
				}
			}
		}
		out.retained += uint64(physical)
		if sc.Confidence == graph.Pattern {
			out.crossSuppressed += uint64(pairs - physical)
		}
		prototype := diskgraph.Edge{Type: source.typeID, Confidence: sc.Confidence, Evidence: sc.Evidence, Name: name, Generation: generation}
		group := factoredSource{source: source.key, prototype: prototype, targets: targets, exclude: exclude, rawEdges: physical, moveTarget: moveTarget, moveAfter: moveAfter}
		if group.count() == 0 {
			continue
		}
		identity := sourceIdentity{source: group.source, prototype: prototype, targets: targets, exclude: exclude}
		if previous, duplicate := seen[identity]; duplicate {
			out.groups[previous].rawEdges += physical
			continue
		}
		seen[identity] = len(out.groups)
		out.groups = append(out.groups, group)
	}
	// These build-only maps/counts are not needed by persistence or LSP readers.
	for _, targets := range out.targets {
		targets.positions = nil
		targets.physicalCounts = nil
		targets.firstSites = nil
		targets.firstPhysical = nil
		targets.nextPhysical = nil
	}
	return out, nil
}

func (s *graphSweep) computeFactoredNames(names []string, generation uint64) ([]factoredNameResult, GraphScheduleStats, error) {
	stats := GraphScheduleStats{Workers: runtime.GOMAXPROCS(0), Names: len(names)}
	before := s.sourcesVerified.Load()
	started := time.Now()
	if s.baseCorpus == nil {
		s.baseCorpus = s.corpus
	}
	s.corpus = s.baseCorpus
	log.Printf("server: graph occurrence scan started names=%d budget_bytes=%d", len(names), graphTextOccurrenceBytes)
	scan := time.Now()
	s.corpus, stats.TextOccurrences = candidates.WithTextOccurrences(s.baseCorpus, names, graphTextOccurrenceBytes)
	stats.TextScanElapsed = time.Since(scan)
	log.Printf("server: graph occurrence scan complete built=%t names=%d/%d blobs=%d content_bytes=%d occurrences=%d retained_bytes=%d budget_bytes=%d fallback=%q elapsed=%s", stats.TextOccurrences.Built, stats.TextOccurrences.IndexedNames, stats.TextOccurrences.RequestedNames, stats.TextOccurrences.BlobsScanned, stats.TextOccurrences.ContentBytes, stats.TextOccurrences.Occurrences, stats.TextOccurrences.RetainedBytes, stats.TextOccurrences.BudgetBytes, stats.TextOccurrences.FallbackReason, stats.TextScanElapsed.Round(time.Millisecond))
	var completed, lastLog atomic.Int64
	lastLog.Store(time.Now().UnixNano())
	type result struct {
		name                 factoredNameResult
		sources, definitions int
	}
	prepared, err := computeParallel(names, stats.Workers, func(name string) (result, error) {
		p, err := s.prepareGraphName(candidates.PrepareName(s.corpus, name))
		if err != nil {
			return result{}, err
		}
		factored, err := s.factorName(name, p, generation)
		count := completed.Add(1)
		now := time.Now().UnixNano()
		previous := lastLog.Load()
		if now-previous >= int64(30*time.Second) && lastLog.CompareAndSwap(previous, now) {
			log.Printf("server: graph compact preparation progress %d/%d names elapsed=%s", count, len(names), time.Since(started).Round(time.Second))
		}
		return result{name: factored, sources: p.NumSources(), definitions: p.NumDefinitions()}, err
	})
	stats.PreparationElapsed = time.Since(started)
	stats.RegionCache = s.verifierRegions.Stats()
	stats.SourcesVerified = s.sourcesVerified.Load() - before
	if err != nil {
		return nil, stats, err
	}
	out := make([]factoredNameResult, len(prepared))
	for i, p := range prepared {
		out[i] = p.name
		upper := uint64(p.sources) * uint64(p.definitions)
		stats.CandidateUpperBound += upper
		if upper > stats.HeaviestNameUpperBound {
			stats.HeaviestName = names[i]
			stats.HeaviestNameUpperBound = upper
			stats.HeaviestNameSources = p.sources
			stats.HeaviestNameDefinitions = p.definitions
		}
		stats.CandidatePairs += p.name.pairs
		stats.RetainedPairs += p.name.retained
		stats.PhysicalSources += uint64(len(p.name.groups))
		stats.SharedTargetSets += uint64(len(p.name.targets))
		for _, targets := range p.name.targets {
			stats.SharedTargets += uint64(len(targets.keys))
		}
		for _, group := range p.name.groups {
			stats.LogicalEdges += uint64(group.count())
		}
		s.suppressedRawCandidates.Add(int64(p.name.rawSuppressed))
		s.suppressedCrossRepoPatterns.Add(int64(p.name.crossSuppressed))
	}
	stats.RetainedPairUpperBound = stats.RetainedPairs
	log.Printf("server: graph compact census names=%d sources_verified=%d candidate_pairs=%d retained_before_dedup=%d physical_sources=%d target_sets=%d shared_targets=%d logical_edges=%d elapsed=%s", stats.Names, stats.SourcesVerified, stats.CandidatePairs, stats.RetainedPairs, stats.PhysicalSources, stats.SharedTargetSets, stats.SharedTargets, stats.LogicalEdges, stats.PreparationElapsed.Round(time.Millisecond))
	log.Printf("server: graph compact heaviest=%q sources=%d definitions=%d upper_bound=%d; region_cache_hits=%d misses=%d retained_bytes=%d", stats.HeaviestName, stats.HeaviestNameSources, stats.HeaviestNameDefinitions, stats.HeaviestNameUpperBound, stats.RegionCache.Hits, stats.RegionCache.Misses, stats.RegionCache.Bytes)
	return out, stats, nil
}

type factoredEmitter struct {
	builder *diskgraph.Builder
	sets    map[*factoredTargets]diskgraph.TargetSetID
}

func newFactoredEmitter(builder *diskgraph.Builder) *factoredEmitter {
	return &factoredEmitter{builder: builder, sets: make(map[*factoredTargets]diskgraph.TargetSetID)}
}
func (e *factoredEmitter) add(group factoredSource) error {
	id, ok := e.sets[group.targets]
	if !ok {
		var err error
		id, err = e.builder.AddTargetSet(group.targets.keys)
		if err != nil {
			return err
		}
		e.sets[group.targets] = id
	}
	return e.builder.AddFactoredSourceOrdered(group.source, group.prototype, id, group.exclude, group.moveTarget, group.moveAfter)
}

func (t *factoredTargets) sortedKeys() []diskgraph.Key {
	if t.sorted == nil {
		t.sorted = append([]diskgraph.Key(nil), t.keys...)
		sort.Slice(t.sorted, func(i, j int) bool { return graphKeyLess(t.sorted[i], t.sorted[j]) })
	}
	return t.sorted
}

func factoredPatternCallGroups(results []factoredNameResult) []patternCallGroup {
	byKey := make(map[patternCallSiteKey]*patternCallGroup)
	type selection struct {
		targets *factoredTargets
		exclude int
		merged  bool
	}
	targetIdentity := make(map[patternCallSiteKey]selection)
	for _, result := range results {
		for _, source := range result.groups {
			edge := source.prototype
			if edge.Type != diskgraph.EdgeCalls || edge.Confidence != graph.Pattern || edge.Name == "" {
				continue
			}
			key := patternCallSiteKey{sourceBlob: edge.Evidence.BlobSHA, evidence: edge.Evidence.ByteOffset, name: edge.Name}
			group := byKey[key]
			if group == nil {
				targets := source.targets.sortedKeys()
				if source.exclude >= 0 {
					excluded := source.targets.keys[source.exclude]
					filtered := make([]diskgraph.Key, 0, len(targets)-1)
					for _, target := range targets {
						if target != excluded {
							filtered = append(filtered, target)
						}
					}
					targets = filtered
				}
				group = &patternCallGroup{key: key, source: source.source, targets: targets}
				byKey[key] = group
				targetIdentity[key] = selection{targets: source.targets, exclude: source.exclude}
			} else if prior := targetIdentity[key]; prior.targets != source.targets || prior.exclude != source.exclude || prior.merged {
				// Different physical source contexts rarely require a union; keep the
				// common path as shared immutable target slices.
				union := make(map[diskgraph.Key]struct{}, len(group.targets))
				for _, target := range group.targets {
					union[target] = struct{}{}
				}
				for i, target := range source.targets.keys {
					if i != source.exclude {
						union[target] = struct{}{}
					}
				}
				group.targets = make([]diskgraph.Key, 0, len(union))
				for target := range union {
					group.targets = append(group.targets, target)
				}
				sort.Slice(group.targets, func(i, j int) bool { return graphKeyLess(group.targets[i], group.targets[j]) })
				targetIdentity[key] = selection{merged: true}
			}
			group.edgeCount += source.rawEdges
		}
	}
	out := make([]patternCallGroup, 0, len(byKey))
	for _, group := range byKey {
		out = append(out, *group)
	}
	sort.Slice(out, func(i, j int) bool { return patternCallSiteLess(out[i].key, out[j].key) })
	return out
}

// importedTargetSets reuses one target roster per persisted set during refresh.
type importedTargetSets struct {
	graph  *diskgraph.Graph
	sets   map[diskgraph.TargetSetID]*factoredTargets
	scopes map[string]*factoredTargets
}

func newImportedTargetSets(g *diskgraph.Graph) *importedTargetSets {
	return &importedTargetSets{graph: g, sets: make(map[diskgraph.TargetSetID]*factoredTargets), scopes: make(map[string]*factoredTargets)}
}
func (i *importedTargetSets) get(id diskgraph.TargetSetID) *factoredTargets {
	if found := i.sets[id]; found != nil {
		return found
	}
	t := &factoredTargets{positions: make(map[diskgraph.Key]int)}
	i.graph.EachTarget(id, func(key diskgraph.Key) bool {
		t.positions[key] = len(t.keys)
		t.keys = append(t.keys, key)
		return true
	})
	i.sets[id] = t
	return t
}
func (i *importedTargetSets) scoped(id diskgraph.TargetSetID, signature string, keep func(diskgraph.Key) bool) *factoredTargets {
	cacheKey := fmt.Sprintf("%d:%s", id, signature)
	if found := i.scopes[cacheKey]; found != nil {
		return found
	}
	original := i.get(id)
	var filtered *factoredTargets
	for n, key := range original.keys {
		if keep(key) {
			if filtered != nil {
				filtered.positions[key] = len(filtered.keys)
				filtered.keys = append(filtered.keys, key)
			}
			continue
		}
		if filtered == nil {
			filtered = &factoredTargets{keys: append([]diskgraph.Key(nil), original.keys[:n]...), positions: make(map[diskgraph.Key]int)}
			for j, key := range filtered.keys {
				filtered.positions[key] = j
			}
		}
	}
	if filtered == nil {
		filtered = original
	}
	i.scopes[cacheKey] = filtered
	return filtered
}
func (i *importedTargetSets) source(record diskgraph.PhysicalRecord, targets *factoredTargets) factoredSource {
	exclude := -1
	if record.Exclude >= 0 {
		if key, ok := i.graph.TargetAt(record.Targets, record.Exclude); ok {
			if position, ok := targets.positions[key]; ok {
				exclude = position
			}
		}
	}
	moveTarget, moveAfter := -1, -1
	if record.MoveTarget >= 0 {
		if key, ok := i.graph.TargetAt(record.Targets, record.MoveTarget); ok {
			if position, retained := targets.positions[key]; retained {
				original := i.get(record.Targets)
				after := sort.Search(len(targets.keys), func(n int) bool { return original.positions[targets.keys[n]] > record.MoveAfter }) - 1
				if after > position {
					moveTarget, moveAfter = position, after
				}
			}
		}
	}
	return factoredSource{source: record.Source, prototype: record.Edge, targets: targets, exclude: exclude, moveTarget: moveTarget, moveAfter: moveAfter}
}

type carriedGraphRecord struct {
	source diskgraph.Key
	edge   diskgraph.Edge
	group  *factoredSource
}

func (r carriedGraphRecord) count() int {
	if r.group != nil {
		return r.group.count()
	}
	return 1
}
func (r carriedGraphRecord) add(explicit *graphEmitter, compact *factoredEmitter) error {
	if r.group != nil {
		return compact.add(*r.group)
	}
	return explicit.Add(r.source, r.edge)
}
