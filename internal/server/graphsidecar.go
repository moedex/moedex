package server

// graphsidecar.go is the offline bridge from the graph candidate/verification
// pipeline to the mmap-backed adjacency format. It intentionally builds from
// the per-shard indices (rather than loadUnified): candidate sites carry
// shard-local blob IDs until they are folded to content identity by blob SHA.

import (
	"fmt"
	"io"
	"math"
	"path/filepath"
	"sort"

	"moedex/internal/diskstore"
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

// GraphSidecarPath returns the default graph sidecar path under dir.
func GraphSidecarPath(dir string) string { return filepath.Join(dir, GraphSidecarName) }

// BuildGraphSidecar generates, verifies, and persists the graph for every
// exported trigram-length symbol name in dir's shard set. Posting lists and, for
// deduped shards, content stay mmap-backed during the offline sweep.
func BuildGraphSidecar(dir string) (path string, err error) {
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
	for _, name := range names {
		for _, scored := range graphverify.Verify(candidates.GenerateCandidates(corpus, name)) {
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
				Type:           graphEdgeType(scored),
				TargetBlob:     targetBlob.SHA,
				TargetOffset:   uint64(scored.Target.Start),
				Confidence:     scored.Confidence,
				EvidenceOffset: uint64(scored.Source.Start),
			}
			record := persistedGraphEdge{
				sourceBlob:   sourceBlob.SHA,
				sourceOffset: sourceOffset,
				typeID:       edge.Type,
				targetBlob:   edge.TargetBlob,
				targetOffset: edge.TargetOffset,
				confidence:   math.Float64bits(edge.Confidence),
				evidence:     edge.EvidenceOffset,
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
	sourceBlob   string
	sourceOffset uint64
	typeID       diskgraph.EdgeType
	targetBlob   string
	targetOffset uint64
	confidence   uint64
	evidence     uint64
}

func graphEdgeType(edge graphverify.Edge) diskgraph.EdgeType {
	if edge.Edge.Type == candidates.SiblingDefinition {
		return diskgraph.EdgeSiblingDefinition
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
