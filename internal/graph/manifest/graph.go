package manifest

import "sort"

// Graph is the repo-level view of a resolved edge set: which repos depend on
// which, and on the strength of which declarations.
//
// It is a small in-memory index, not a persisted one. The graph adjacency file stores
// these edges content-addressed by blob SHA, which is the right key for a corpus
// where identical manifests are one piece of content; the repo-level rollup is
// derived, cheap to rebuild, and is what an operator or an MCP tool actually asks
// for ("what depends on this service?").
type Graph struct {
	all  []Edge
	out  map[string][]string
	in   map[string][]string
	pair map[[2]string][]Edge
}

// NewGraph rolls edges up by repo. The returned graph does not alias the caller's
// slice ordering assumptions: every accessor returns sorted, deduplicated results.
func NewGraph(edges []Edge) *Graph {
	g := &Graph{
		all:  edges,
		out:  make(map[string][]string),
		in:   make(map[string][]string),
		pair: make(map[[2]string][]Edge),
	}
	for _, edge := range edges {
		from, to := edge.Source.Repo, edge.Target.Repo
		if from == "" || to == "" || from == to {
			continue
		}
		if !contains(g.out[from], to) {
			g.out[from] = append(g.out[from], to)
		}
		if !contains(g.in[to], from) {
			g.in[to] = append(g.in[to], from)
		}
		key := [2]string{from, to}
		g.pair[key] = append(g.pair[key], edge)
	}
	for _, adjacency := range g.out {
		sort.Strings(adjacency)
	}
	for _, adjacency := range g.in {
		sort.Strings(adjacency)
	}
	return g
}

// DependsOn returns the repos repo declares a dependency on, sorted.
func (g *Graph) DependsOn(repo string) []string {
	if g == nil {
		return nil
	}
	return append([]string(nil), g.out[repo]...)
}

// Dependents returns the repos that declare a dependency on repo, sorted — the
// blast radius of a change to it.
func (g *Graph) Dependents(repo string) []string {
	if g == nil {
		return nil
	}
	return append([]string(nil), g.in[repo]...)
}

// EdgesBetween returns the declarations that justify the from → to dependency, in
// the order Resolve emitted them.
func (g *Graph) EdgesBetween(from, to string) []Edge {
	if g == nil {
		return nil
	}
	return append([]Edge(nil), g.pair[[2]string{from, to}]...)
}

// Repos returns every repo that appears at either end of an edge, sorted.
func (g *Graph) Repos() []string {
	if g == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(g.out)+len(g.in))
	for repo := range g.out {
		seen[repo] = struct{}{}
	}
	for repo := range g.in {
		seen[repo] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for repo := range seen {
		out = append(out, repo)
	}
	sort.Strings(out)
	return out
}

// Edges returns every edge in the graph.
func (g *Graph) Edges() []Edge {
	if g == nil {
		return nil
	}
	return append([]Edge(nil), g.all...)
}

// NumEdges reports how many edges the graph holds.
func (g *Graph) NumEdges() int {
	if g == nil {
		return 0
	}
	return len(g.all)
}
