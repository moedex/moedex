package graphserve

// graphtools.go is the online graph-query layer. The adjacency data remains in
// diskgraph's read-only mmap; this file builds only a compact symbol/location
// catalog for rendering results. Reverse traversals scan the mmap on demand, so
// serving the tools does not duplicate the complete edge set on the Go heap.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"moedex/internal/graph"
	"moedex/internal/graph/adjacency"
	"moedex/internal/graph/cluster"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
	"moedex/internal/mcp"
)

const (
	defaultTraceDepth  = 1
	defaultImpactDepth = 3
	maxGraphDepth      = 10
	maxGraphInputBytes = 4 << 10
)

// GraphToolset owns one hot-swappable mmap graph generation and exposes its
// three MCP handlers. It is safe for concurrent calls and reloads.
type GraphToolset struct {
	mu  sync.Mutex
	cur *graphSnapshot
}

type graphSnapshot struct {
	graph   *diskgraph.Graph
	symbols *SymbolCorpus
	wg      sync.WaitGroup

	// Snapshot identity is captured when the graph mmap opens and remains bound
	// to it across concurrent calls. The corpus fingerprint uses the same
	// content-true shard identity as rank sidecars; buildID identifies the exact
	// graph artifact rather than merely its refresh generation.
	corpusFingerprint string
	buildID           string

	nodes    map[diskgraph.Key]nodeMetadata
	bySymbol map[string][]diskgraph.Key
	blobs    map[string][]*index.Blob
	// byPath resolves a FILE to the graph nodes located in it, keyed by every
	// normalized spelling of that file's path (absolute, repo/rel, and rel). It is
	// what lets a search_context block — which knows only a path and a line range —
	// be anchored to graph nodes without a reverse scan. See anchorsFor.
	byPath map[string][]locatedNode

	// Lazy indices for discovery tools (built on first access via sync.Once).
	repoOnce   sync.Once
	repoFiles  map[string]map[string]*index.Blob // repo → relpath → blob
	schemaOnce sync.Once
	schemaInfo *discoverySchema

	clusters   *cluster.Sidecar
	clusterErr error
}

// locatedNode is one graph node placed at a 1-based line of a file.
type locatedNode struct {
	line int
	key  diskgraph.Key
}

type nodeMetadata struct {
	Symbol    string
	Kind      string
	Locations []GraphLocation
}

// GraphLocation is one source location for a content-addressed graph node.
// Identical content can have several locations across repos.
type GraphLocation struct {
	Repo    string `json:"repo,omitempty"`
	Path    string `json:"path,omitempty"`
	AbsPath string `json:"abs_path,omitempty"`
	Line    int    `json:"line,omitempty"`
	BlobSHA string `json:"blob_sha,omitempty"`
}

// GraphNode is the structured MCP representation of a graph node. Query roots
// are Proven; traversed nodes carry the strongest path's weakest edge tier.
type GraphNode struct {
	ID           string           `json:"id"`
	Symbol       string           `json:"symbol,omitempty"`
	Kind         string           `json:"kind,omitempty"`
	BlobSHA      string           `json:"blob_sha"`
	SymbolOffset uint64           `json:"symbol_offset"`
	Confidence   graph.Confidence `json:"confidence"`
	Hops         int              `json:"hops"`
	Locations    []GraphLocation  `json:"locations"`
}

// GraphEdge is one directed dependency -> definition relationship returned by
// a graph MCP query.
type GraphEdge struct {
	Source     string           `json:"source"`
	Target     string           `json:"target"`
	Type       string           `json:"type"`
	Confidence graph.Confidence `json:"confidence"`
	Evidence   graph.Evidence   `json:"evidence"`
	Similarity float64          `json:"similarity,omitempty"`
}

// GraphQueryResult is the common structured payload returned by every graph
// tool. Relationship-specific meaning is carried by Tool and edge Type.
type GraphQueryResult struct {
	Tool           string      `json:"tool"`
	Query          string      `json:"query"`
	Repo           string      `json:"repo,omitempty"`
	Path           string      `json:"path,omitempty"`
	Depth          int         `json:"depth"`
	Nodes          []GraphNode `json:"nodes"`
	Edges          []GraphEdge `json:"edges"`
	Truncated      bool        `json:"truncated"`
	TotalIsExact   bool        `json:"total_is_exact"`
	ExpansionLimit int         `json:"expansion_limit,omitempty"`
}

type graphRelation struct {
	Source     diskgraph.Key
	Target     diskgraph.Key
	Type       diskgraph.EdgeType
	Confidence graph.ConfidenceTier
	Evidence   graph.Evidence
	Similarity float64
}

func (r graphRelation) weight() float64 {
	if r.Type == diskgraph.EdgeSimilarTo {
		return r.Similarity
	}
	return r.Confidence.Score()
}

// OpenGraphTools opens dir's graph adjacency file with mmap and its symbol/location
// resolver. The caller must Close the returned toolset after the MCP server has
// stopped accepting calls.
//
// A missing graph uses an immutable source-only snapshot when the source shards
// can be opened. An unopenable graph otherwise preserves legacy best-effort
// startup with no active generation. Owners requiring corrupt-artifact rejection
// should use OpenGraphToolsStrict after checking for an explicitly absent graph.
func OpenGraphTools(dir string) (*GraphToolset, error) {
	if _, err := os.Stat(GraphPath(dir)); errors.Is(err, os.ErrNotExist) {
		if source, sourceErr := OpenSourceTools(dir); sourceErr == nil {
			return source, nil
		}
	}
	snap, err := openGraphSnapshot(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "server: open graph (serving without graph tools/annotations until the next reload): %v\n", err)
		return &GraphToolset{}, nil
	}
	return &GraphToolset{cur: snap}, nil
}

// OpenGraphToolsStrict opens an unpublished graph generation without degrading
// to an absent graph. A serving owner can use it to prepare a complete rank/graph
// pair and retain its previous generation when either component fails to open.
func OpenGraphToolsStrict(dir string) (*GraphToolset, error) {
	snap, err := openGraphSnapshot(dir)
	if err != nil {
		return nil, err
	}
	return &GraphToolset{cur: snap}, nil
}

// OpenSourceTools opens immutable source and symbol discovery without requiring
// an optional graph sidecar. Graph traversal remains explicitly unavailable.
func OpenSourceTools(dir string) (*GraphToolset, error) {
	syms, err := OpenSymbols(dir)
	if err != nil {
		return nil, err
	}
	paths, err := globShards(dir)
	if err != nil {
		_ = syms.Close()
		return nil, err
	}
	s := &graphSnapshot{symbols: syms, corpusFingerprint: corpusFingerprint(paths),
		nodes: make(map[diskgraph.Key]nodeMetadata), bySymbol: make(map[string][]diskgraph.Key),
		blobs: make(map[string][]*index.Blob), byPath: make(map[string][]locatedNode)}
	s.buildCatalog()
	return &GraphToolset{cur: s}, nil
}

func openGraphSnapshot(dir string) (*graphSnapshot, error) {
	return openGraphSnapshotWithClusters(dir, true)
}

func openGraphSnapshotWithClusters(dir string, loadClusters bool) (*graphSnapshot, error) {
	started := time.Now()
	g, err := diskgraph.Open(GraphPath(dir))
	if err != nil {
		return nil, fmt.Errorf("server: open graph: %w", err)
	}
	validated := time.Now()
	syms, err := OpenSymbols(dir)
	if err != nil {
		_ = g.Close()
		return nil, fmt.Errorf("server: open graph symbols: %w", err)
	}
	symbolsReady := time.Now()
	shardPaths, err := globShards(dir)
	if err != nil {
		_ = syms.Close()
		_ = g.Close()
		return nil, fmt.Errorf("server: fingerprint graph corpus: %w", err)
	}
	buildID := g.BuildID()
	fingerprint := corpusFingerprint(shardPaths)
	identityReady := time.Now()
	s := &graphSnapshot{
		graph:             g,
		symbols:           syms,
		corpusFingerprint: fingerprint,
		buildID:           buildID,
		nodes:             make(map[diskgraph.Key]nodeMetadata),
		bySymbol:          make(map[string][]diskgraph.Key),
		blobs:             make(map[string][]*index.Blob),
		byPath:            make(map[string][]locatedNode),
	}
	catalogCached := s.buildCatalogCached(dir)
	catalogReady := time.Now()
	if loadClusters {
		s.clusters, s.clusterErr = cluster.Load(ClusterPath(dir), g.Generation())
	}
	log.Printf("server: graph open validation=%s symbols=%s identity=%s catalog=%s catalog_cached=%t total=%s nodes=%d records=%d logical_edges=%d", validated.Sub(started).Round(time.Millisecond), symbolsReady.Sub(validated).Round(time.Millisecond), identityReady.Sub(symbolsReady).Round(time.Millisecond), catalogReady.Sub(identityReady).Round(time.Millisecond), catalogCached, time.Since(started).Round(time.Millisecond), len(s.nodes), g.NumRecords(), g.NumEdges())
	return s, nil
}

func (s *graphSnapshot) buildCatalog() {
	seenSymbols := make(map[string]map[diskgraph.Key]bool)
	merged := s.symbols.Merged()
	for shard := 0; shard < s.symbols.NumShards(); shard++ {
		for id := uint64(0); id < uint64(s.symbols.NumShardBlobs(shard)); id++ {
			blob := s.symbols.ShardBlob(shard, id)
			if blob == nil || blob.SHA == "" {
				continue
			}
			s.blobs[blob.SHA] = append(s.blobs[blob.SHA], blob)
			for _, sym := range merged.Symbols(shard, id) {
				if sym.NameStart < 0 {
					continue
				}
				key := diskgraph.Key{BlobSHA: blob.SHA, SymbolOffset: uint64(sym.NameStart)}
				meta := s.nodes[key]
				if meta.Symbol == "" {
					meta.Symbol = sym.Name
					meta.Kind = sym.Kind.String()
				}
				meta.Locations = mergeLocations(meta.Locations, locationsFor(blob, sym.NameStart))
				s.nodes[key] = meta
				if seenSymbols[sym.Name] == nil {
					seenSymbols[sym.Name] = make(map[diskgraph.Key]bool)
				}
				if !seenSymbols[sym.Name][key] {
					seenSymbols[sym.Name][key] = true
					s.bySymbol[sym.Name] = append(s.bySymbol[sym.Name], key)
				}
			}
		}
	}

	if s.graph != nil {
		// A graph source can be a raw evidence occurrence when no enclosing symbol
		// exists. Keep those nodes addressable and attributable to their blob.
		for _, key := range s.graph.Keys() {
			if _, ok := s.nodes[key]; ok {
				continue
			}
			s.nodes[key] = nodeMetadata{Locations: s.locationsForKey(key)}
		}
		// An edge's TARGET need not itself be the SOURCE of any edge (e.g. an
		// HTTP_CALLS handler with no enclosing named symbol falls back to its own
		// raw byte offset -- see httpgraph.go's httpNodeOffset), so it can be
		// entirely absent from s.graph.Keys() (which enumerates sources only).
		// Fold every edge target in too, so it is addressable -- and locatable via
		// rootsForFile -- the same way a source-only raw-evidence node already is.
		adjacency.EachEndpoint(s.graph, func(key diskgraph.Key, typ diskgraph.EdgeType, source bool) bool {
			if _, ok := s.nodes[key]; !ok {
				s.nodes[key] = nodeMetadata{Locations: s.locationsForKey(key)}
			}
			kind := "Occurrence"
			switch typ {
			case diskgraph.EdgeDependsOn:
				kind = "File"
			case diskgraph.EdgeHTTPCalls:
				if !source {
					kind = "Route"
				}
			case diskgraph.EdgeUnknown:
				return true
			}
			s.assignRawKind(key, kind)
			return true
		})

	}
	for name := range s.bySymbol {
		sort.Slice(s.bySymbol[name], func(i, j int) bool { return lessGraphKeyID(s.bySymbol[name][i], s.bySymbol[name][j]) })
	}
	// Source discovery uses blob paths and symbol locations directly. The
	// graph traversal path index is unnecessary for a source-only generation.
	if s.graph != nil {
		s.buildPathIndex()
	}
}

func (s *graphSnapshot) assignRawKind(key diskgraph.Key, kind string) {
	meta := s.nodes[key]
	if meta.Symbol != "" {
		return
	}
	priority := map[string]int{"": 0, "Occurrence": 1, "Route": 2, "File": 3}
	if priority[kind] > priority[meta.Kind] {
		meta.Kind = kind
		s.nodes[key] = meta
	}
}

// buildPathIndex inverts the node catalog's locations into a file -> nodes
// lookup. Every node is filed under each spelling of its location's path, so a
// caller holding any one of them (a context block reports an absolute path, the
// graph tools accept repo-relative or bare suffixes) resolves without a scan.
func (s *graphSnapshot) buildPathIndex() {
	started := time.Now()
	// Path spellings depend on file identity, not line or node. A large source
	// file can contribute thousands of locations with the same spellings.
	type fileLocation struct{ repo, path, abs string }
	spellings := make(map[fileLocation][]string)
	for key, meta := range s.nodes {
		locations := meta.Locations
		if locations == nil {
			locations = s.locationsForKey(key)
		}
		for _, loc := range locations {
			file := fileLocation{loc.Repo, loc.Path, loc.AbsPath}
			paths, ok := spellings[file]
			if !ok {
				paths = indexedPathSpellings(loc)
				spellings[file] = paths
			}
			for _, spelling := range paths {
				s.byPath[spelling] = append(s.byPath[spelling], locatedNode{line: loc.Line, key: key})
			}
		}
	}
	for path := range s.byPath {
		nodes := s.byPath[path]
		sort.Slice(nodes, func(i, j int) bool {
			if nodes[i].line != nodes[j].line {
				return nodes[i].line < nodes[j].line
			}
			return lessGraphKeyID(nodes[i].key, nodes[j].key)
		})
	}
	log.Printf("server: graph path index elapsed=%s paths=%d", time.Since(started).Round(time.Millisecond), len(s.byPath))
}

// indexedPathSpellings is the subset of pathSpellings that buildPathIndex files a
// node under. The BARE relative spelling is deliberately dropped: byPath costs one
// entry per node per spelling across the whole corpus, and every context block
// carries the repo and absolute path of the file it came from, so the ambiguous
// bare spelling would buy nothing for the memory. A location with no repo still
// lands under its bare path, because repo-qualifying an empty repo IS that path.
func indexedPathSpellings(loc GraphLocation) []string {
	all := pathSpellings(loc)
	if len(all) > 2 {
		return all[:2]
	}
	return all
}

// pathSpellings returns the distinct normalized ways a location's file can be
// named, most specific first: absolute, repo-qualified, then bare relative.
func pathSpellings(loc GraphLocation) []string {
	var out []string
	for _, raw := range []string{loc.AbsPath, filepath.Join(loc.Repo, loc.Path), loc.Path} {
		if raw == "" {
			continue
		}
		clean := cleanGraphPath(raw)
		if clean == "" || clean == "." {
			continue
		}
		if !contains(out, clean) {
			out = append(out, clean)
		}
	}
	return out
}

func contains(list []string, want string) bool {
	for _, have := range list {
		if have == want {
			return true
		}
	}
	return false
}

func locationsFor(blob *index.Blob, off int) []GraphLocation {
	if blob == nil {
		return nil
	}
	line := blob.LineOf(off)
	out := make([]GraphLocation, 0, len(blob.Files))
	for _, file := range blob.Files {
		out = append(out, GraphLocation{Repo: file.Repo, Path: file.RelPath, AbsPath: file.AbsPath, Line: line, BlobSHA: blob.SHA})
	}
	return out
}

func mergeLocations(left, right []GraphLocation) []GraphLocation {
	// Most catalog entries have one file location. Retain the fresh-slice
	// ownership contract without allocating a dedup map or invoking a sort.
	if len(left) == 0 && len(right) == 1 {
		return []GraphLocation{right[0]}
	}
	if len(left) == 1 && len(right) == 0 {
		return []GraphLocation{left[0]}
	}
	seen := make(map[string]bool, len(left)+len(right))
	out := make([]GraphLocation, 0, len(left)+len(right))
	for _, locs := range [][]GraphLocation{left, right} {
		for _, loc := range locs {
			id := loc.Repo + "\x00" + loc.Path + "\x00" + loc.AbsPath + "\x00" + strconv.Itoa(loc.Line)
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, loc)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].AbsPath != out[j].AbsPath {
			return out[i].AbsPath < out[j].AbsPath
		}
		return out[i].Line < out[j].Line
	})
	return out
}

func (s *graphSnapshot) locationsForKey(key diskgraph.Key) []GraphLocation {
	var out []GraphLocation
	for _, blob := range s.blobs[key.BlobSHA] {
		off := int(key.SymbolOffset)
		if key.SymbolOffset > uint64(len(blob.Content)) {
			off = len(blob.Content)
		}
		out = mergeLocations(out, locationsFor(blob, off))
	}
	return out
}

// Tools returns the graph handlers in stable tools/list order.
func (g *GraphToolset) Tools() []mcp.ToolHandler {
	return []mcp.ToolHandler{
		&graphTool{owner: g, name: "trace_calls"},
		&graphTool{owner: g, name: "trace_consumers"},
		&graphTool{owner: g, name: "trace_hierarchy"},
		&graphTool{owner: g, name: "trace_queries"},
		&graphTool{owner: g, name: "trace_renders"},
		&graphTool{owner: g, name: "impact_analysis"},
		g.ClusterTool(),
		&discoveryTool{owner: g, name: "list_repos"},
		&discoveryTool{owner: g, name: "graph_schema"},
		&discoveryTool{owner: g, name: "read_source"},
		&discoveryTool{owner: g, name: "graph_neighbors"},
		&discoveryTool{owner: g, name: "list_symbols"},
		&discoveryTool{owner: g, name: "file_tree"},
	}
}

// Reload atomically installs a freshly mmap'd graph generation. Existing calls
// drain against the old generation before its mappings are released.
//
// The two return values name independent failure sources; callers must not
// conflate them. openErr means the NEW generation failed to build, so the OLD
// generation is still installed and being served -- the reload genuinely did
// not take effect. closeErr means the swap to the new generation already
// SUCCEEDED (it is what every call now sees) and only releasing the PREVIOUS
// generation's mmaps/handles afterward failed -- a resource leak on the
// retired generation, not a staleness problem for callers.
func (g *GraphToolset) Reload(dir string) (openErr, closeErr error) {
	next, err := openGraphSnapshot(dir)
	if err != nil {
		return err, nil
	}
	g.mu.Lock()
	old := g.cur
	g.cur = next
	g.mu.Unlock()
	if old != nil {
		old.wg.Wait()
		return nil, old.close()
	}
	return nil, nil
}

func (g *GraphToolset) acquire() *graphSnapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cur == nil {
		return nil
	}
	g.cur.wg.Add(1)
	return g.cur
}

// Available reports whether the toolset currently owns a queryable graph
// generation. It is intended for human-facing degradation messages; callers
// must still use the normal query methods, which acquire their own snapshot.
func (g *GraphToolset) Available() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cur != nil && g.cur.graph != nil
}

// CorpusFingerprint returns the identity captured by the current graph
// or source-only generation. Serving owners use this only
// while preparing an unpublished toolset, which they never reload independently.
func (g *GraphToolset) CorpusFingerprint() string {
	if g == nil {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cur == nil {
		return ""
	}
	return g.cur.corpusFingerprint
}

// Close waits for in-flight queries and releases the graph and content mmaps.
func (g *GraphToolset) Close() error {
	g.mu.Lock()
	old := g.cur
	g.cur = nil
	g.mu.Unlock()
	if old == nil {
		return nil
	}
	old.wg.Wait()
	return old.close()
}

func (s *graphSnapshot) close() error {
	var graphErr error
	if s.graph != nil {
		graphErr = s.graph.Close()
	}
	return errors.Join(graphErr, s.symbols.Close())
}

type graphTool struct {
	owner *GraphToolset
	name  string
}

func (t *graphTool) Name() string { return t.name }

func (t *graphTool) Specification() mcp.ToolSpecification {
	return specificationForDescriptor(t.Descriptor())
}

func specificationForDescriptor(desc map[string]interface{}) mcp.ToolSpecification {
	return mcp.NewToolSpecification(
		desc["name"].(string),
		desc["description"].(string),
		desc["inputSchema"].(map[string]interface{}),
	)
}

func (t *graphTool) Descriptor() map[string]interface{} {
	depth := map[string]interface{}{
		"type":        "integer",
		"minimum":     1,
		"maximum":     maxGraphDepth,
		"description": "Maximum graph traversal depth.",
	}
	schema := map[string]interface{}{
		"type":                 "object",
		"additionalProperties": false,
	}
	description := ""
	switch t.name {
	case "trace_calls":
		description = "Trace callers and callees of a symbol through confidence-scored call edges from the mmap graph. Optional repo/path select starting declarations; traversal can cross repositories."
		schema["properties"] = map[string]interface{}{
			"symbol": map[string]interface{}{"type": "string", "minLength": 1, "description": "Exact symbol name to trace."},
			"repo":   map[string]interface{}{"type": "string", "minLength": 1, "description": "Exact indexed repository for starting declarations; omitted searches all repositories."},
			"path":   map[string]interface{}{"type": "string", "minLength": 1, "description": "Exact repository-relative file for starting declarations. Requires repo; no suffix matching."},
			"hops":   depth,
		}
		schema["required"] = []string{"symbol"}
	case "trace_consumers":
		description = "Return every publisher and consumer connected to an event or queue name in the mmap graph."
		schema["properties"] = map[string]interface{}{
			"name": map[string]interface{}{"type": "string", "minLength": 1, "description": "Exact event or queue symbol name."},
		}
		schema["required"] = []string{"name"}
	case "trace_hierarchy":
		description = "Return the type hierarchy for a type symbol: supertypes (extends/implements), subtypes, contained methods, and DI injection bindings."
		schema["properties"] = map[string]interface{}{
			"symbol": map[string]interface{}{"type": "string", "minLength": 1, "description": "Exact type name to trace."},
			"hops":   depth,
		}
		schema["required"] = []string{"symbol"}
	case "trace_queries":
		description = "Return the EF/database query relationships for a symbol: what entities a method queries and what code touches an entity."
		schema["properties"] = map[string]interface{}{
			"symbol": map[string]interface{}{"type": "string", "minLength": 1, "description": "Exact symbol name (method or entity type) to trace."},
			"hops":   depth,
		}
		schema["required"] = []string{"symbol"}
	case "trace_renders":
		description = "Return the component rendering relationships for a symbol: what child components a parent renders and what parents render a child."
		schema["properties"] = map[string]interface{}{
			"symbol": map[string]interface{}{"type": "string", "minLength": 1, "description": "Exact component name to trace."},
			"hops":   depth,
		}
		schema["required"] = []string{"symbol"}
	case "impact_analysis":
		description = "Return the transitive closure of graph dependents for exactly one file or symbol, up to the requested depth."
		schema["properties"] = map[string]interface{}{
			"file":   map[string]interface{}{"type": "string", "minLength": 1, "description": "Absolute, repo-relative, or suffix file path. Supply exactly one of file or symbol."},
			"symbol": map[string]interface{}{"type": "string", "minLength": 1, "description": "Exact symbol name. Supply exactly one of file or symbol."},
			"depth":  depth,
		}
	}
	if properties, ok := schema["properties"].(map[string]interface{}); ok {
		properties["min_confidence"] = map[string]interface{}{
			"type": "string", "enum": []string{"Candidate", "Pattern", "Verified", "Proven"},
			"description": "Minimum edge confidence included before traversal (default Pattern).",
		}
	}
	return map[string]interface{}{
		"name":        t.name,
		"description": description,
		"inputSchema": schema,
	}
}

func (t *graphTool) Call(ctx context.Context, raw json.RawMessage) (map[string]interface{}, error) {
	snap := t.owner.acquire()
	if snap == nil {
		return mcp.TextResult("graph tools are closed", true), nil
	}
	defer snap.wg.Done()
	if snap.graph == nil {
		return snap.errorResult("graph_unavailable", "graph sidecar is absent; source discovery remains available"), nil
	}

	var (
		result        GraphQueryResult
		err           error
		minConfidence = graph.DefaultMinConfidence
	)
	switch t.name {
	case "trace_calls":
		var args struct {
			Symbol        string  `json:"symbol"`
			Repo          *string `json:"repo"`
			Path          *string `json:"path"`
			Hops          *int    `json:"hops"`
			MinConfidence string  `json:"min_confidence"`
		}
		if err := decodeGraphArgs(raw, &args); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		args.Symbol = strings.TrimSpace(args.Symbol)
		if err := validateGraphString("symbol", args.Symbol); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		var repo, file string
		if args.Repo != nil {
			repo = *args.Repo
			if err := validateGraphString("repo", repo); err != nil {
				return invalidGraphArgs(err, snap), nil
			}
		}
		if args.Path != nil {
			file = *args.Path
			if err := validateGraphString("path", file); err != nil {
				return invalidGraphArgs(err, snap), nil
			}
		}
		if file != "" && repo == "" {
			return invalidGraphArgs(errors.New("path requires repo"), snap), nil
		}
		hops := defaultTraceDepth
		if args.Hops != nil {
			hops = *args.Hops
		}
		if err := validateDepth("hops", hops); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		if minConfidence, err = graph.ParseMinConfidence(args.MinConfidence); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		result, err = snap.traceCallsScoped(ctx, args.Symbol, repo, file, hops, minConfidence)
	case "trace_consumers":
		var args struct {
			Name          string `json:"name"`
			MinConfidence string `json:"min_confidence"`
		}
		if err := decodeGraphArgs(raw, &args); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		args.Name = strings.TrimSpace(args.Name)
		if err := validateGraphString("name", args.Name); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		if minConfidence, err = graph.ParseMinConfidence(args.MinConfidence); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		result, err = snap.traceConsumers(ctx, args.Name, minConfidence)
	case "trace_hierarchy":
		var args struct {
			Symbol        string `json:"symbol"`
			Hops          *int   `json:"hops"`
			MinConfidence string `json:"min_confidence"`
		}
		if err := decodeGraphArgs(raw, &args); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		args.Symbol = strings.TrimSpace(args.Symbol)
		if err := validateGraphString("symbol", args.Symbol); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		hops := defaultTraceDepth
		if args.Hops != nil {
			hops = *args.Hops
		}
		if err := validateDepth("hops", hops); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		if minConfidence, err = graph.ParseMinConfidence(args.MinConfidence); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		result, err = snap.traceHierarchy(ctx, args.Symbol, hops, minConfidence)
	case "trace_queries":
		var args struct {
			Symbol        string `json:"symbol"`
			Hops          *int   `json:"hops"`
			MinConfidence string `json:"min_confidence"`
		}
		if err := decodeGraphArgs(raw, &args); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		args.Symbol = strings.TrimSpace(args.Symbol)
		if err := validateGraphString("symbol", args.Symbol); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		hops := defaultTraceDepth
		if args.Hops != nil {
			hops = *args.Hops
		}
		if err := validateDepth("hops", hops); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		if minConfidence, err = graph.ParseMinConfidence(args.MinConfidence); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		result, err = snap.traceQueries(ctx, args.Symbol, hops, minConfidence)
	case "trace_renders":
		var args struct {
			Symbol        string `json:"symbol"`
			Hops          *int   `json:"hops"`
			MinConfidence string `json:"min_confidence"`
		}
		if err := decodeGraphArgs(raw, &args); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		args.Symbol = strings.TrimSpace(args.Symbol)
		if err := validateGraphString("symbol", args.Symbol); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		hops := defaultTraceDepth
		if args.Hops != nil {
			hops = *args.Hops
		}
		if err := validateDepth("hops", hops); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		if minConfidence, err = graph.ParseMinConfidence(args.MinConfidence); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		result, err = snap.traceRenders(ctx, args.Symbol, hops, minConfidence)
	case "impact_analysis":
		var args struct {
			File          *string `json:"file"`
			Symbol        *string `json:"symbol"`
			Depth         *int    `json:"depth"`
			MinConfidence string  `json:"min_confidence"`
		}
		if err := decodeGraphArgs(raw, &args); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		if (args.File == nil) == (args.Symbol == nil) {
			return invalidGraphArgs(fmt.Errorf("exactly one of file or symbol is required"), snap), nil
		}
		file, symbol := "", ""
		if args.File != nil {
			file = strings.TrimSpace(*args.File)
			if err := validateGraphString("file", file); err != nil {
				return invalidGraphArgs(err, snap), nil
			}
		} else {
			symbol = strings.TrimSpace(*args.Symbol)
			if err := validateGraphString("symbol", symbol); err != nil {
				return invalidGraphArgs(err, snap), nil
			}
		}
		depth := defaultImpactDepth
		if args.Depth != nil {
			depth = *args.Depth
		}
		if err := validateDepth("depth", depth); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		if minConfidence, err = graph.ParseMinConfidence(args.MinConfidence); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		result, err = snap.impactAnalysis(ctx, file, symbol, depth, minConfidence)
	default:
		return nil, fmt.Errorf("server: unknown graph tool %q", t.name)
	}
	if err != nil {
		return nil, err
	}
	return snap.graphStructuredResult(result), nil
}

func decodeGraphArgs(raw json.RawMessage, dst interface{}) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || len(raw) > maxGraphInputBytes {
		if len(raw) > maxGraphInputBytes {
			return fmt.Errorf("arguments exceed %d bytes", maxGraphInputBytes)
		}
		return fmt.Errorf("arguments object is required")
	}
	if trimmed[0] != '{' {
		return fmt.Errorf("arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra interface{}
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("arguments must contain one JSON object")
		}
		return err
	}
	return nil
}

func validateGraphString(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if len(value) > 1024 {
		return fmt.Errorf("%s exceeds 1024 bytes", field)
	}
	return nil
}

func validateDepth(field string, value int) error {
	if value < 1 || value > maxGraphDepth {
		return fmt.Errorf("%s must be between 1 and %d", field, maxGraphDepth)
	}
	return nil
}

func invalidGraphArgs(err error, snapshots ...*graphSnapshot) map[string]interface{} {
	message := "invalid arguments: " + err.Error()
	if len(snapshots) > 0 && snapshots[0] != nil {
		return snapshots[0].errorResult("invalid_arguments", message)
	}
	return mcp.StructuredToolError("invalid_arguments", message, mcp.SnapshotIdentity{Cacheable: false})
}

func (s *graphSnapshot) graphStructuredResult(result GraphQueryResult) map[string]interface{} {
	identity := s.identity()
	for _, node := range result.Nodes {
		identity.BlobSHAs = append(identity.BlobSHAs, node.BlobSHA)
	}
	for _, edge := range result.Edges {
		identity.BlobSHAs = append(identity.BlobSHAs, edge.Evidence.BlobSHA)
	}
	text := fmt.Sprintf("%s returned %d node(s) and %d edge(s)", result.Tool, len(result.Nodes), len(result.Edges))
	if result.Truncated {
		text += "; traversal limited, discovered counts are lower bounds"
	}
	return mcp.StructuredResultWithSnapshot(
		text,
		result,
		false,
		identity,
	)
}

func (s *graphSnapshot) identity(blobSHAs ...string) mcp.SnapshotIdentity {
	var generation uint64
	if s.graph != nil {
		generation = s.graph.Generation()
	}
	return mcp.SnapshotIdentity{
		Cacheable:         true,
		CorpusFingerprint: s.corpusFingerprint,
		GraphGeneration:   generation,
		GraphBuildID:      s.buildID,
		BlobSHAs:          blobSHAs,
	}.Normalize()
}

func (s *graphSnapshot) structuredResult(text string, structured any, isError bool, blobSHAs ...string) map[string]interface{} {
	return mcp.StructuredResultWithSnapshot(text, structured, isError, s.identity(blobSHAs...))
}

func (s *graphSnapshot) errorResult(code, message string, blobSHAs ...string) map[string]interface{} {
	return mcp.StructuredToolError(code, message, s.identity(blobSHAs...))
}

func (s *graphSnapshot) traceCalls(ctx context.Context, symbol string, hops int, minConfidence graph.ConfidenceTier) (GraphQueryResult, error) {
	return s.traceCallsScoped(ctx, symbol, "", "", hops, minConfidence)
}

func (s *graphSnapshot) traceCallsScoped(ctx context.Context, symbol, repo, file string, hops int, minConfidence graph.ConfidenceTier) (GraphQueryResult, error) {
	roots := append([]diskgraph.Key(nil), s.bySymbol[symbol]...)
	if repo != "" {
		selected := roots[:0]
		for _, key := range roots {
			for _, location := range s.nodes[key].Locations {
				if location.Repo == repo && (file == "" || location.Path == file) {
					selected = append(selected, key)
					break
				}
			}
		}
		roots = selected
	}
	result, err := s.traverse(ctx, "trace_calls", symbol, roots, hops, true, minConfidence, func(t diskgraph.EdgeType) bool {
		return t == diskgraph.EdgeCalls
	})
	result.Repo, result.Path = repo, file
	return result, err
}

func (s *graphSnapshot) traceConsumers(ctx context.Context, name string, minConfidence graph.ConfidenceTier) (GraphQueryResult, error) {
	budget := newTraversalBudget()
	roots := append([]diskgraph.Key(nil), s.bySymbol[name]...)
	conf := rootsWithConfidence(roots)
	distances := rootsWithDistance(roots)
	relations := make(map[string]graphRelation)
	wanted := keySet(roots)
	incoming, err := s.incoming(ctx, wanted, func(t diskgraph.EdgeType) bool {
		return t == diskgraph.EdgePublishes || t == diskgraph.EdgeConsumes
	}, minConfidence, budget)
	if err != nil {
		return GraphQueryResult{}, err
	}
	for _, rel := range incoming {
		addRelation(relations, rel)
		conf[rel.Source] = maxTier(conf[rel.Source], rel.Confidence)
		distances[rel.Source] = 1
	}
	pruneDisconnectedRoots(conf, distances, roots, relations)
	result := s.makeResult("trace_consumers", name, 1, conf, distances, roots, relations)
	result.setBudget(budget)
	return result, nil
}

func (s *graphSnapshot) traceHierarchy(ctx context.Context, symbol string, hops int, minConfidence graph.ConfidenceTier) (GraphQueryResult, error) {
	roots := append([]diskgraph.Key(nil), s.bySymbol[symbol]...)
	return s.traverse(ctx, "trace_hierarchy", symbol, roots, hops, true, minConfidence, func(t diskgraph.EdgeType) bool {
		return t == diskgraph.EdgeExtends || t == diskgraph.EdgeImplements || t == diskgraph.EdgeContainsMethod || t == diskgraph.EdgeInjects
	})
}

func (s *graphSnapshot) traceQueries(ctx context.Context, symbol string, hops int, minConfidence graph.ConfidenceTier) (GraphQueryResult, error) {
	roots := append([]diskgraph.Key(nil), s.bySymbol[symbol]...)
	return s.traverse(ctx, "trace_queries", symbol, roots, hops, true, minConfidence, func(t diskgraph.EdgeType) bool {
		return t == diskgraph.EdgeQueries
	})
}

func (s *graphSnapshot) traceRenders(ctx context.Context, symbol string, hops int, minConfidence graph.ConfidenceTier) (GraphQueryResult, error) {
	roots := append([]diskgraph.Key(nil), s.bySymbol[symbol]...)
	return s.traverse(ctx, "trace_renders", symbol, roots, hops, true, minConfidence, func(t diskgraph.EdgeType) bool {
		return t == diskgraph.EdgeRenders
	})
}

func (s *graphSnapshot) impactAnalysis(ctx context.Context, file, symbol string, depth int, minConfidence graph.ConfidenceTier) (GraphQueryResult, error) {
	query := symbol
	var roots []diskgraph.Key
	if file != "" {
		query = file
		roots = s.rootsForFile(file)
	} else {
		roots = append([]diskgraph.Key(nil), s.bySymbol[symbol]...)
	}
	return s.traverse(ctx, "impact_analysis", query, roots, depth, false, minConfidence, func(diskgraph.EdgeType) bool { return true })
}

func (s *graphSnapshot) traverse(
	ctx context.Context,
	tool, query string,
	roots []diskgraph.Key,
	depth int,
	bothDirections bool,
	minConfidence graph.ConfidenceTier,
	allow func(diskgraph.EdgeType) bool,
) (GraphQueryResult, error) {
	budget := newTraversalBudget()
	confidence := rootsWithConfidence(roots)
	distances := rootsWithDistance(roots)
	visited := keySet(roots)
	frontier := append([]diskgraph.Key(nil), roots...)
	relations := make(map[string]graphRelation)

	for level := 0; level < depth && len(frontier) > 0 && !budget.truncated; level++ {
		if err := ctx.Err(); err != nil {
			return GraphQueryResult{}, err
		}
		nextSet := make(map[diskgraph.Key]bool)
		for _, key := range frontier {
			if !bothDirections {
				continue
			}
			err := s.outgoing(ctx, key, allow, minConfidence, budget, func(rel graphRelation) {
				addRelation(relations, rel)
				pathConfidence := minTier(confidence[key], rel.Confidence)
				confidence[rel.Target] = maxTier(confidence[rel.Target], pathConfidence)
				if !visited[rel.Target] {
					nextSet[rel.Target] = true
					if old, ok := distances[rel.Target]; !ok || level+1 < old {
						distances[rel.Target] = level + 1
					}
				}
			})
			if err != nil {
				return GraphQueryResult{}, err
			}
			if budget.truncated {
				break
			}
		}

		incoming, err := s.incoming(ctx, keySet(frontier), allow, minConfidence, budget)
		if err != nil {
			return GraphQueryResult{}, err
		}
		for _, rel := range incoming {
			addRelation(relations, rel)
			pathConfidence := minTier(confidence[rel.Target], rel.Confidence)
			confidence[rel.Source] = maxTier(confidence[rel.Source], pathConfidence)
			if !visited[rel.Source] {
				nextSet[rel.Source] = true
				if old, ok := distances[rel.Source]; !ok || level+1 < old {
					distances[rel.Source] = level + 1
				}
			}
		}
		frontier = sortedKeys(nextSet)
		for _, key := range frontier {
			visited[key] = true
		}
	}
	pruneDisconnectedRoots(confidence, distances, roots, relations)
	result := s.makeResult(tool, query, depth, confidence, distances, roots, relations)
	result.setBudget(budget)
	return result, nil
}

func pruneDisconnectedRoots(confidence map[diskgraph.Key]graph.ConfidenceTier, distances map[diskgraph.Key]int, roots []diskgraph.Key, relations map[string]graphRelation) {
	if len(relations) == 0 {
		return
	}
	incident := make(map[diskgraph.Key]bool, len(relations)*2)
	for _, relation := range relations {
		incident[relation.Source] = true
		incident[relation.Target] = true
	}
	for _, root := range roots {
		if incident[root] {
			continue
		}
		delete(confidence, root)
		delete(distances, root)
	}
}

// incoming intersects requested targets with shared sets before expanding any
// source records. The per-request budget bounds retained query work.
func (s *graphSnapshot) incoming(ctx context.Context, targets map[diskgraph.Key]bool, allow func(diskgraph.EdgeType) bool, minConfidence graph.ConfidenceTier, budget *traversalBudget) ([]graphRelation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(targets) == 0 || budget.truncated {
		return nil, nil
	}
	var out []graphRelation
	err := s.graph.EachIncomingContext(ctx, targets, func(edge diskgraph.Edge) bool {
		return ctx.Err() == nil && edge.Confidence >= minConfidence && allow(edge.Type)
	}, func(source diskgraph.Key, edge diskgraph.Edge) bool {
		if ctx.Err() != nil || !budget.take() {
			return false
		}
		out = append(out, relationFrom(source, edge))
		return true
	})
	return out, err
}

func relationFrom(source diskgraph.Key, edge diskgraph.Edge) graphRelation {
	return graphRelation{
		Source:     source,
		Target:     diskgraph.Key{BlobSHA: edge.TargetBlob, SymbolOffset: edge.TargetOffset},
		Type:       edge.Type,
		Confidence: edge.Confidence,
		Evidence:   edge.Evidence,
		Similarity: edge.Similarity,
	}
}

func addRelation(dst map[string]graphRelation, rel graphRelation) {
	// A relationship is source|type|target. Multiple evidence offsets support
	// the same claim; returning each as another graph edge wastes agent context
	// and inflates evaluator denominators. Retain the strongest relationship and
	// use stable evidence order only to break an equal-weight tie.
	id := keyID(rel.Source) + "\x00" + keyID(rel.Target) + "\x00" + strconv.FormatUint(uint64(rel.Type), 10)
	old, ok := dst[id]
	if !ok || rel.weight() > old.weight() || rel.weight() == old.weight() && evidenceLess(rel.Evidence, old.Evidence) {
		dst[id] = rel
	}
}

func evidenceLess(left, right graph.Evidence) bool {
	if left.BlobSHA != right.BlobSHA {
		return left.BlobSHA < right.BlobSHA
	}
	if left.ByteOffset != right.ByteOffset {
		return left.ByteOffset < right.ByteOffset
	}
	return left.ByteLength < right.ByteLength
}

func (s *graphSnapshot) makeResult(tool, query string, depth int, confidence map[diskgraph.Key]graph.ConfidenceTier, distances map[diskgraph.Key]int, roots []diskgraph.Key, relations map[string]graphRelation) GraphQueryResult {
	result := GraphQueryResult{
		TotalIsExact: true,
		Tool:         tool,
		Query:        query,
		Depth:        depth,
		Nodes:        make([]GraphNode, 0, len(confidence)),
		Edges:        make([]GraphEdge, 0, len(relations)),
	}
	for key, tier := range confidence {
		meta := s.nodes[key]
		locations := meta.Locations
		if locations == nil {
			locations = s.locationsForKey(key)
		}
		if locations == nil {
			locations = []GraphLocation{}
		}
		result.Nodes = append(result.Nodes, GraphNode{
			ID:           keyID(key),
			Symbol:       meta.Symbol,
			Kind:         meta.Kind,
			BlobSHA:      key.BlobSHA,
			SymbolOffset: key.SymbolOffset,
			Confidence:   graph.ConfidenceOf(tier),
			Hops:         distances[key],
			Locations:    locations,
		})
	}
	for _, rel := range relations {
		result.Edges = append(result.Edges, GraphEdge{
			Source:     keyID(rel.Source),
			Target:     keyID(rel.Target),
			Type:       rel.Type.String(),
			Confidence: graph.ConfidenceOf(rel.Confidence),
			Evidence:   rel.Evidence,
			Similarity: rel.Similarity,
		})
	}
	rootLocations := s.locationsForKeys(roots)
	proximity := newGraphProximity(rootLocations)
	nodeProximity := make(map[string]graphProximityRank, len(result.Nodes))
	for _, node := range result.Nodes {
		nodeProximity[node.ID] = proximity.rank(node.Locations)
	}
	sort.Slice(result.Nodes, func(i, j int) bool {
		a, b := result.Nodes[i], result.Nodes[j]
		if a.Hops != b.Hops {
			return a.Hops < b.Hops
		}
		if a.Confidence.Score != b.Confidence.Score {
			return a.Confidence.Score > b.Confidence.Score
		}
		aProximity, bProximity := nodeProximity[a.ID], nodeProximity[b.ID]
		if aProximity.sameRepo != bProximity.sameRepo {
			return aProximity.sameRepo
		}
		if aProximity.prefixDepth != bProximity.prefixDepth {
			return aProximity.prefixDepth > bProximity.prefixDepth
		}
		if a.Symbol != b.Symbol {
			return a.Symbol < b.Symbol
		}
		return a.ID < b.ID
	})
	nodeRank := make(map[string]int, len(result.Nodes))
	for i, node := range result.Nodes {
		nodeRank[node.ID] = i
	}
	sort.Slice(result.Edges, func(i, j int) bool {
		a, b := result.Edges[i], result.Edges[j]
		aDepth := max(nodeRank[a.Source], nodeRank[a.Target])
		bDepth := max(nodeRank[b.Source], nodeRank[b.Target])
		if aDepth != bDepth {
			return aDepth < bDepth
		}
		if a.Confidence.Score != b.Confidence.Score {
			return a.Confidence.Score > b.Confidence.Score
		}
		if nodeRank[a.Source] != nodeRank[b.Source] {
			return nodeRank[a.Source] < nodeRank[b.Source]
		}
		if nodeRank[a.Target] != nodeRank[b.Target] {
			return nodeRank[a.Target] < nodeRank[b.Target]
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		return evidenceLess(a.Evidence, b.Evidence)
	})
	return result
}

func (s *graphSnapshot) locationsForKeys(keys []diskgraph.Key) []GraphLocation {
	if len(keys) == 0 {
		return nil
	}
	// Merge once across the complete roster. The old per-root merge repeatedly
	// copied and sorted every location already seen. Keep its exact identity and
	// first-wins rule (BlobSHA is deliberately not part of that identity).
	out := make([]GraphLocation, 0)
	seen := make(map[string]struct{})
	for _, key := range keys {
		locations := s.nodes[key].Locations
		if locations == nil {
			locations = s.locationsForKey(key)
		}
		for _, loc := range locations {
			id := loc.Repo + "\x00" + loc.Path + "\x00" + loc.AbsPath + "\x00" + strconv.Itoa(loc.Line)
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, loc)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].AbsPath != out[j].AbsPath {
			return out[i].AbsPath < out[j].AbsPath
		}
		return out[i].Line < out[j].Line
	})
	return out
}

type graphProximityRank struct {
	sameRepo    bool
	prefixDepth int
}

type graphDirectoryPrefix struct {
	children map[string]*graphDirectoryPrefix
}

// graphProximity computes the same maxima as graphLocationProximity, without
// comparing every node location with every root location inside the sort. Repo
// equality and directory depth are independent in the existing ranking rule.
type graphProximity struct {
	repos map[string]struct{}
	dirs  graphDirectoryPrefix
}

func newGraphProximity(anchors []GraphLocation) graphProximity {
	result := graphProximity{repos: make(map[string]struct{})}
	for _, anchor := range anchors {
		if anchor.Repo != "" {
			result.repos[anchor.Repo] = struct{}{}
		}
		dir := path.Dir(strings.TrimPrefix(anchor.Path, "./"))
		if dir == "." {
			continue
		}
		node := &result.dirs
		for _, component := range strings.Split(dir, "/") {
			if node.children == nil {
				node.children = make(map[string]*graphDirectoryPrefix)
			}
			next := node.children[component]
			if next == nil {
				next = &graphDirectoryPrefix{}
				node.children[component] = next
			}
			node = next
		}
	}
	return result
}

func (p graphProximity) rank(locations []GraphLocation) graphProximityRank {
	var result graphProximityRank
	for _, loc := range locations {
		if _, ok := p.repos[loc.Repo]; ok {
			result.sameRepo = true
		}
		if len(p.dirs.children) == 0 {
			continue
		}
		dir := path.Dir(strings.TrimPrefix(loc.Path, "./"))
		if dir == "." {
			continue
		}
		node := &p.dirs
		depth := 0
		for _, component := range strings.Split(dir, "/") {
			node = node.children[component]
			if node == nil {
				break
			}
			depth++
		}
		result.prefixDepth = max(result.prefixDepth, depth)
	}
	return result
}

func graphLocationProximity(locations, anchors []GraphLocation) (sameRepo bool, prefixDepth int) {
	for _, loc := range locations {
		for _, anchor := range anchors {
			if loc.Repo != "" && loc.Repo == anchor.Repo {
				sameRepo = true
			}
			if depth := sharedDirectoryPrefixDepth(loc.Path, anchor.Path); depth > prefixDepth {
				prefixDepth = depth
			}
		}
	}
	return sameRepo, prefixDepth
}

func rootsWithConfidence(roots []diskgraph.Key) map[diskgraph.Key]graph.ConfidenceTier {
	out := make(map[diskgraph.Key]graph.ConfidenceTier, len(roots))
	for _, root := range roots {
		out[root] = graph.Proven
	}
	return out
}

func rootsWithDistance(roots []diskgraph.Key) map[diskgraph.Key]int {
	out := make(map[diskgraph.Key]int, len(roots))
	for _, root := range roots {
		out[root] = 0
	}
	return out
}

func keySet(keys []diskgraph.Key) map[diskgraph.Key]bool {
	out := make(map[diskgraph.Key]bool, len(keys))
	for _, key := range keys {
		out[key] = true
	}
	return out
}

func sortedKeys(set map[diskgraph.Key]bool) []diskgraph.Key {
	out := make([]diskgraph.Key, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Slice(out, func(i, j int) bool { return keyID(out[i]) < keyID(out[j]) })
	return out
}

func keyID(key diskgraph.Key) string {
	return key.BlobSHA + ":" + strconv.FormatUint(key.SymbolOffset, 10)
}

func minTier(left, right graph.ConfidenceTier) graph.ConfidenceTier {
	if right < left {
		return right
	}
	return left
}

func maxTier(left, right graph.ConfidenceTier) graph.ConfidenceTier {
	if right > left {
		return right
	}
	return left
}

func (s *graphSnapshot) rootsForFile(file string) []diskgraph.Key {
	want := cleanGraphPath(file)
	set := make(map[diskgraph.Key]bool)
	for key, meta := range s.nodes {
		for _, loc := range meta.Locations {
			if graphPathMatches(want, loc) {
				set[key] = true
				break
			}
		}
	}
	return sortedKeys(set)
}

func cleanGraphPath(path string) string {
	path = filepath.ToSlash(filepath.Clean(path))
	return strings.TrimPrefix(path, "./")
}

func graphPathMatches(want string, loc GraphLocation) bool {
	if want == "" || want == "." {
		return false
	}
	for _, candidate := range []string{
		cleanGraphPath(loc.Path),
		cleanGraphPath(filepath.Join(loc.Repo, loc.Path)),
		cleanGraphPath(loc.AbsPath),
	} {
		if candidate == want || strings.HasSuffix(candidate, "/"+want) {
			return true
		}
	}
	return false
}
