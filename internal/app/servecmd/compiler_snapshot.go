package servecmd

import (
	"context"
	"fmt"
	"path/filepath"

	graphserve "moedex/internal/graph/serve"
	"moedex/internal/mcp"
	"moedex/internal/semanticindex"
	server "moedex/internal/serve"
	"moedex/internal/snapshot"
)

// Only an explicitly resolved manifest can authorize a compiler query index.
// Legacy shard directories never infer a sibling attachment or mapping.
func (s *servingSnapshot) openCompiler(resolved snapshot.Resolved) error {
	if resolved.Manifest == nil || resolved.Legacy {
		return nil
	}
	if resolved.ID != resolved.Manifest.ID {
		return fmt.Errorf("serving snapshot: resolved ID differs from manifest")
	}
	dir, err := filepath.Abs(resolved.ShardDir())
	if err != nil {
		return err
	}
	if dir != s.rank.ShardDirectory() {
		return fmt.Errorf("serving snapshot: compiler and rank directories differ")
	}
	// Validate the component relation before indexing its fixed single artifact.
	if err := snapshot.Validate(resolved.Manifest); err != nil {
		return err
	}
	if err := snapshot.VerifySemanticComponent(resolved.Root, resolved.Manifest); err != nil {
		return err
	}
	s.compilerSnapshotID = resolved.ID
	s.compilerCorpusFingerprint = resolved.Manifest.CorpusFingerprint
	component, ok := resolved.Manifest.Components[snapshot.SemanticIndexComponent]
	if !ok {
		return nil
	}
	expected := semanticindex.Expected{ArtifactSHA256: component.Metadata["source_artifact_sha256"], CorpusFingerprint: resolved.Manifest.CorpusFingerprint}
	reader, err := semanticindex.Open(filepath.Join(resolved.Root, filepath.FromSlash(component.Artifacts[0].Path)), expected, semanticindex.Limits{})
	if err != nil {
		return err
	}
	s.compiler = reader
	s.compilerSnapshotID = resolved.ID
	s.compilerArtifactSHA = expected.ArtifactSHA256
	s.compilerCorpusFingerprint = expected.CorpusFingerprint
	return nil
}
func newServingHolderResolved(rank *server.RankCorpus, graph *graphserve.GraphToolset, resolved *snapshot.Resolved) (*servingHolder, error) {
	holder, err := newServingHolder(rank, graph)
	if err != nil {
		return nil, err
	}
	if resolved != nil {
		if err := holder.cur.openCompiler(*resolved); err != nil {
			return nil, err
		}
	}
	return holder, nil
}
func (h *servingHolder) AcquireCompilerSession(ctx context.Context) (mcp.CompilerSession, error) {
	s, release, err := h.acquire(ctx)
	if err != nil {
		return mcp.CompilerSession{}, err
	}
	session := mcp.CompilerSession{SnapshotID: s.compilerSnapshotID, ArtifactSHA256: s.compilerArtifactSHA, CorpusFingerprint: s.compilerCorpusFingerprint, Release: release}
	if s.compiler != nil {
		session.Reader = s.compiler
	}
	return session, nil
}
