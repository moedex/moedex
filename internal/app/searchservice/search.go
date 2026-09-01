// Package searchservice exposes the shared ranked-search application boundary
// used by the CLI and terminal UI.
package searchservice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"moedex/internal/contextwin"
	"moedex/internal/embed"
	"moedex/internal/graph"
	graphserve "moedex/internal/graph/serve"
	"moedex/internal/mcp"
	server "moedex/internal/serve"
	indexsnapshot "moedex/internal/snapshot"
)

// Options selects the published index and ranking/window defaults.
type Options struct {
	IndexDir    string
	ShardDir    string
	TokenBudget int
	TopK        int
	GraphDepth  int
}

// Result is one ranked search response ready for terminal or clipboard use.
type Result struct {
	Query          string
	Window         contextwin.ContextWindow
	Neighbors      []mcp.BlockNeighbors
	GraphAvailable bool
	Markdown       string
}

// Service owns a corpus ranker and its optional graph generation.
type Service struct {
	rank        *server.RankCorpus
	graph       *graphserve.GraphToolset
	embedCloser io.Closer
	defaults    Options
}

// Open loads the current published generation. Exactly one of IndexDir or
// ShardDir must be supplied.
func Open(ctx context.Context, opts Options) (*Service, error) {
	if opts.IndexDir != "" && opts.ShardDir != "" {
		return nil, errors.New("search: index-dir and shard-dir are mutually exclusive")
	}
	shardDir := opts.ShardDir
	if opts.IndexDir != "" {
		resolved, err := indexsnapshot.Resolve(opts.IndexDir)
		if err != nil {
			return nil, fmt.Errorf("search: resolve published index: %w", err)
		}
		shardDir = resolved.ShardDir()
	}
	if shardDir == "" {
		return nil, errors.New("search: no published index configured")
	}

	cfg := server.RankConfig{TopK: opts.TopK}
	closer, err := configureDense(&cfg, shardDir)
	if err != nil {
		return nil, err
	}
	ranker, err := server.OpenRank(ctx, shardDir, cfg)
	if err != nil {
		if closer != nil {
			_ = closer.Close()
		}
		return nil, fmt.Errorf("search: open ranker: %w", err)
	}
	graphTools, err := graphserve.OpenGraphTools(shardDir)
	if err != nil {
		_ = ranker.Close()
		if closer != nil {
			_ = closer.Close()
		}
		return nil, fmt.Errorf("search: open graph: %w", err)
	}
	return &Service{rank: ranker, graph: graphTools, embedCloser: closer, defaults: opts}, nil
}

func configureDense(cfg *server.RankConfig, shardDir string) (io.Closer, error) {
	kind := strings.ToLower(strings.TrimSpace(os.Getenv("MOEDEX_EMBED")))
	if kind == "" {
		kind = "auto"
	}
	url := os.Getenv("MOEDEX_EMBED_URL")
	runtimePath := embed.ResolveONNXRuntimePath("")
	if kind == "auto" {
		switch {
		case embed.ONNXCompiled && runtimePath != "":
			kind = "onnx"
		case url != "":
			kind = "http"
		default:
			kind = "none"
		}
	}
	if kind == "none" {
		return nil, nil
	}
	cfg.StorePath = filepath.Join(shardDir, "corpus-embeddings.store")
	switch kind {
	case "http":
		if url == "" {
			return nil, errors.New("search: MOEDEX_EMBED=http requires MOEDEX_EMBED_URL")
		}
		cfg.EmbedModel = os.Getenv("MOEDEX_EMBED_MODEL")
		cfg.Emb = embed.NewHTTPEmbedder(url, cfg.EmbedModel)
		return nil, nil
	case "onnx":
		if !embed.ONNXCompiled {
			return nil, errors.New("search: MOEDEX_EMBED=onnx requires a binary built with -tags onnx")
		}
		if runtimePath == "" {
			return nil, errors.New("search: ONNX Runtime library was not found; set ONNXRUNTIME_LIB_PATH")
		}
		emb, err := embed.NewONNXEmbedder(runtimePath)
		if err != nil {
			return nil, fmt.Errorf("search: open ONNX embedder: %w", err)
		}
		cfg.Emb = emb
		cfg.EmbedModel = "st-codesearch-distilroberta-onnx"
		return emb, nil
	default:
		return nil, fmt.Errorf("search: unknown MOEDEX_EMBED %q (want auto, none, http, or onnx)", kind)
	}
}

// Search runs ranked retrieval and graph annotation with the service defaults.
func (s *Service) Search(ctx context.Context, query string) (Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Result{}, errors.New("search: query is empty")
	}
	window, err := s.rank.SearchContext(ctx, query, s.defaults.TokenBudget, s.defaults.TopK)
	if err != nil {
		return Result{}, fmt.Errorf("search: %w", err)
	}
	depth := s.defaults.GraphDepth
	if depth == 0 {
		depth = 1
	}
	var neighbors []mcp.BlockNeighbors
	if s.graph != nil && s.graph.Available() {
		neighbors, err = s.graph.NeighborsWithConfidence(ctx, window.Blocks, depth, graph.DefaultMinConfidence)
		if err != nil {
			return Result{}, fmt.Errorf("search: graph annotations: %w", err)
		}
	}
	result := Result{
		Query:          query,
		Window:         window,
		Neighbors:      neighbors,
		GraphAvailable: s.graph != nil && s.graph.Available(),
	}
	result.Markdown = FormatMarkdown(result)
	return result, nil
}

// Close releases graph, corpus, and optional embedder resources.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	var errs []error
	if s.graph != nil {
		errs = append(errs, s.graph.Close())
	}
	if s.rank != nil {
		errs = append(errs, s.rank.Close())
	}
	if s.embedCloser != nil {
		errs = append(errs, s.embedCloser.Close())
	}
	return errors.Join(errs...)
}

// FormatMarkdown returns the exact terminal clipboard payload.
func FormatMarkdown(result Result) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# Moe search: %s\n\n", result.Query)
	fmt.Fprintf(&out, "%d context blocks · ~%d tokens", len(result.Window.Blocks), result.Window.TokenEstimate)
	if result.Window.Truncated {
		out.WriteString(" · truncated")
	}
	out.WriteString("\n\n")
	for i, block := range result.Window.Blocks {
		path := block.RelPath
		if block.Repo != "" {
			path = filepath.ToSlash(filepath.Join(block.Repo, block.RelPath))
		}
		if path == "" {
			path = block.AbsPath
		}
		fmt.Fprintf(&out, "## %d. `%s:%d-%d`\n\n", i+1, path, block.StartLine, block.EndLine)
		fmt.Fprintf(&out, "score %.4f · lexical %.4f", block.Score, block.Lexical)
		if block.Dense != 0 {
			fmt.Fprintf(&out, " · dense %.4f", block.Dense)
		}
		out.WriteString("\n\n")
		fmt.Fprintf(&out, "```%s\n%s", languageFor(path), block.Text)
		if !strings.HasSuffix(block.Text, "\n") {
			out.WriteByte('\n')
		}
		out.WriteString("```\n\n")
		if i < len(result.Neighbors) {
			formatNeighbors(&out, result.Neighbors[i])
		}
	}
	if !result.GraphAvailable {
		out.WriteString("_Graph sidecar unavailable; showing ranked context only._\n")
	}
	return out.String()
}

func formatNeighbors(out *strings.Builder, n mcp.BlockNeighbors) {
	if len(n.Anchors) == 0 {
		return
	}
	fmt.Fprintf(out, "Graph anchors: %s\n\n", strings.Join(n.Anchors, ", "))
	buckets := []struct {
		name string
		list []mcp.Neighbor
	}{
		{"callers", n.Callers}, {"callees", n.Callees}, {"consumers", n.Consumers},
		{"publishers", n.Publishers}, {"depends on", n.DependsOn}, {"similar to", n.SimilarTo},
	}
	for _, bucket := range buckets {
		if len(bucket.list) == 0 {
			continue
		}
		names := make([]string, 0, len(bucket.list))
		for _, neighbor := range bucket.list {
			name := neighbor.Symbol
			if name == "" {
				name = neighbor.ID
			}
			names = append(names, name)
		}
		sort.Strings(names)
		fmt.Fprintf(out, "- %s: %s\n", bucket.name, strings.Join(names, ", "))
	}
	out.WriteByte('\n')
}

func languageFor(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return "text"
	}
	aliases := map[string]string{
		".c": "c", ".cc": "cpp", ".cpp": "cpp", ".cs": "csharp", ".go": "go",
		".java": "java", ".js": "javascript", ".jsx": "jsx", ".json": "json",
		".md": "markdown", ".py": "python", ".rb": "ruby", ".rs": "rust",
		".sh": "bash", ".ts": "typescript", ".tsx": "tsx", ".yaml": "yaml", ".yml": "yaml",
	}
	if alias := aliases[ext]; alias != "" {
		return alias
	}
	return strings.TrimPrefix(ext, ".")
}
