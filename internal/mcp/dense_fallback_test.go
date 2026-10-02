package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"moedex/internal/contextwin"
	"moedex/internal/embed"
	"moedex/internal/index"
	"moedex/internal/rank"
	"moedex/internal/tokenindex"
)

type fallbackTestEmbedder struct{ err error }

func (e fallbackTestEmbedder) Dim() int { return 2 }
func (e fallbackTestEmbedder) Embed(_ context.Context, texts []string) ([]embed.Vector, error) {
	if e.err != nil {
		return nil, e.err
	}
	out := make([]embed.Vector, len(texts))
	for i := range out {
		out[i] = embed.Vector{1, 0}
	}
	return out, nil
}

func TestAgentDenseFallbackWarningAndCachePolicy(t *testing.T) {
	ix := index.New()
	ix.AddFile("repo", "source.go", "/repo/source.go", "sha", []byte("package source\nfunc Lookup() {}\n"))
	store, err := embed.BuildStore(context.Background(), ix, fallbackTestEmbedder{}, 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ranker := rank.New(ix, tokenindex.Build(ix), store, fallbackTestEmbedder{errors.New("private-host.example secret-token")}, rank.Config{DenseMinQueryTerms: -1})
	ranker.UseTokenCandidates(true)
	s := NewServer(NewIndexSearcher(ix, ranker, 5))
	for _, query := range []string{"Lookup", "unmatchable"} {
		for _, format := range []string{"text", "structured"} {
			t.Run(query+"/"+format, func(t *testing.T) {
				params, _ := json.Marshal(map[string]interface{}{"name": "search_context", "arguments": map[string]interface{}{"query": query, "format": format}})
				result, err := s.callTool(context.Background(), params)
				if err != nil || result["isError"] == true {
					t.Fatalf("result=%v err=%v", result, err)
				}
				if resultSnapshot(result).Cacheable {
					t.Fatal("degraded answer was cacheable")
				}
				payload, _ := json.Marshal(result["structuredContent"])
				var structured map[string]interface{}
				if err := json.Unmarshal(payload, &structured); err != nil {
					t.Fatal(err)
				}
				validateFixture(t, OutputSchema("search_context"), structured)
				warnings := structured["summary"].(map[string]interface{})["warnings"].([]interface{})
				if len(warnings) != 1 || warnings[0] != contextwin.WarningDenseUnavailable {
					t.Fatalf("warnings=%v", warnings)
				}
				blocks := structured["blocks"].([]interface{})
				if (len(blocks) > 0) != (query == "Lookup") {
					t.Fatalf("blocks=%v", blocks)
				}
				for _, block := range blocks {
					if block.(map[string]interface{})["dense"] != float64(0) {
						t.Fatal("failed arm advertised dense score")
					}
				}
				text := result["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
				if !strings.Contains(text, "dense_unavailable") {
					t.Fatalf("missing visible warning: %s", text)
				}
				encoded, _ := json.Marshal(result)
				if strings.Contains(string(encoded), "private-host") || strings.Contains(string(encoded), "secret-token") {
					t.Fatal("backend details leaked")
				}
			})
		}
	}
	// A missing or query-gated dense arm is normal operation, not an outage.
	for _, healthyRanker := range []*rank.Ranker{
		rank.New(ix, tokenindex.Build(ix), nil, nil, rank.Config{}),
		rank.New(ix, tokenindex.Build(ix), store, fallbackTestEmbedder{errors.New("should not run")}, rank.Config{DenseMinQueryTerms: 99}),
	} {
		healthyRanker.UseTokenCandidates(true)
		healthy := NewIndexSearcher(ix, healthyRanker, 5)
		result, err := healthy.SearchContextWithSnapshot(context.Background(), "Lookup", 100, 5)
		if err != nil || len(result.Window.Warnings) != 0 || !result.Snapshot.Cacheable {
			t.Fatalf("healthy result=%+v err=%v", result, err)
		}
	}
}
