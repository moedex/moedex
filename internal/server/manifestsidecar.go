package server

// manifestsidecar.go is the offline bridge from package manifests to the graph's
// DEPENDS_ON adjacency. It is the phase-8 counterpart of graphbuild.go: same
// shard set, same graph, a different and much stronger
// kind of evidence. Where a symbol-derived edge is a name occurrence a regex tier
// scored, a manifest edge is two declarations joined, so it is persisted at
// Proven confidence.

import (
	"fmt"

	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/graph/manifest"
	"moedex/internal/index"
)

// manifestNodeOffset keys a manifest's adjacency at the file rather than at a
// position inside it: no symbol declares a repo's dependencies, and every
// dependency of one manifest belongs under one node so a single lookup answers
// "what does this manifest depend on?". Each declaration's own byte offset
// survives as the edge's evidence offset.
const manifestNodeOffset = 0

// ManifestEdges opens dir's shard set and returns the cross-repo dependency edges
// its package manifests declare, with the sweep's accounting. Unresolvable
// declarations — every third-party package — are counted in the Report, not
// returned and not errors.
func ManifestEdges(dir string) (edges []manifest.Edge, report manifest.Report, err error) {
	_, idxs, closeShards, err := openGraphShards(dir)
	if err != nil {
		return nil, manifest.Report{}, err
	}
	defer func() {
		if closeErr := closeShards(); err == nil {
			err = closeErr
		}
	}()
	edges, report = collectManifests(idxs).Resolve()
	return edges, report, nil
}

// ManifestGraph is the repo-level rollup of ManifestEdges: which repos depend on
// which, per their own manifests.
func ManifestGraph(dir string) (*manifest.Graph, manifest.Report, error) {
	edges, report, err := ManifestEdges(dir)
	if err != nil {
		return nil, report, err
	}
	return manifest.NewGraph(edges), report, nil
}

// collectManifests feeds every manifest file in the shard set to a resolver.
func collectManifests(idxs []*index.Index) *manifest.Builder {
	builder := manifest.NewBuilder()
	seen := make(map[manifestSite]struct{})
	for _, ix := range idxs {
		for id := 0; id < ix.NumBlobs(); id++ {
			blob := ix.Blob(uint64(id))
			if blob == nil {
				continue
			}
			for _, file := range blob.Files {
				if manifest.KindOf(file.RelPath) == manifest.Unknown {
					continue
				}
				site := manifestSite{repo: file.Repo, path: file.RelPath, blob: blob.SHA}
				if _, duplicate := seen[site]; duplicate {
					continue
				}
				seen[site] = struct{}{}
				builder.AddFile(file.Repo, blob.SHA, file.RelPath, blob.Content)
			}
		}
	}
	return builder
}

// manifestSite is the identity of one manifest occurrence across the shard set.
type manifestSite struct {
	repo string
	path string
	blob string
}

// addManifestEdges resolves the shard set's manifests and appends their
// DEPENDS_ON edges to an in-progress graph build, reusing the caller's
// deduplication map so an edge repeated through shard overlap is stored once.
func addManifestEdges(builder *diskgraph.Builder, seen map[persistedGraphEdge]struct{}, idxs []*index.Index) (manifest.Report, error) {
	edges, report := collectManifests(idxs).Resolve()
	for _, resolved := range edges {
		if resolved.Source.Blob == "" || resolved.Target.Blob == "" {
			return report, fmt.Errorf("server: manifest edge %q has no content identity", resolved.Declaration.Name)
		}
		if resolved.Source.Offset < 0 || resolved.Target.Offset < 0 {
			return report, fmt.Errorf("server: manifest edge %q has a negative byte offset", resolved.Declaration.Name)
		}
		evidenceLen := uint64(len(resolved.Declaration.Name))
		if evidenceLen == 0 {
			evidenceLen = 1
		}
		edge := diskgraph.Edge{
			Type:         diskgraph.EdgeDependsOn,
			TargetBlob:   resolved.Target.Blob,
			TargetOffset: uint64(resolved.Target.Offset),
			Confidence:   graph.Proven,
			Evidence: graph.Evidence{
				BlobSHA:    resolved.Source.Blob,
				ByteOffset: uint64(resolved.Source.Offset),
				ByteLength: evidenceLen,
			},
		}
		record := persistedGraphEdge{
			sourceBlob:     resolved.Source.Blob,
			sourceOffset:   manifestNodeOffset,
			typeID:         edge.Type,
			targetBlob:     edge.TargetBlob,
			targetOffset:   edge.TargetOffset,
			confidence:     uint64(edge.Confidence),
			evidenceBlob:   edge.Evidence.BlobSHA,
			evidence:       edge.Evidence.ByteOffset,
			evidenceLength: edge.Evidence.ByteLength,
		}
		if _, duplicate := seen[record]; duplicate {
			continue
		}
		seen[record] = struct{}{}
		if err := builder.Add(resolved.Source.Blob, manifestNodeOffset, edge); err != nil {
			return report, err
		}
	}
	return report, nil
}
