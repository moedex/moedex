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
		current: SnapshotIdentity{Cacheable: true, CorpusFingerprint: "corpus-old", BlobSHAs: []string{"source-old"}},
		started: make(chan struct{}), release: make(chan struct{}),
		window: contextwin.ContextWindow{Blocks: []contextwin.ContextBlock{{
			BlobSHA: "source-old", Repo: "fixture", RelPath: "old.go", Text: "package fixture\n",
		}}},
	}
	annotator := &pausingSnapshotAnnotator{
		current: SnapshotIdentity{Cacheable: true, CorpusFingerprint: "corpus-old", GraphGeneration: 5, GraphBuildID: "build-old", BlobSHAs: []string{"evidence-old"}},
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
	if result["isError"] == true {
		t.Fatalf("compatible acquired snapshots rejected: %v", result)
	}
	identity := resultSnapshot(result)
	if identity.CorpusFingerprint != "corpus-old" || identity.GraphCorpusFingerprint != "" || identity.GraphGeneration != 5 || identity.GraphBuildID != "build-old" {
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

// Snapshot adapters deliberately return different graph builds for the same
// corpus: graph publication alone does not invalidate source compatibility.
type fixedSnapshotSearcher struct{ ContextSearchResult }

func (s fixedSnapshotSearcher) SearchContext(context.Context, string, int, int) (contextwin.ContextWindow, error) {
	return s.Window, nil
}
func (s fixedSnapshotSearcher) SearchContextWithSnapshot(context.Context, string, int, int) (ContextSearchResult, error) {
	return s.ContextSearchResult, nil
}

type fixedSnapshotAnnotator struct{ GraphAnnotationResult }

func (a fixedSnapshotAnnotator) Neighbors(context.Context, []contextwin.ContextBlock, int) ([]BlockNeighbors, error) {
	return a.GraphAnnotationResult.Neighbors, nil
}
func (a fixedSnapshotAnnotator) NeighborsWithSnapshot(context.Context, []contextwin.ContextBlock, int, graph.ConfidenceTier) (GraphAnnotationResult, error) {
	return a.GraphAnnotationResult, nil
}

func TestSearchSnapshotCompatibilityAcrossTransports(t *testing.T) {
	for _, transport := range []string{"direct", "stdio", "http"} {
		for _, tc := range []struct {
			name, graphCorpus string
			depth             int
			wantError         bool
		}{
			{"mixed generations", "new", 1, true},
			{"same corpus new graph build", "old", 1, false},
			{"source only during refresh", "new", 0, false},
		} {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				s := NewServer(fixedSnapshotSearcher{ContextSearchResult{
					Window:   contextwin.ContextWindow{Blocks: []contextwin.ContextBlock{{BlobSHA: "old-sha", Text: "old source"}}},
					Snapshot: SnapshotIdentity{Cacheable: true, CorpusFingerprint: "old"},
				}}, WithGraphAnnotator(fixedSnapshotAnnotator{GraphAnnotationResult{
					Neighbors: []BlockNeighbors{NewBlockNeighbors()},
					Snapshot:  SnapshotIdentity{Cacheable: true, CorpusFingerprint: tc.graphCorpus, GraphGeneration: 9, GraphBuildID: "new-build"},
				}}))
				params := map[string]interface{}{"name": "search_context", "arguments": map[string]interface{}{"query": "source", "graph_depth": tc.depth}}
				request := map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params}
				var result map[string]interface{}
				switch transport {
				case "direct":
					raw, _ := json.Marshal(params)
					var err error
					result, err = s.callTool(context.Background(), raw)
					if err != nil {
						t.Fatal(err)
					}
				case "stdio":
					responses := drive(t, s, request)
					if len(responses) != 1 || responses[0].Error != nil {
						t.Fatalf("responses=%+v", responses)
					}
					result = responses[0].Result.(map[string]interface{})
				case "http":
					raw, _ := json.Marshal(request)
					response := decodeRPC(t, postRPC(t, s.HTTPHandler(), string(raw)))
					if response.Error != nil {
						t.Fatal(response.Error)
					}
					result = response.Result.(map[string]interface{})
				}
				if (result["isError"] == true) != tc.wantError {
					t.Fatalf("result=%v", result)
				}
				if tc.wantError {
					structured := result["structuredContent"].(map[string]interface{})
					if structured["error"].(map[string]interface{})["code"] != "snapshot_unavailable" {
						t.Fatalf("error=%v", structured)
					}
					if _, ok := structured["blocks"]; ok {
						t.Fatal("mixed source leaked in error")
					}
					// Decode through JSON so both typed direct and wire metadata are checked.
					raw, _ := json.Marshal(result["_meta"].(map[string]interface{})[SnapshotMetaKey])
					var identity SnapshotIdentity
					if err := json.Unmarshal(raw, &identity); err != nil {
						t.Fatal(err)
					}
					if identity.Cacheable || !identity.HasCorpusConflict() {
						t.Fatalf("identity=%+v", identity)
					}
				}
			})
		}
	}
}

func TestSearchSnapshotAcquisitionCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	searcher := &pausingSnapshotSearcher{
		current: SnapshotIdentity{Cacheable: true, CorpusFingerprint: "old"},
		started: make(chan struct{}), release: make(chan struct{}),
	}
	s := NewServer(searcher)
	done := make(chan error, 1)
	go func() {
		_, err := s.callTool(ctx, json.RawMessage(`{"name":"search_context","arguments":{"query":"source"}}`))
		done <- err
	}()
	<-searcher.started
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("cancellation became a snapshot failure: %v", err)
	}
}
