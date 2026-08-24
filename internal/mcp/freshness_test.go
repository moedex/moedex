package mcp

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"moedex/internal/contextwin"
	"moedex/internal/graph"
)

type pausingSnapshotSearcher struct {
	mu      sync.Mutex
	current SnapshotIdentity
	started chan struct{}
	release chan struct{}
	window  contextwin.ContextWindow
}

func (s *pausingSnapshotSearcher) SearchContext(context.Context, string, int, int) (contextwin.ContextWindow, error) {
	return s.window, nil
}

func (s *pausingSnapshotSearcher) SearchContextWithSnapshot(ctx context.Context, _ string, _, _ int) (ContextSearchResult, error) {
	s.mu.Lock()
	captured := s.current
	s.mu.Unlock()
	close(s.started)
	select {
	case <-s.release:
		return ContextSearchResult{Window: s.window, Snapshot: captured}, nil
	case <-ctx.Done():
		return ContextSearchResult{}, ctx.Err()
	}
}

func (s *pausingSnapshotSearcher) swap(identity SnapshotIdentity) {
	s.mu.Lock()
	s.current = identity
	s.mu.Unlock()
}

type pausingSnapshotAnnotator struct {
	mu      sync.Mutex
	current SnapshotIdentity
	started chan struct{}
	release chan struct{}
}

func (a *pausingSnapshotAnnotator) Neighbors(context.Context, []contextwin.ContextBlock, int) ([]BlockNeighbors, error) {
	return []BlockNeighbors{NewBlockNeighbors()}, nil
}

func (a *pausingSnapshotAnnotator) NeighborsWithSnapshot(ctx context.Context, blocks []contextwin.ContextBlock, _ int, _ graph.ConfidenceTier) (GraphAnnotationResult, error) {
	a.mu.Lock()
	captured := a.current
	a.mu.Unlock()
	close(a.started)
	select {
	case <-a.release:
		neighbors := make([]BlockNeighbors, len(blocks))
		for i := range neighbors {
			neighbors[i] = NewBlockNeighbors()
		}
		return GraphAnnotationResult{Neighbors: neighbors, Snapshot: captured}, nil
	case <-ctx.Done():
		return GraphAnnotationResult{}, ctx.Err()
	}
}

func (a *pausingSnapshotAnnotator) swap(identity SnapshotIdentity) {
	a.mu.Lock()
	a.current = identity
	a.mu.Unlock()
}

func TestSearchContextStampsSnapshotsAcquiredBeforeReload(t *testing.T) {
	searcher := &pausingSnapshotSearcher{
		current: SnapshotIdentity{Cacheable: true, CorpusFingerprint: "rank-old", BlobSHAs: []string{"source-old"}},
		started: make(chan struct{}), release: make(chan struct{}),
		window: contextwin.ContextWindow{Blocks: []contextwin.ContextBlock{{
			BlobSHA: "source-old", Repo: "fixture", RelPath: "old.go", Text: "package fixture\n",
		}}},
	}
	annotator := &pausingSnapshotAnnotator{
		current: SnapshotIdentity{Cacheable: true, CorpusFingerprint: "graph-old", GraphGeneration: 5, GraphBuildID: "build-old", BlobSHAs: []string{"evidence-old"}},
		started: make(chan struct{}), release: make(chan struct{}),
	}
	server := NewServer(searcher, WithGraphAnnotator(annotator))
	resultCh := make(chan map[string]interface{}, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := server.callTool(context.Background(), json.RawMessage(`{"name":"search_context","arguments":{"query":"fixture"}}`))
		resultCh <- result
		errCh <- err
	}()

	<-searcher.started
	searcher.swap(SnapshotIdentity{Cacheable: true, CorpusFingerprint: "rank-new", BlobSHAs: []string{"source-new"}})
	close(searcher.release)

	<-annotator.started
	annotator.swap(SnapshotIdentity{Cacheable: true, CorpusFingerprint: "graph-new", GraphGeneration: 6, GraphBuildID: "build-new", BlobSHAs: []string{"evidence-new"}})
	close(annotator.release)

	result := <-resultCh
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	identity := resultSnapshot(result)
	if identity.CorpusFingerprint != "rank-old" || identity.GraphCorpusFingerprint != "graph-old" || identity.GraphGeneration != 5 || identity.GraphBuildID != "build-old" {
		t.Fatalf("response was stamped from a later snapshot: %+v", identity)
	}
	for _, want := range []string{"evidence-old", "source-old"} {
		if !containsString(identity.BlobSHAs, want) {
			t.Errorf("blob_shas omitted %q: %v", want, identity.BlobSHAs)
		}
	}
	for _, stale := range []string{"evidence-new", "source-new"} {
		if containsString(identity.BlobSHAs, stale) {
			t.Errorf("blob_shas leaked post-reload identity %q: %v", stale, identity.BlobSHAs)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
