package server

// graphbuild.go is the offline bridge from the graph candidate/verification
// pipeline to the mmap-backed adjacency format. It intentionally builds from
// the per-shard indices (rather than loadUnified): candidate sites carry
// shard-local blob IDs until they are folded to content identity by blob SHA.
//
// The sweep is factored into graphSweep because the incremental refresh in
// graphrefresh.go runs the SAME per-name generation over the SAME shard view and
// only differs in which names it spends the fan-out on. Keeping one emitter means
// a carried-forward edge and a freshly computed one cannot disagree about record
// shape, ordering, or dedup.

import (
	"context"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/embed"
	"moedex/internal/graph/candidates"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/graph/httproute"
	"moedex/internal/graph/manifest"
	graphverify "moedex/internal/graph/verify"
	"moedex/internal/index"
	"moedex/internal/symbol"
	"moedex/internal/trigram"
)

// GraphFileName is the graph adjacency file written next to the corpus token
// and symbol files.
const GraphFileName = "corpus-graph.graph"

const (
	// DefaultSimilarTopK bounds each definition's semantic neighborhood.
	DefaultSimilarTopK = 5
	// DefaultSimilarThreshold keeps the default graph focused on near-duplicate
	// implementations and strong pattern matches rather than broad topic affinity.
	DefaultSimilarThreshold = 0.60
)

// GraphBuildOptions controls optional graph passes. SIMILAR_TO construction is
// compiled only with the onnx build tag. Embedder is injected so tagged tests
// and offline callers share the same embed.BuildStore path.
type GraphBuildOptions struct {
	SimilarTopK      int
	SimilarThreshold float64
	Embedder         embed.Embedder

	// LSPRequestsPerSecond caps the aggregate request start rate across all
	// language servers used by the optional lsp-tagged call-graph pass. Zero
	// selects DefaultLSPRequestsPerSecond. The field is inert without -tags lsp.
	LSPRequestsPerSecond float64
	// LSPRequestTimeout bounds each document-symbol and find-references call.
	// Zero selects DefaultLSPRequestTimeout. The field is inert without -tags lsp.
	LSPRequestTimeout time.Duration
	// LSPStats, when non-nil, receives coverage counters from the lsp-tagged pass.
	LSPStats *LSPGraphStats
}

const (
	// DefaultLSPRequestsPerSecond is intentionally conservative: language
	// servers already do substantial workspace analysis per request, and the
	// offline sweep must not turn symbol count into an unbounded request burst.
	DefaultLSPRequestsPerSecond = 5
	// DefaultLSPRequestTimeout prevents one wedged symbol query from stalling an
	// entire graph rebuild indefinitely.
	DefaultLSPRequestTimeout = 30 * time.Second
)

// LSPGraphStats describes one systematic call-graph sweep. It is populated only
// in lsp-tagged builds; default builds leave it at the zero value.
type LSPGraphStats struct {
	EligibleFiles        int
	DocumentRequests     int
	DiscoveredSymbols    int
	PrioritizedSymbols   int
	ReferenceRequests    int
	FailedRequests       int
	RestrictedWorkspaces int
	CallEdges            int
}

// GraphPath returns the graph adjacency file path under dir.
func GraphPath(dir string) string { return filepath.Join(dir, GraphFileName) }

// GraphBuildReport accounts for one graph build, so a small graph is never
// mistaken for a small corpus.
type GraphBuildReport struct {
	Nodes    int
	Edges    uint64
	HTTP     httproute.Report
	Manifest manifest.Report
}

// BuildGraph generates, verifies, and persists the graph for every exported
// trigram-length symbol name in dir's shard set, plus proven DEPENDS_ON edges
// from package manifests. Posting lists and, for deduped shards, content stay
// mmap-backed during the offline sweep.
//
// This is the unconditional full build, stamped diskgraph.FirstGeneration. A dir
// that already holds a graph should usually go through RefreshGraph, which
// recomputes only the names the content delta invalidated.
func BuildGraph(dir string) (path string, report GraphBuildReport, err error) {
	return BuildGraphWithOptions(dir, GraphBuildOptions{})
}

// BuildGraphWithOptions is BuildGraph plus optional tagged graph passes such as
// ONNX-backed semantic similarity.
func BuildGraphWithOptions(dir string, opts GraphBuildOptions) (path string, report GraphBuildReport, err error) {
	if opts.SimilarTopK < 0 {
		return "", report, fmt.Errorf("server: graph similar top-K must be non-negative")
	}
	if math.IsNaN(opts.SimilarThreshold) || opts.SimilarThreshold < -1 || opts.SimilarThreshold > 1 {
		return "", report, fmt.Errorf("server: graph similarity threshold %g is outside [-1,1]", opts.SimilarThreshold)
	}
	if math.IsNaN(opts.LSPRequestsPerSecond) || math.IsInf(opts.LSPRequestsPerSecond, 0) || opts.LSPRequestsPerSecond < 0 {
		return "", report, fmt.Errorf("server: graph LSP requests/second must be finite and non-negative")
	}
	if opts.LSPRequestTimeout < 0 {
		return "", report, fmt.Errorf("server: graph LSP request timeout must be non-negative")
	}
	if opts.LSPStats != nil {
		*opts.LSPStats = LSPGraphStats{}
	}

	sweep, err := openGraphSweep(dir)
	if err != nil {
		return "", report, err
	}
	defer func() {
		if closeErr := sweep.Close(); err == nil {
			err = closeErr
		}
	}()

	builder := diskgraph.NewBuilder()
	builder.SetGeneration(diskgraph.FirstGeneration)
	sweep.recordCorpusRoster(builder)
	emit := newGraphEmitter(builder)

	crossRelevant := make(map[string]bool)
	for _, name := range sweep.names {
		for _, definition := range sweep.merged.Definitions(name) {
			if definition.Shard < 0 || definition.Shard >= len(sweep.idxs) {
				return "", report, fmt.Errorf("server: graph definition %q references unknown shard %d", name, definition.Shard)
			}
			blob := sweep.idxs[definition.Shard].Blob(definition.Blob)
			if blob == nil {
				return "", report, fmt.Errorf("server: graph definition %q references an unknown blob", name)
			}
			if definition.Start < 0 {
				return "", report, fmt.Errorf("server: graph definition %q has a negative byte offset", name)
			}
			if err := builder.AddNode(diskgraph.Key{BlobSHA: blob.SHA, SymbolOffset: uint64(definition.Start)}); err != nil {
				return "", report, err
			}
		}
		if err := sweep.emit(name, diskgraph.FirstGeneration, emit); err != nil {
			return "", report, err
		}
		generated := candidates.GenerateCandidates(sweep.corpus, name)
		for _, candidate := range generated {
			if candidate.CrossShard() {
				crossRelevant[name] = true
				break
			}
		}
	}
	if err := addLSPCallEdges(context.Background(), builder, sweep.merged, sweep.idxs, emit.seen, crossRelevant, opts); err != nil {
		return "", report, err
	}
	if err := addSimilarToEdges(context.Background(), builder, sweep.merged, sweep.idxs, emit.seen, opts); err != nil {
		return "", report, err
	}

	httpShards := make([]httproute.Shard, len(sweep.idxs))
	for i, shardPath := range sweep.paths {
		httpShards[i] = httproute.Shard{Name: filepath.Base(shardPath), Index: sweep.idxs[i], Symbols: sweep.symbols[i]}
	}
	httpReport, err := addHTTPCallEdges(builder, httproute.NewCorpus(httpShards...), emit.seen)
	if err != nil {
		return "", report, err
	}

	manifestReport, err := addManifestEdges(builder, emit.seen, sweep.idxs)
	if err != nil {
		return "", report, err
	}
	report = GraphBuildReport{Nodes: builder.NumNodes(), Edges: builder.NumEdges(), HTTP: httpReport, Manifest: manifestReport}

	path, err = saveGraph(builder, dir)
	return path, report, err
}

// ---------------------------------------------------------------------------
// graphSweep — the loaded shard-set view a graph build or refresh runs over
// ---------------------------------------------------------------------------

// blobSite locates one copy of a blob's content within the loaded shard set.
type blobSite struct {
	shard int
	blob  uint64
}

// graphSweep is the loaded shard-set view a graph build or refresh runs over:
// the merged cross-shard symbol index, the per-shard content indices behind it,
// the eligible name list, and the corpus's blob roster.
type graphSweep struct {
	corpus  *candidates.Corpus
	merged  *symbol.Corpus
	idxs    []*index.Index
	paths   []string
	symbols []*symbol.Index

	names    []string
	eligible map[string]int

	// sites maps every blob SHA in the shard set to every shard copy of it.
	sites map[string][]blobSite
	// identity maps each blob SHA to the roster token persisted in the graph.
	identity map[string]string

	closers []io.Closer
	content io.Closer
}

// openGraphSweep loads dir's shard set and derives everything a graph pass needs
// from it. The caller must Close the result.
func openGraphSweep(dir string) (sweep *graphSweep, err error) {
	paths, err := globShards(dir)
	if err != nil {
		return nil, err
	}
	contentStore, err := openSharedContent(dir, paths)
	if err != nil {
		return nil, err
	}
	s := &graphSweep{
		sites: make(map[string][]blobSite),
		paths: paths,
	}
	if contentStore != nil {
		s.content = contentStore
	}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()

	merged := symbol.NewCorpus()
	s.symbols = make([]*symbol.Index, 0, len(paths))
	for _, shardPath := range paths {
		var (
			ix     *index.Index
			closer io.Closer
		)
		if contentStore != nil && diskstore.IsDeduped(shardPath) {
			ix, closer, err = diskstore.LoadMmapDeduped(shardPath, contentStore)
		} else {
			ix, closer, err = diskstore.LoadMmap(shardPath)
		}
		if err != nil {
			return nil, fmt.Errorf("server: load graph shard %s: %w", shardPath, err)
		}
		s.closers = append(s.closers, closer)
		sym := symbol.BuildMulti(ix)
		s.symbols = append(s.symbols, sym)
		shard := merged.AddShard(filepath.Base(shardPath), sym)
		s.idxs = append(s.idxs, ix)
		for id := uint64(0); id < uint64(ix.NumBlobs()); id++ {
			b := ix.Blob(id)
			if b == nil || b.SHA == "" {
				continue
			}
			s.sites[b.SHA] = append(s.sites[b.SHA], blobSite{shard: shard, blob: id})
		}
	}
	s.merged = merged

	// Fold each SHA's per-shard path context into one roster token.
	s.identity = make(map[string]string, len(s.sites))
	exts := make([]string, 0, 4)
	for sha, sites := range s.sites {
		exts = exts[:0]
		for _, site := range sites {
			exts = append(exts, blobExtensions(s.idxs[site.shard].Blob(site.blob)))
		}
		sort.Strings(exts)
		s.identity[sha] = sha + "\x00" + strings.Join(exts, "\x01")
	}
	if s.corpus, err = candidates.NewCorpus(merged, s.idxs...); err != nil {
		return nil, err
	}

	s.names = make([]string, 0, merged.NumNames())
	merged.EachName(func(name string) bool {
		if len(name) >= trigram.N && candidates.Exported(name) {
			s.names = append(s.names, name)
		}
		return true
	})
	sort.Strings(s.names)
	s.eligible = make(map[string]int, len(s.names))
	for i, name := range s.names {
		s.eligible[name] = i
	}
	return s, nil
}

// Close releases every mmap the sweep opened, innermost first.
func (s *graphSweep) Close() error {
	var err error
	for i := len(s.closers) - 1; i >= 0; i-- {
		if closeErr := s.closers[i].Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("server: close graph shard mmap: %w", closeErr)
		}
	}
	s.closers = nil
	if s.content != nil {
		if closeErr := s.content.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("server: close graph content mmap: %w", closeErr)
		}
		s.content = nil
	}
	return err
}

// blobExtensions returns the distinct lower-cased file extensions of a blob's
// refs within one shard, in first-appearance order.
func blobExtensions(b *index.Blob) string {
	var out []string
	seen := make(map[string]struct{}, len(b.Files))
	for _, f := range b.Files {
		ext := strings.ToLower(filepath.Ext(f.RelPath))
		if _, dup := seen[ext]; dup {
			continue
		}
		seen[ext] = struct{}{}
		out = append(out, ext)
	}
	return strings.Join(out, ",")
}

// recordCorpusRoster stamps one identity token per corpus blob into the builder.
func (s *graphSweep) recordCorpusRoster(builder *diskgraph.Builder) {
	for _, token := range s.identity {
		builder.AddCorpusEntry(token)
	}
}

// rosterSHA recovers the blob SHA from a roster token.
func rosterSHA(token string) string {
	if i := strings.IndexByte(token, 0); i >= 0 {
		return token[:i]
	}
	return token
}

// saveGraph persists builder as dir's graph adjacency file.
func saveGraph(builder *diskgraph.Builder, dir string) (string, error) {
	path := GraphPath(dir)
	if err := builder.Save(path); err != nil {
		return "", fmt.Errorf("server: persist graph: %w", err)
	}
	return path, nil
}

// graphKeyEdge is a resolved key/edge pair ready for the emitter.
type graphKeyEdge struct {
	Key  diskgraph.Key
	Edge diskgraph.Edge
}

// computeEdgesForName runs the expensive generate→verify→resolve pipeline for
// one name and returns the resolved key/edge pairs. It is read-only against the
// corpus and symbol index, so multiple names can be computed concurrently.
func (s *graphSweep) computeEdgesForName(name string, generation uint64) ([]graphKeyEdge, error) {
	scored := graphverify.Verify(candidates.GenerateCandidates(s.corpus, name))
	out := make([]graphKeyEdge, 0, len(scored))
	for _, sc := range scored {
		sourceBlob := s.corpus.Blob(sc.Source)
		targetBlob := s.corpus.Blob(sc.Target)
		if sourceBlob == nil || targetBlob == nil {
			return nil, fmt.Errorf("server: graph edge %q references an unknown blob", name)
		}
		if sc.Source.Start < 0 || sc.Target.Start < 0 {
			return nil, fmt.Errorf("server: graph edge %q has a negative byte offset", name)
		}

		sourceOffset := uint64(sc.Source.Start)
		if enclosing, ok := s.merged.Enclosing(sc.Source.Shard, sc.Source.Blob, sc.Source.Start); ok {
			if enclosing.NameStart < 0 {
				return nil, fmt.Errorf("server: enclosing symbol %q has a negative byte offset", enclosing.Name)
			}
			sourceOffset = uint64(enclosing.NameStart)
		}
		out = append(out, graphKeyEdge{
			Key: diskgraph.Key{BlobSHA: sourceBlob.SHA, SymbolOffset: sourceOffset},
			Edge: diskgraph.Edge{
				Type:         graphEdgeType(sc, sourceBlob.Content),
				TargetBlob:   targetBlob.SHA,
				TargetOffset: uint64(sc.Target.Start),
				Confidence:   sc.Confidence,
				Evidence:     sc.Evidence,
				Name:         name,
				Generation:   generation,
			},
		})
	}
	return out, nil
}

// emit generates, verifies, and hands every edge for name to add, stamped with
// generation. It is the one place a candidate becomes a persisted record, so a
// full build and an incremental recompute produce byte-identical output for the
// same name.
func (s *graphSweep) emit(name string, generation uint64, add *graphEmitter) error {
	edges, err := s.computeEdgesForName(name, generation)
	if err != nil {
		return err
	}
	for i := range edges {
		if err := add.Add(edges[i].Key, edges[i].Edge); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// graphEmitter — dedup wrapper around diskgraph.Builder
// ---------------------------------------------------------------------------

// graphEmitter folds the duplicate candidates produced when identical content
// occurs in several shards down to one record and appends the survivors to the
// builder.
type graphEmitter struct {
	builder *diskgraph.Builder
	seen    map[persistedGraphEdge]struct{}
}

// newGraphEmitter returns a new emitter that deduplicates and appends to builder.
func newGraphEmitter(builder *diskgraph.Builder) *graphEmitter {
	return &graphEmitter{builder: builder, seen: make(map[persistedGraphEdge]struct{})}
}

// Add deduplicates and appends one edge.
func (e *graphEmitter) Add(key diskgraph.Key, edge diskgraph.Edge) error {
	record := persistedGraphEdge{
		sourceBlob:     key.BlobSHA,
		sourceOffset:   key.SymbolOffset,
		name:           edge.Name,
		typeID:         edge.Type,
		targetBlob:     edge.TargetBlob,
		targetOffset:   edge.TargetOffset,
		confidence:     uint64(edge.Confidence),
		evidenceBlob:   edge.Evidence.BlobSHA,
		evidence:       edge.Evidence.ByteOffset,
		evidenceLength: edge.Evidence.ByteLength,
	}
	if _, duplicate := e.seen[record]; duplicate {
		return nil
	}
	e.seen[record] = struct{}{}
	return e.builder.AddEdge(key, edge)
}

// ---------------------------------------------------------------------------
// openGraphShards — low-level shard opener used by manifest edges
// ---------------------------------------------------------------------------

// openGraphShards opens every shard under dir for an offline graph pass, sharing
// the deduped content store when the shard dir has one. The returned closer
// releases every mmap in reverse order and must be called even on error paths.
func openGraphShards(dir string) (paths []string, idxs []*index.Index, closeAll func() error, err error) {
	paths, err = globShards(dir)
	if err != nil {
		return nil, nil, nil, err
	}
	content, err := openSharedContent(dir, paths)
	if err != nil {
		return nil, nil, nil, err
	}
	var closers []io.Closer
	closeAll = func() error {
		var closeErr error
		for i := len(closers) - 1; i >= 0; i-- {
			if err := closers[i].Close(); err != nil && closeErr == nil {
				closeErr = fmt.Errorf("server: close graph shard mmap: %w", err)
			}
		}
		closers = nil
		if content != nil {
			if err := content.Close(); err != nil && closeErr == nil {
				closeErr = fmt.Errorf("server: close graph content mmap: %w", err)
			}
			content = nil
		}
		return closeErr
	}
	defer func() {
		if err != nil {
			_ = closeAll()
		}
	}()

	idxs = make([]*index.Index, 0, len(paths))
	for _, shardPath := range paths {
		var (
			ix     *index.Index
			closer io.Closer
		)
		if content != nil && diskstore.IsDeduped(shardPath) {
			ix, closer, err = diskstore.LoadMmapDeduped(shardPath, content)
		} else {
			ix, closer, err = diskstore.LoadMmap(shardPath)
		}
		if err != nil {
			return nil, nil, nil, fmt.Errorf("server: load graph shard %s: %w", shardPath, err)
		}
		closers = append(closers, closer)
		idxs = append(idxs, ix)
	}
	return paths, idxs, closeAll, nil
}

// ---------------------------------------------------------------------------
// persistedGraphEdge — dedup key
// ---------------------------------------------------------------------------

// persistedGraphEdge is the content-addressed identity used to collapse the
// duplicate candidates produced when identical source/target blobs occur in
// several shards. Generation is deliberately absent: it records when an edge was
// computed, not which edge it is.
type persistedGraphEdge struct {
	sourceBlob     string
	sourceOffset   uint64
	name           string
	typeID         diskgraph.EdgeType
	targetBlob     string
	targetOffset   uint64
	confidence     uint64
	evidenceBlob   string
	evidence       uint64
	evidenceLength uint64
	similarity     uint64
}

// ---------------------------------------------------------------------------
// edge classification
// ---------------------------------------------------------------------------

func graphEdgeType(edge graphverify.Edge, source []byte) diskgraph.EdgeType {
	if edge.Edge.Type == candidates.SiblingDefinition {
		return diskgraph.EdgeSiblingDefinition
	}
	if edge.Confidence == graphverify.Pattern {
		if typ := messagingEdgeType(edge, source); typ != diskgraph.EdgeUnknown {
			return typ
		}
	}
	switch edge.Kind {
	case graphverify.Call:
		return diskgraph.EdgeCalls
	case graphverify.Import:
		return diskgraph.EdgeImports
	case graphverify.TypeReference:
		return diskgraph.EdgeUsesType
	case graphverify.IdentifierMatch:
		return diskgraph.EdgeReferences
	default:
		return diskgraph.EdgeCandidate
	}
}

// messagingEdgeType recognizes the two MassTransit relationships needed by
// trace_consumers.
func messagingEdgeType(edge graphverify.Edge, content []byte) diskgraph.EdgeType {
	start, end := edge.Source.Start, edge.Source.End
	if start < 0 || end < start || end > len(content) {
		return diskgraph.EdgeUnknown
	}
	windowStart := start - 512
	if windowStart < 0 {
		windowStart = 0
	}
	windowEnd := end + 128
	if windowEnd > len(content) {
		windowEnd = len(content)
	}
	before := string(content[windowStart:start])
	after := string(content[end:windowEnd])

	if marker := strings.LastIndex(before, "IConsumer"); marker >= 0 {
		between := before[marker+len("IConsumer"):]
		if open := strings.LastIndexByte(between, '<'); open >= 0 &&
			!strings.ContainsRune(between[open+1:], '>') && closesTypeArgument(after) {
			return diskgraph.EdgeConsumes
		}
	}

	if marker := strings.LastIndex(before, "Publish"); marker >= 0 {
		between := before[marker+len("Publish"):]
		if !strings.ContainsAny(between, ";{}") &&
			(strings.ContainsRune(between, '<') || strings.ContainsRune(between, '(')) {
			return diskgraph.EdgePublishes
		}
	}
	return diskgraph.EdgeUnknown
}

func closesTypeArgument(after string) bool {
	for _, r := range after {
		switch {
		case r == '>' || r == ',':
			return true
		case r == '.' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			continue
		case r == ' ' || r == '\t' || r == '\r' || r == '\n':
			continue
		default:
			return false
		}
	}
	return false
}
