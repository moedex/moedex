package server

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
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"moedex/internal/graph"
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

	nodes    map[diskgraph.Key]nodeMetadata
	bySymbol map[string][]diskgraph.Key
	blobs    map[string][]*index.Blob
	// byPath resolves a FILE to the graph nodes located in it, keyed by every
	// normalized spelling of that file's path (absolute, repo/rel, and rel). It is
	// what lets a search_context block — which knows only a path and a line range —
	// be anchored to graph nodes without a reverse scan. See anchorsFor.
	byPath map[string][]locatedNode
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
	Tool  string      `json:"tool"`
	Query string      `json:"query"`
	Depth int         `json:"depth"`
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
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
// A missing, unbuilt, or unopenable graph sidecar is never fatal: every other
// ranking sidecar (token index, symbol index, embedding store) degrades
// best-effort, moedex-index build/refresh treats a graph-build failure as a
// logged warning rather than aborting the shard build, and this same toolset's
// own Reload keeps serving the old (possibly absent) graph when a refresh's
// re-open fails. Boot mirrors that: log a warning and return a toolset with no
// active generation — every graph tool call and Neighbors annotation already
// handles that state gracefully (see acquire) — instead of failing the whole
// MCP daemon over an optional sidecar.
func OpenGraphTools(dir string) (*GraphToolset, error) {
	snap, err := openGraphSnapshot(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "server: open graph (serving without graph tools/annotations until the next reload): %v\n", err)
		return &GraphToolset{}, nil
	}
	return &GraphToolset{cur: snap}, nil
}

func openGraphSnapshot(dir string) (*graphSnapshot, error) {
	g, err := diskgraph.Open(GraphPath(dir))
	if err != nil {
		return nil, fmt.Errorf("server: open graph: %w", err)
	}
	syms, err := OpenSymbols(dir)
	if err != nil {
		_ = g.Close()
		return nil, fmt.Errorf("server: open graph symbols: %w", err)
	}
	s := &graphSnapshot{
		graph:    g,
		symbols:  syms,
		nodes:    make(map[diskgraph.Key]nodeMetadata),
		bySymbol: make(map[string][]diskgraph.Key),
		blobs:    make(map[string][]*index.Blob),
		byPath:   make(map[string][]locatedNode),
	}
	s.buildCatalog()
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
	s.graph.EachEdge(func(_ diskgraph.Key, edge diskgraph.Edge) bool {
		target := diskgraph.Key{BlobSHA: edge.TargetBlob, SymbolOffset: edge.TargetOffset}
		if _, ok := s.nodes[target]; !ok {
			s.nodes[target] = nodeMetadata{Locations: s.locationsForKey(target)}
		}
		return true
	})
	for name := range s.bySymbol {
		sort.Slice(s.bySymbol[name], func(i, j int) bool { return keyID(s.bySymbol[name][i]) < keyID(s.bySymbol[name][j]) })
	}
	s.buildPathIndex()
}

// buildPathIndex inverts the node catalog's locations into a file -> nodes
// lookup. Every node is filed under each spelling of its location's path, so a
// caller holding any one of them (a context block reports an absolute path, the
// graph tools accept repo-relative or bare suffixes) resolves without a scan.
func (s *graphSnapshot) buildPathIndex() {
	for key, meta := range s.nodes {
		locations := meta.Locations
		if locations == nil {
			locations = s.locationsForKey(key)
		}
		for _, loc := range locations {
			for _, spelling := range indexedPathSpellings(loc) {
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
			return keyID(nodes[i].key) < keyID(nodes[j].key)
		})
	}
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
		out = append(out, GraphLocation{Repo: file.Repo, Path: file.RelPath, AbsPath: file.AbsPath, Line: line})
	}
	return out
}

func mergeLocations(left, right []GraphLocation) []GraphLocation {
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
	return errors.Join(s.graph.Close(), s.symbols.Close())
}

type graphTool struct {
	owner *GraphToolset
	name  string
}

func (t *graphTool) Name() string { return t.name }

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
		description = "Trace callers and callees of a symbol through confidence-scored call edges from the mmap graph."
		schema["properties"] = map[string]interface{}{
			"symbol": map[string]interface{}{"type": "string", "minLength": 1, "description": "Exact symbol name to trace."},
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
			"file":   map[string]interface{}{"type": "string", "minLength": 1, "description": "Absolute, repo-relative, or suffix file path."},
			"symbol": map[string]interface{}{"type": "string", "minLength": 1, "description": "Exact symbol name."},
			"depth":  depth,
		}
		schema["oneOf"] = []interface{}{
			map[string]interface{}{"required": []string{"file"}},
			map[string]interface{}{"required": []string{"symbol"}},
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

	var (
		result GraphQueryResult
		err    error
	)
	switch t.name {
	case "trace_calls":
		var args struct {
			Symbol string `json:"symbol"`
			Hops   *int   `json:"hops"`
		}
		if err := decodeGraphArgs(raw, &args); err != nil {
			return invalidGraphArgs(err), nil
		}
		args.Symbol = strings.TrimSpace(args.Symbol)
		if err := validateGraphString("symbol", args.Symbol); err != nil {
			return invalidGraphArgs(err), nil
		}
		hops := defaultTraceDepth
		if args.Hops != nil {
			hops = *args.Hops
		}
		if err := validateDepth("hops", hops); err != nil {
			return invalidGraphArgs(err), nil
		}
		result, err = snap.traceCalls(ctx, args.Symbol, hops)
	case "trace_consumers":
		var args struct {
			Name string `json:"name"`
		}
		if err := decodeGraphArgs(raw, &args); err != nil {
			return invalidGraphArgs(err), nil
		}
		args.Name = strings.TrimSpace(args.Name)
		if err := validateGraphString("name", args.Name); err != nil {
			return invalidGraphArgs(err), nil
		}
		result, err = snap.traceConsumers(ctx, args.Name)
	case "trace_hierarchy":
		var args struct {
			Symbol string `json:"symbol"`
			Hops   *int   `json:"hops"`
		}
		if err := decodeGraphArgs(raw, &args); err != nil {
			return invalidGraphArgs(err), nil
		}
		args.Symbol = strings.TrimSpace(args.Symbol)
		if err := validateGraphString("symbol", args.Symbol); err != nil {
			return invalidGraphArgs(err), nil
		}
		hops := defaultTraceDepth
		if args.Hops != nil {
			hops = *args.Hops
		}
		if err := validateDepth("hops", hops); err != nil {
			return invalidGraphArgs(err), nil
		}
		result, err = snap.traceHierarchy(ctx, args.Symbol, hops)
	case "trace_queries":
		var args struct {
			Symbol string `json:"symbol"`
			Hops   *int   `json:"hops"`
		}
		if err := decodeGraphArgs(raw, &args); err != nil {
			return invalidGraphArgs(err), nil
		}
		args.Symbol = strings.TrimSpace(args.Symbol)
		if err := validateGraphString("symbol", args.Symbol); err != nil {
			return invalidGraphArgs(err), nil
		}
		hops := defaultTraceDepth
		if args.Hops != nil {
			hops = *args.Hops
		}
		if err := validateDepth("hops", hops); err != nil {
			return invalidGraphArgs(err), nil
		}
		result, err = snap.traceQueries(ctx, args.Symbol, hops)
	case "trace_renders":
		var args struct {
			Symbol string `json:"symbol"`
			Hops   *int   `json:"hops"`
		}
		if err := decodeGraphArgs(raw, &args); err != nil {
			return invalidGraphArgs(err), nil
		}
		args.Symbol = strings.TrimSpace(args.Symbol)
		if err := validateGraphString("symbol", args.Symbol); err != nil {
			return invalidGraphArgs(err), nil
		}
		hops := defaultTraceDepth
		if args.Hops != nil {
			hops = *args.Hops
		}
		if err := validateDepth("hops", hops); err != nil {
			return invalidGraphArgs(err), nil
		}
		result, err = snap.traceRenders(ctx, args.Symbol, hops)
	case "impact_analysis":
		var args struct {
			File   *string `json:"file"`
			Symbol *string `json:"symbol"`
			Depth  *int    `json:"depth"`
		}
		if err := decodeGraphArgs(raw, &args); err != nil {
			return invalidGraphArgs(err), nil
		}
		if (args.File == nil) == (args.Symbol == nil) {
			return invalidGraphArgs(fmt.Errorf("exactly one of file or symbol is required")), nil
		}
		file, symbol := "", ""
		if args.File != nil {
			file = strings.TrimSpace(*args.File)
			if err := validateGraphString("file", file); err != nil {
				return invalidGraphArgs(err), nil
			}
		} else {
			symbol = strings.TrimSpace(*args.Symbol)
			if err := validateGraphString("symbol", symbol); err != nil {
				return invalidGraphArgs(err), nil
			}
		}
		depth := defaultImpactDepth
		if args.Depth != nil {
			depth = *args.Depth
		}
		if err := validateDepth("depth", depth); err != nil {
			return invalidGraphArgs(err), nil
		}
		result, err = snap.impactAnalysis(ctx, file, symbol, depth)
	default:
		return nil, fmt.Errorf("server: unknown graph tool %q", t.name)
	}
	if err != nil {
		return nil, err
	}
	return graphStructuredResult(result), nil
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

func invalidGraphArgs(err error) map[string]interface{} {
	return mcp.TextResult("invalid arguments: "+err.Error(), true)
}

func graphStructuredResult(result GraphQueryResult) map[string]interface{} {
	return mcp.StructuredResult(
		fmt.Sprintf("%s returned %d node(s) and %d edge(s)", result.Tool, len(result.Nodes), len(result.Edges)),
		result,
		false,
	)
}

func (s *graphSnapshot) traceCalls(ctx context.Context, symbol string, hops int) (GraphQueryResult, error) {
	roots := append([]diskgraph.Key(nil), s.bySymbol[symbol]...)
	return s.traverse(ctx, "trace_calls", symbol, roots, hops, true, func(t diskgraph.EdgeType) bool {
		return t == diskgraph.EdgeCalls
	})
}

func (s *graphSnapshot) traceConsumers(ctx context.Context, name string) (GraphQueryResult, error) {
	roots := append([]diskgraph.Key(nil), s.bySymbol[name]...)
	conf := rootsWithConfidence(roots)
	relations := make(map[string]graphRelation)
	wanted := keySet(roots)
	incoming, err := s.incoming(ctx, wanted, func(t diskgraph.EdgeType) bool {
		return t == diskgraph.EdgePublishes || t == diskgraph.EdgeConsumes
	})
	if err != nil {
		return GraphQueryResult{}, err
	}
	for _, rel := range incoming {
		addRelation(relations, rel)
		conf[rel.Source] = maxTier(conf[rel.Source], rel.Confidence)
	}
	return s.makeResult("trace_consumers", name, 1, conf, relations), nil
}

func (s *graphSnapshot) traceHierarchy(ctx context.Context, symbol string, hops int) (GraphQueryResult, error) {
	roots := append([]diskgraph.Key(nil), s.bySymbol[symbol]...)
	return s.traverse(ctx, "trace_hierarchy", symbol, roots, hops, true, func(t diskgraph.EdgeType) bool {
		return t == diskgraph.EdgeExtends || t == diskgraph.EdgeImplements || t == diskgraph.EdgeContainsMethod || t == diskgraph.EdgeInjects
	})
}

func (s *graphSnapshot) traceQueries(ctx context.Context, symbol string, hops int) (GraphQueryResult, error) {
	roots := append([]diskgraph.Key(nil), s.bySymbol[symbol]...)
	return s.traverse(ctx, "trace_queries", symbol, roots, hops, true, func(t diskgraph.EdgeType) bool {
		return t == diskgraph.EdgeQueries
	})
}

func (s *graphSnapshot) traceRenders(ctx context.Context, symbol string, hops int) (GraphQueryResult, error) {
	roots := append([]diskgraph.Key(nil), s.bySymbol[symbol]...)
	return s.traverse(ctx, "trace_renders", symbol, roots, hops, true, func(t diskgraph.EdgeType) bool {
		return t == diskgraph.EdgeRenders
	})
}

func (s *graphSnapshot) impactAnalysis(ctx context.Context, file, symbol string, depth int) (GraphQueryResult, error) {
	query := symbol
	var roots []diskgraph.Key
	if file != "" {
		query = file
		roots = s.rootsForFile(file)
	} else {
		roots = append([]diskgraph.Key(nil), s.bySymbol[symbol]...)
	}
	return s.traverse(ctx, "impact_analysis", query, roots, depth, false, func(diskgraph.EdgeType) bool { return true })
}

func (s *graphSnapshot) traverse(
	ctx context.Context,
	tool, query string,
	roots []diskgraph.Key,
	depth int,
	bothDirections bool,
	allow func(diskgraph.EdgeType) bool,
) (GraphQueryResult, error) {
	confidence := rootsWithConfidence(roots)
	visited := keySet(roots)
	frontier := append([]diskgraph.Key(nil), roots...)
	relations := make(map[string]graphRelation)

	for level := 0; level < depth && len(frontier) > 0; level++ {
		if err := ctx.Err(); err != nil {
			return GraphQueryResult{}, err
		}
		nextSet := make(map[diskgraph.Key]bool)
		for _, key := range frontier {
			if !bothDirections {
				continue
			}
			for _, edge := range s.graph.Edges(key) {
				if !allow(edge.Type) {
					continue
				}
				rel := relationFrom(key, edge)
				addRelation(relations, rel)
				pathConfidence := minTier(confidence[key], edge.Confidence)
				confidence[rel.Target] = maxTier(confidence[rel.Target], pathConfidence)
				if !visited[rel.Target] {
					nextSet[rel.Target] = true
				}
			}
		}

		incoming, err := s.incoming(ctx, keySet(frontier), allow)
		if err != nil {
			return GraphQueryResult{}, err
		}
		for _, rel := range incoming {
			addRelation(relations, rel)
			pathConfidence := minTier(confidence[rel.Target], rel.Confidence)
			confidence[rel.Source] = maxTier(confidence[rel.Source], pathConfidence)
			if !visited[rel.Source] {
				nextSet[rel.Source] = true
			}
		}
		frontier = sortedKeys(nextSet)
		for _, key := range frontier {
			visited[key] = true
		}
	}
	return s.makeResult(tool, query, depth, confidence, relations), nil
}

// incoming scans the mmap adjacency records without retaining a reverse graph.
// The sweep is one allocation-free linear pass (diskgraph.EachEdge) rather than a
// per-node lookup, because search_context now runs it on every ranked query.
func (s *graphSnapshot) incoming(ctx context.Context, targets map[diskgraph.Key]bool, allow func(diskgraph.EdgeType) bool) ([]graphRelation, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	var (
		out  []graphRelation
		err  error
		seen int
	)
	s.graph.EachEdge(func(source diskgraph.Key, edge diskgraph.Edge) bool {
		seen++
		if seen&1023 == 0 {
			if cancelled := ctx.Err(); cancelled != nil {
				err = cancelled
				return false
			}
		}
		if !allow(edge.Type) {
			return true
		}
		rel := relationFrom(source, edge)
		if targets[rel.Target] {
			out = append(out, rel)
		}
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
	id := keyID(rel.Source) + "\x00" + keyID(rel.Target) + "\x00" + strconv.FormatUint(uint64(rel.Type), 10) + "\x00" + rel.Evidence.BlobSHA + "\x00" + strconv.FormatUint(rel.Evidence.ByteOffset, 10) + "\x00" + strconv.FormatUint(rel.Evidence.ByteLength, 10)
	if old, ok := dst[id]; !ok || rel.weight() > old.weight() {
		dst[id] = rel
	}
}

func (s *graphSnapshot) makeResult(tool, query string, depth int, confidence map[diskgraph.Key]graph.ConfidenceTier, relations map[string]graphRelation) GraphQueryResult {
	result := GraphQueryResult{
		Tool:  tool,
		Query: query,
		Depth: depth,
		Nodes: make([]GraphNode, 0, len(confidence)),
		Edges: make([]GraphEdge, 0, len(relations)),
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
	sort.Slice(result.Nodes, func(i, j int) bool { return result.Nodes[i].ID < result.Nodes[j].ID })
	sort.Slice(result.Edges, func(i, j int) bool {
		if result.Edges[i].Source != result.Edges[j].Source {
			return result.Edges[i].Source < result.Edges[j].Source
		}
		if result.Edges[i].Target != result.Edges[j].Target {
			return result.Edges[i].Target < result.Edges[j].Target
		}
		if result.Edges[i].Type != result.Edges[j].Type {
			return result.Edges[i].Type < result.Edges[j].Type
		}
		if result.Edges[i].Evidence.BlobSHA != result.Edges[j].Evidence.BlobSHA {
			return result.Edges[i].Evidence.BlobSHA < result.Edges[j].Evidence.BlobSHA
		}
		return result.Edges[i].Evidence.ByteOffset < result.Edges[j].Evidence.ByteOffset
	})
	return result
}

func rootsWithConfidence(roots []diskgraph.Key) map[diskgraph.Key]graph.ConfidenceTier {
	out := make(map[diskgraph.Key]graph.ConfidenceTier, len(roots))
	for _, root := range roots {
		out[root] = graph.Proven
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
