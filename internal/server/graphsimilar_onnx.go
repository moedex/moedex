//go:build onnx

package server

import (
	"context"
	"fmt"
	"math"

	"moedex/internal/embed"
	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
	"moedex/internal/symbol"
)

type similarDefinition struct {
	key      diskgraph.Key
	evidence graph.Evidence
}

// addSimilarToEdges embeds each unique content-addressed definition exactly
// once through embed.BuildStore, performs an exact top-K cosine pass over that
// store, and appends directed SIMILAR_TO adjacency records. The surrounding
// file's build constraint keeps the entire pass out of pure-Go builds.
func addSimilarToEdges(ctx context.Context, builder *diskgraph.Builder, merged *symbol.Corpus, shards []*index.Index, seen map[persistedGraphEdge]struct{}, opts GraphBuildOptions) error {
	if opts.SimilarTopK == 0 {
		return nil
	}
	if opts.Embedder == nil {
		return fmt.Errorf("server: SIMILAR_TO graph build requires an embedder")
	}

	definitions := make([]similarDefinition, 0)
	definitionIDs := make(map[diskgraph.Key]struct{})
	synthetic := index.New()
	for shardID, ix := range shards {
		if ix == nil {
			continue
		}
		for blobID := uint64(0); blobID < uint64(ix.NumBlobs()); blobID++ {
			blob := ix.Blob(blobID)
			if blob == nil {
				continue
			}
			for _, definition := range merged.Symbols(shardID, blobID) {
				if definition.NameStart < 0 || definition.BodyStart < 0 || definition.BodyEnd <= definition.BodyStart || definition.BodyEnd > len(blob.Content) {
					return fmt.Errorf("server: symbol %q has invalid embedding span [%d,%d) in blob %s", definition.Name, definition.BodyStart, definition.BodyEnd, blob.SHA)
				}
				key := diskgraph.Key{BlobSHA: blob.SHA, SymbolOffset: uint64(definition.NameStart)}
				if _, duplicate := definitionIDs[key]; duplicate {
					continue
				}
				definitionIDs[key] = struct{}{}
				if err := builder.AddNode(key); err != nil {
					return err
				}
				id := len(definitions)
				definitions = append(definitions, similarDefinition{
					key: key,
					evidence: graph.Evidence{
						BlobSHA:    blob.SHA,
						ByteOffset: uint64(definition.BodyStart),
						ByteLength: uint64(definition.BodyEnd - definition.BodyStart),
					},
				})
				// A unique synthetic SHA preserves one blob/chunk per definition even
				// when two definition bodies are byte-identical. BuildStore still
				// deduplicates the embedding request by its content key.
				synthetic.AddFileUnindexed("graph", fmt.Sprintf("symbol-%d", id), "", fmt.Sprintf("graph-symbol-%d", id), blob.Content[definition.BodyStart:definition.BodyEnd])
			}
		}
	}
	if len(definitions) < 2 {
		return nil
	}

	store, err := embed.BuildStore(ctx, synthetic, opts.Embedder, 0, 0)
	if err != nil {
		return fmt.Errorf("server: embed graph symbols: %w", err)
	}
	pairs, err := store.Similar(ctx, opts.SimilarTopK, float32(opts.SimilarThreshold))
	if err != nil {
		return fmt.Errorf("server: compare graph symbols: %w", err)
	}
	for _, pair := range pairs {
		if pair.Source.Blob >= uint64(len(definitions)) || pair.Target.Blob >= uint64(len(definitions)) {
			return fmt.Errorf("server: similarity result references unknown synthetic symbol")
		}
		sourceDefinition := definitions[pair.Source.Blob]
		source := sourceDefinition.key
		target := definitions[pair.Target.Blob].key
		edge := diskgraph.Edge{
			Type:         diskgraph.EdgeSimilarTo,
			TargetBlob:   target.BlobSHA,
			TargetOffset: target.SymbolOffset,
			Confidence:   graph.Candidate,
			Evidence:     sourceDefinition.evidence,
			Similarity:   float64(pair.Score),
		}
		record := persistedGraphEdge{
			sourceBlob:     source.BlobSHA,
			sourceOffset:   source.SymbolOffset,
			typeID:         edge.Type,
			targetBlob:     edge.TargetBlob,
			targetOffset:   edge.TargetOffset,
			confidence:     uint64(edge.Confidence),
			evidenceBlob:   edge.Evidence.BlobSHA,
			evidence:       edge.Evidence.ByteOffset,
			evidenceLength: edge.Evidence.ByteLength,
			similarity:     math.Float64bits(edge.Similarity),
		}
		if _, duplicate := seen[record]; duplicate {
			continue
		}
		seen[record] = struct{}{}
		if err := builder.AddEdge(source, edge); err != nil {
			return err
		}
	}
	return nil
}
