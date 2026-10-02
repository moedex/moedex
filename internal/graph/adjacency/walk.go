// Package adjacency provides physical-record walks that avoid expanding shared
// graph target sets when only endpoint membership or aggregate counts are needed.
package adjacency

import (
	"sort"

	"moedex/internal/graph/diskgraph"
)

// TargetUnion records the union of targets used by source records. An index is
// absent from that union only when every source excludes that same index.
type TargetUnion map[diskgraph.TargetSetID]int

func (u TargetUnion) Add(set diskgraph.TargetSetID, exclude int) {
	if old, exists := u[set]; !exists {
		u[set] = exclude
	} else if old != exclude {
		u[set] = -1
	}
}

func (u TargetUnion) Each(g *diskgraph.Graph, fn func(diskgraph.Key) bool) bool {
	ids := make([]diskgraph.TargetSetID, 0, len(u))
	for id := range u {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		for i := 0; i < g.TargetCount(id); i++ {
			if i == u[id] {
				continue
			}
			key, ok := g.TargetAt(id, i)
			if ok && !fn(key) {
				return false
			}
		}
	}
	return true
}

// RecordCount is the logical edge count represented by a physical record.
func RecordCount(g *diskgraph.Graph, r diskgraph.PhysicalRecord) int {
	if !r.Factored {
		return 1
	}
	n := g.TargetCount(r.Targets)
	if r.Exclude >= 0 {
		n--
	}
	return n
}

// EachEndpoint visits source and target endpoints with their edge type. It may
// repeat an endpoint across source records or types, but shared targets are
// visited only once per set and type, never once per source/target pair.
func EachEndpoint(g *diskgraph.Graph, fn func(diskgraph.Key, diskgraph.EdgeType, bool) bool) {
	unions := make(map[diskgraph.EdgeType]TargetUnion)
	stopped := false
	g.EachRecord(func(r diskgraph.PhysicalRecord) bool {
		if RecordCount(g, r) == 0 {
			return true
		}
		if !fn(r.Source, r.Edge.Type, true) {
			stopped = true
			return false
		}
		if r.Factored {
			if unions[r.Edge.Type] == nil {
				unions[r.Edge.Type] = make(TargetUnion)
			}
			unions[r.Edge.Type].Add(r.Targets, r.Exclude)
			return true
		}
		stopped = !fn(diskgraph.Key{BlobSHA: r.Edge.TargetBlob, SymbolOffset: r.Edge.TargetOffset}, r.Edge.Type, false)
		return !stopped
	})
	if stopped {
		return
	}
	types := make([]diskgraph.EdgeType, 0, len(unions))
	for typ := range unions {
		types = append(types, typ)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	for _, typ := range types {
		if !unions[typ].Each(g, func(key diskgraph.Key) bool { return fn(key, typ, false) }) {
			return
		}
	}
}
