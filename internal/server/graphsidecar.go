package server

// graphsidecar.go is the offline bridge from the graph candidate/verification
// pipeline to the mmap-backed adjacency format. It intentionally builds from
// the per-shard indices (rather than loadUnified): candidate sites carry
// shard-local blob IDs until they are folded to content identity by blob SHA.

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
	graphverify "moedex/internal/graph/verify"
	"moedex/internal/index"
	"moedex/internal/symbol"
	"moedex/internal/trigram"
)

// GraphSidecarName is the default graph adjacency sidecar written next to the
// corpus token and symbol sidecars.
const GraphSidecarName = "corpus-graph.graph"

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

// GraphSidecarPath returns the default graph sidecar path under dir.
func GraphSidecarPath(dir string) string { return filepath.Join(dir, GraphSidecarName) }

// BuildGraphSidecar generates, verifies, and persists the graph for every
// exported trigram-length symbol name in dir's shard set. Posting lists and, for
// deduped shards, content stay mmap-backed during the offline sweep.
func BuildGraphSidecar(dir string) (path string, err error) {
	return BuildGraphSidecarWithOptions(dir, GraphBuildOptions{})
}

// BuildGraphSidecarWithOptions is BuildGraphSidecar plus optional tagged graph
// passes such as ONNX-backed semantic similarity.
func BuildGraphSidecarWithOptions(dir string, opts GraphBuildOptions) (path string, err error) {
	if opts.SimilarTopK < 0 {
		return "", fmt.Errorf("server: graph similar top-K must be non-negative")
	}
	if math.IsNaN(opts.SimilarThreshold) || opts.SimilarThreshold < -1 || opts.SimilarThreshold > 1 {
		return "", fmt.Errorf("server: graph similarity threshold %g is outside [-1,1]", opts.SimilarThreshold)
	}
	if math.IsNaN(opts.LSPRequestsPerSecond) || math.IsInf(opts.LSPRequestsPerSecond, 0) || opts.LSPRequestsPerSecond < 0 {
		return "", fmt.Errorf("server: graph LSP requests/second must be finite and non-negative")
	}
	if opts.LSPRequestTimeout < 0 {
		return "", fmt.Errorf("server: graph LSP request timeout must be non-negative")
	}
	if opts.LSPStats != nil {
		*opts.LSPStats = LSPGraphStats{}
	}
	paths, err := globShards(dir)
	if err != nil {
		return "", err
	}
	content, err := openSharedContent(dir, paths)
	if err != nil {
		return "", err
	}
	var closers []io.Closer
	defer func() {
		for i := len(closers) - 1; i >= 0; i-- {
			if closeErr := closers[i].Close(); err == nil && closeErr != nil {
				err = fmt.Errorf("server: close graph shard mmap: %w", closeErr)
			}
		}
		if content != nil {
			if closeErr := content.Close(); err == nil && closeErr != nil {
				err = fmt.Errorf("server: close graph content mmap: %w", closeErr)
			}
		}
	}()

	idxs := make([]*index.Index, 0, len(paths))
	merged := symbol.NewCorpus()
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
			return "", fmt.Errorf("server: load graph shard %s: %w", shardPath, err)
		}
		closers = append(closers, closer)
		idxs = append(idxs, ix)
		merged.AddShard(filepath.Base(shardPath), symbol.BuildMulti(ix))
	}

	corpus, err := candidates.NewCorpus(merged, idxs...)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, merged.NumNames())
	merged.EachName(func(name string) bool {
		if len(name) >= trigram.N && candidates.Exported(name) {
			names = append(names, name)
		}
		return true
	})
	sort.Strings(names)

	builder := diskgraph.NewBuilder()
	seen := make(map[persistedGraphEdge]struct{})
	crossRelevant := make(map[string]bool)
	for _, name := range names {
		// Definitions are graph nodes even when nothing references them. Keeping
		// them in the sidecar is what lets clustering return isolated services as
		// singleton communities instead of silently dropping them.
		for _, definition := range merged.Definitions(name) {
			if definition.Shard < 0 || definition.Shard >= len(idxs) {
				return "", fmt.Errorf("server: graph definition %q references unknown shard %d", name, definition.Shard)
			}
			blob := idxs[definition.Shard].Blob(definition.Blob)
			if blob == nil {
				return "", fmt.Errorf("server: graph definition %q references an unknown blob", name)
			}
			if definition.Start < 0 {
				return "", fmt.Errorf("server: graph definition %q has a negative byte offset", name)
			}
			if err := builder.AddNode(diskgraph.Key{BlobSHA: blob.SHA, SymbolOffset: uint64(definition.Start)}); err != nil {
				return "", err
			}
		}
		generated := candidates.GenerateCandidates(corpus, name)
		for _, candidate := range generated {
			if candidate.CrossShard() {
				crossRelevant[name] = true
				break
			}
		}
		for _, scored := range graphverify.Verify(generated) {
			sourceBlob := corpus.Blob(scored.Source)
			targetBlob := corpus.Blob(scored.Target)
			if sourceBlob == nil || targetBlob == nil {
				return "", fmt.Errorf("server: graph edge %q references an unknown blob", name)
			}
			if scored.Source.Start < 0 || scored.Target.Start < 0 {
				return "", fmt.Errorf("server: graph edge %q has a negative byte offset", name)
			}

			sourceOffset := uint64(scored.Source.Start)
			if enclosing, ok := merged.Enclosing(scored.Source.Shard, scored.Source.Blob, scored.Source.Start); ok {
				if enclosing.NameStart < 0 {
					return "", fmt.Errorf("server: enclosing symbol %q has a negative byte offset", enclosing.Name)
				}
				sourceOffset = uint64(enclosing.NameStart)
			}
			edge := diskgraph.Edge{
				Type:         graphEdgeType(scored, sourceBlob.Content),
				TargetBlob:   targetBlob.SHA,
				TargetOffset: uint64(scored.Target.Start),
				Confidence:   scored.Confidence,
				Evidence:     scored.Evidence,
			}
			record := persistedGraphEdge{
				sourceBlob:     sourceBlob.SHA,
				sourceOffset:   sourceOffset,
				typeID:         edge.Type,
				targetBlob:     edge.TargetBlob,
				targetOffset:   edge.TargetOffset,
				confidence:     uint64(edge.Confidence),
				evidenceBlob:   edge.Evidence.BlobSHA,
				evidence:       edge.Evidence.ByteOffset,
				evidenceLength: edge.Evidence.ByteLength,
			}
			if _, duplicate := seen[record]; duplicate {
				continue // the same content may appear in several shards
			}
			seen[record] = struct{}{}
			if err := builder.Add(sourceBlob.SHA, sourceOffset, edge); err != nil {
				return "", err
			}
		}
	}
	if err := addLSPCallEdges(context.Background(), builder, merged, idxs, seen, crossRelevant, opts); err != nil {
		return "", err
	}
	if err := addSimilarToEdges(context.Background(), builder, merged, idxs, seen, opts); err != nil {
		return "", err
	}

	path = GraphSidecarPath(dir)
	if err := builder.Save(path); err != nil {
		return "", fmt.Errorf("server: persist graph sidecar: %w", err)
	}
	return path, nil
}

// persistedGraphEdge is the content-addressed identity used to collapse the
// duplicate candidates produced when identical source/target blobs occur in
// several shards. Confidence is compared by bits so the key remains exact.
type persistedGraphEdge struct {
	sourceBlob     string
	sourceOffset   uint64
	typeID         diskgraph.EdgeType
	targetBlob     string
	targetOffset   uint64
	confidence     uint64
	evidenceBlob   string
	evidence       uint64
	evidenceLength uint64
	similarity     uint64
}

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
// trace_consumers. Verification has already proved the occurrence is code (not
// a comment/string) and identifier-delimited; this pass only specializes that
// verified type use from its nearby framework syntax.
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

	// IConsumer<Foo> / IConsumer<Namespace.Foo> associates the containing
	// consumer definition with Foo. Requiring the most recent '<' to remain
	// unclosed before the occurrence avoids matching an unrelated earlier use.
	if marker := strings.LastIndex(before, "IConsumer"); marker >= 0 {
		between := before[marker+len("IConsumer"):]
		if open := strings.LastIndexByte(between, '<'); open >= 0 &&
			!strings.ContainsRune(between[open+1:], '>') && closesTypeArgument(after) {
			return diskgraph.EdgeConsumes
		}
	}

	// Publish<Foo>(...) and Publish(new Foo(...)) are the two common typed
	// publisher forms. The candidate name itself is the event definition, so a
	// Publish(message) call with only a runtime variable correctly cannot invent
	// an event edge.
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
