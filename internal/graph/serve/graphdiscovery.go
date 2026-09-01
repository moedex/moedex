package graphserve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
	"moedex/internal/mcp"
)

const (
	maxReadSourceLines = 500
	maxListSymbols     = 200
	maxFileTreeEntries = 2000
)

// discoverySchema caches the graph's node-kind and edge-type counts, computed
// once per snapshot generation.
type discoverySchema struct {
	Generation        uint64         `json:"generation"`
	CorpusFingerprint string         `json:"corpus_fingerprint"`
	BuildID           string         `json:"build_id"`
	TotalNodes        int            `json:"total_nodes"`
	TotalEdges        int            `json:"total_edges"`
	NodeKinds         map[string]int `json:"node_kinds"`
	EdgeTypes         map[string]int `json:"edge_types"`
}

// discoveryTool handles corpus orientation and discovery MCP tools.
type discoveryTool struct {
	owner *GraphToolset
	name  string
}

func (t *discoveryTool) Name() string { return t.name }

func (t *discoveryTool) Specification() mcp.ToolSpecification {
	return specificationForDescriptor(t.Descriptor())
}

func (t *discoveryTool) Descriptor() map[string]interface{} {
	schema := map[string]interface{}{
		"type":                 "object",
		"additionalProperties": false,
	}
	var description string
	switch t.name {
	case "list_repos":
		description = "List all indexed repositories in the corpus with file counts. Use this first to discover what code is available."
		schema["properties"] = map[string]interface{}{
			"filter": map[string]interface{}{
				"type":        "string",
				"description": "Optional substring filter on repository names (case-insensitive).",
			},
		}
	case "graph_schema":
		description = "Describe the loaded graph snapshot: generation, corpus fingerprint, build ID, node kind counts, edge type counts, and totals. Use its identity fields for downstream cache keys."
		schema["properties"] = map[string]interface{}{}
	case "read_source":
		description = "Read a source file's content from the indexed corpus by repository and path. Supports partial reads via start_line/end_line."
		schema["properties"] = map[string]interface{}{
			"repo": map[string]interface{}{
				"type":        "string",
				"description": "Repository name (as returned by list_repos). Omit to search all repos.",
			},
			"path": map[string]interface{}{
				"type":      "string",
				"minLength": 1,
				"description": "Relative file path within the repository. If not an exact match," +
					" the tool falls back to suffix matching (e.g. 'OrderService.cs' finds 'src/Services/OrderService.cs').",
			},
			"start_line": map[string]interface{}{
				"type": "integer", "minimum": 1,
				"description": "Optional first 1-based line to return (default 1).",
			},
			"end_line": map[string]interface{}{
				"type": "integer", "minimum": 1,
				"description": "Optional inclusive final line; omitted reads to the file or tool limit.",
			},
		}
		schema["required"] = []string{"path"}
	case "graph_neighbors":
		description = "Return ALL graph edges (any type) connected to a symbol — the full unfiltered neighborhood. Use trace_* tools when you know the edge type; use this for orientation."
		schema["properties"] = map[string]interface{}{
			"symbol": map[string]interface{}{"type": "string", "minLength": 1, "description": "Exact symbol name to query."},
			"hops": map[string]interface{}{
				"type": "integer", "minimum": 1, "maximum": maxGraphDepth,
				"description": "Maximum traversal depth (default 1).",
			},
			"min_confidence": map[string]interface{}{
				"type": "string", "enum": []string{"Candidate", "Pattern", "Verified", "Proven"},
				"description": "Minimum edge confidence included before traversal (default Pattern).",
			},
		}
		schema["required"] = []string{"symbol"}
	case "list_symbols":
		description = "Search symbols across the corpus by kind, name, and/or repository. At least one filter is required. Returns at most 200 results."
		schema["properties"] = map[string]interface{}{
			"kind": map[string]interface{}{
				"type":        "string",
				"description": "Filter by symbol kind (e.g. Type, Method, Function, Class, Interface). Case-insensitive.",
			},
			"repo": map[string]interface{}{
				"type":        "string",
				"description": "Filter by repository name (exact match).",
			},
			"query": map[string]interface{}{
				"type":        "string",
				"minLength":   1,
				"description": "Substring filter on symbol name (case-insensitive).",
			},
		}
	case "file_tree":
		description = "List files in an indexed repository, optionally filtered by directory path prefix."
		schema["properties"] = map[string]interface{}{
			"repo": map[string]interface{}{
				"type": "string", "minLength": 1,
				"description": "Repository name (as returned by list_repos).",
			},
			"prefix": map[string]interface{}{
				"type":        "string",
				"description": "Path prefix filter (e.g. 'src/' to list only files under src/).",
			},
		}
		schema["required"] = []string{"repo"}
	}
	return map[string]interface{}{
		"name":        t.name,
		"description": description,
		"inputSchema": schema,
	}
}

func (t *discoveryTool) Call(ctx context.Context, raw json.RawMessage) (map[string]interface{}, error) {
	snap := t.owner.acquire()
	if snap == nil {
		return mcp.TextResult("graph tools are closed", true), nil
	}
	defer snap.wg.Done()

	switch t.name {
	case "list_repos":
		var args struct {
			Filter *string `json:"filter"`
		}
		if err := decodeOptionalArgs(raw, &args); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		filter := ""
		if args.Filter != nil {
			filter = strings.TrimSpace(*args.Filter)
		}
		return snap.listRepos(filter), nil

	case "graph_schema":
		if err := rejectExtraArgs(raw); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		return snap.graphSchemaResult(), nil

	case "read_source":
		var args struct {
			Repo      *string `json:"repo"`
			Path      string  `json:"path"`
			StartLine *int    `json:"start_line"`
			EndLine   *int    `json:"end_line"`
		}
		if err := decodeOptionalArgs(raw, &args); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		args.Path = strings.TrimSpace(args.Path)
		if args.Path == "" {
			return invalidGraphArgs(fmt.Errorf("path must not be empty"), snap), nil
		}
		repo := ""
		if args.Repo != nil {
			repo = strings.TrimSpace(*args.Repo)
		}
		startLine := 1
		if args.StartLine != nil {
			startLine = *args.StartLine
			if startLine < 1 {
				startLine = 1
			}
		}
		endLine := 0
		if args.EndLine != nil {
			endLine = *args.EndLine
		}
		return snap.readSource(repo, args.Path, startLine, endLine)

	case "graph_neighbors":
		var args struct {
			Symbol        string `json:"symbol"`
			Hops          *int   `json:"hops"`
			MinConfidence string `json:"min_confidence"`
		}
		if err := decodeGraphArgs(raw, &args); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		args.Symbol = strings.TrimSpace(args.Symbol)
		if err := validateGraphString("symbol", args.Symbol); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		hops := defaultTraceDepth
		if args.Hops != nil {
			hops = *args.Hops
		}
		if err := validateDepth("hops", hops); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		minConfidence, err := graph.ParseMinConfidence(args.MinConfidence)
		if err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		result, err := snap.graphNeighbors(ctx, args.Symbol, hops, minConfidence)
		if err != nil {
			return nil, err
		}
		return snap.graphStructuredResult(result), nil

	case "list_symbols":
		var args struct {
			Kind  *string `json:"kind"`
			Repo  *string `json:"repo"`
			Query *string `json:"query"`
		}
		if err := decodeOptionalArgs(raw, &args); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		kind, repo, query := "", "", ""
		if args.Kind != nil {
			kind = strings.TrimSpace(*args.Kind)
		}
		if args.Repo != nil {
			repo = strings.TrimSpace(*args.Repo)
		}
		if args.Query != nil {
			query = strings.TrimSpace(*args.Query)
		}
		if kind == "" && repo == "" && query == "" {
			return invalidGraphArgs(fmt.Errorf("at least one of kind, repo, or query is required"), snap), nil
		}
		return snap.listSymbols(kind, repo, query), nil

	case "file_tree":
		var args struct {
			Repo   string  `json:"repo"`
			Prefix *string `json:"prefix"`
		}
		if err := decodeGraphArgs(raw, &args); err != nil {
			return invalidGraphArgs(err, snap), nil
		}
		args.Repo = strings.TrimSpace(args.Repo)
		if args.Repo == "" {
			return invalidGraphArgs(fmt.Errorf("repo must not be empty"), snap), nil
		}
		prefix := ""
		if args.Prefix != nil {
			prefix = *args.Prefix
		}
		return snap.fileTree(args.Repo, prefix)

	default:
		return nil, fmt.Errorf("server: unknown discovery tool %q", t.name)
	}
}

// ---------------------------------------------------------------------------
// Lazy index builders
// ---------------------------------------------------------------------------

func (s *graphSnapshot) ensureRepoIndex() {
	s.repoOnce.Do(func() {
		s.repoFiles = make(map[string]map[string]*index.Blob)
		for _, blobs := range s.blobs {
			for _, blob := range blobs {
				for _, f := range blob.Files {
					if f.Repo == "" {
						continue
					}
					if s.repoFiles[f.Repo] == nil {
						s.repoFiles[f.Repo] = make(map[string]*index.Blob)
					}
					if _, exists := s.repoFiles[f.Repo][f.RelPath]; !exists {
						s.repoFiles[f.Repo][f.RelPath] = blob
					}
				}
			}
		}
	})
}

func (s *graphSnapshot) ensureSchema() {
	s.schemaOnce.Do(func() {
		kinds := make(map[string]int)
		for _, meta := range s.nodes {
			k := meta.Kind
			if k == "" {
				k = "unknown"
			}
			kinds[k]++
		}
		types := make(map[string]int)
		for _, key := range s.graph.Keys() {
			for _, edge := range s.graph.Edges(key) {
				types[edge.Type.String()]++
			}
		}
		s.schemaInfo = &discoverySchema{
			Generation:        s.graph.Generation(),
			CorpusFingerprint: s.corpusFingerprint,
			BuildID:           s.buildID,
			TotalNodes:        len(s.nodes),
			TotalEdges:        s.graph.NumEdges(),
			NodeKinds:         kinds,
			EdgeTypes:         types,
		}
	})
}

// ---------------------------------------------------------------------------
// Tool implementations
// ---------------------------------------------------------------------------

type repoSummary struct {
	Name  string `json:"name"`
	Files int    `json:"files"`
}

func (s *graphSnapshot) listRepos(filter string) map[string]interface{} {
	s.ensureRepoIndex()
	filterLower := strings.ToLower(filter)

	var repos []repoSummary
	for name, files := range s.repoFiles {
		if filter != "" && !strings.Contains(strings.ToLower(name), filterLower) {
			continue
		}
		repos = append(repos, repoSummary{Name: name, Files: len(files)})
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })

	type result struct {
		Repos []repoSummary `json:"repos"`
		Total int           `json:"total"`
	}
	r := result{Repos: repos, Total: len(repos)}
	if r.Repos == nil {
		r.Repos = []repoSummary{}
	}
	return s.structuredResult(
		fmt.Sprintf("list_repos: %d repositories", len(repos)),
		r, false,
	)
}

func (s *graphSnapshot) graphSchemaResult() map[string]interface{} {
	s.ensureSchema()
	return s.structuredResult(
		fmt.Sprintf("graph_schema: generation %d, build %s, %d nodes (%d kinds), %d edges (%d types)",
			s.schemaInfo.Generation, s.schemaInfo.BuildID,
			s.schemaInfo.TotalNodes, len(s.schemaInfo.NodeKinds),
			s.schemaInfo.TotalEdges, len(s.schemaInfo.EdgeTypes)),
		s.schemaInfo, false,
	)
}

type sourceResult struct {
	Repo      string `json:"repo"`
	Path      string `json:"path"`
	BlobSHA   string `json:"blob_sha"`
	Lines     int    `json:"lines"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content"`
}

func (s *graphSnapshot) readSource(repo, path string, startLine, endLine int) (map[string]interface{}, error) {
	s.ensureRepoIndex()

	blob, foundRepo, foundPath := s.findBlob(repo, path)
	if blob == nil {
		if repo != "" {
			return s.errorResult("not_found", fmt.Sprintf("file not found: %s in repo %s", path, repo)), nil
		}
		return s.errorResult("not_found", fmt.Sprintf("file not found: %s", path)), nil
	}

	lines := bytes.Split(blob.Content, []byte("\n"))
	totalLines := len(lines)

	if endLine <= 0 || endLine > startLine+maxReadSourceLines-1 {
		endLine = startLine + maxReadSourceLines - 1
	}
	if endLine > totalLines {
		endLine = totalLines
	}
	if startLine > totalLines {
		return s.errorResult("invalid_arguments", fmt.Sprintf("start_line %d exceeds file length (%d lines)", startLine, totalLines), blob.SHA), nil
	}

	selected := lines[startLine-1 : endLine]
	content := string(bytes.Join(selected, []byte("\n")))
	truncated := endLine < totalLines

	r := sourceResult{
		Repo:      foundRepo,
		Path:      foundPath,
		BlobSHA:   blob.SHA,
		Lines:     totalLines,
		StartLine: startLine,
		EndLine:   endLine,
		Truncated: truncated,
		Content:   content,
	}
	return s.structuredResult(
		content,
		r, false, blob.SHA,
	), nil
}

// findBlob looks up a blob by repo+path. Exact relpath first, then suffix fallback.
// Returns (blob, resolvedRepo, resolvedPath).
func (s *graphSnapshot) findBlob(repo, path string) (*index.Blob, string, string) {
	if repo != "" {
		files := s.repoFiles[repo]
		if files == nil {
			return nil, "", ""
		}
		if b, ok := files[path]; ok {
			return b, repo, path
		}
		// Suffix fallback within repo.
		for relPath, b := range files {
			if strings.HasSuffix(relPath, "/"+path) || relPath == path {
				return b, repo, relPath
			}
		}
		return nil, "", ""
	}
	// No repo specified: search all repos, exact first.
	for r, files := range s.repoFiles {
		if b, ok := files[path]; ok {
			return b, r, path
		}
	}
	// Suffix fallback across all repos.
	for r, files := range s.repoFiles {
		for relPath, b := range files {
			if strings.HasSuffix(relPath, "/"+path) || relPath == path {
				return b, r, relPath
			}
		}
	}
	return nil, "", ""
}

func (s *graphSnapshot) graphNeighbors(ctx context.Context, symbol string, hops int, minConfidence graph.ConfidenceTier) (GraphQueryResult, error) {
	roots := append([]diskgraph.Key(nil), s.bySymbol[symbol]...)
	return s.traverse(ctx, "graph_neighbors", symbol, roots, hops, true, minConfidence, func(t diskgraph.EdgeType) bool {
		return true
	})
}

type symbolEntry struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Repo    string `json:"repo,omitempty"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
	BlobSHA string `json:"blob_sha"`
}

func (s *graphSnapshot) listSymbols(kind, repo, query string) map[string]interface{} {
	kindLower := strings.ToLower(kind)
	queryLower := strings.ToLower(query)

	var results []symbolEntry
	seen := make(map[string]bool) // "name:kind:repo:path" dedup

	for name, keys := range s.bySymbol {
		if query != "" && !strings.Contains(strings.ToLower(name), queryLower) {
			continue
		}
		for _, key := range keys {
			meta := s.nodes[key]
			if kind != "" && !strings.EqualFold(meta.Kind, kindLower) {
				continue
			}

			entry := symbolEntry{Name: name, Kind: meta.Kind, BlobSHA: key.BlobSHA}
			if len(meta.Locations) > 0 {
				entry.Repo = meta.Locations[0].Repo
				entry.Path = meta.Locations[0].Path
				entry.Line = meta.Locations[0].Line
			}

			if repo != "" && entry.Repo != repo {
				continue
			}

			dedup := fmt.Sprintf("%s:%s:%s:%s", name, meta.Kind, entry.Repo, entry.Path)
			if seen[dedup] {
				continue
			}
			seen[dedup] = true

			results = append(results, entry)
			if len(results) >= maxListSymbols {
				goto done
			}
		}
	}
done:
	sort.Slice(results, func(i, j int) bool {
		if results[i].Kind != results[j].Kind {
			return results[i].Kind < results[j].Kind
		}
		return results[i].Name < results[j].Name
	})

	type listResult struct {
		Symbols   []symbolEntry `json:"symbols"`
		Total     int           `json:"total"`
		Truncated bool          `json:"truncated"`
	}
	r := listResult{
		Symbols:   results,
		Total:     len(results),
		Truncated: len(results) >= maxListSymbols,
	}
	if r.Symbols == nil {
		r.Symbols = []symbolEntry{}
	}
	var blobSHAs []string
	for _, entry := range results {
		blobSHAs = append(blobSHAs, entry.BlobSHA)
	}
	return s.structuredResult(
		fmt.Sprintf("list_symbols: %d result(s)", len(results)),
		r, false, blobSHAs...,
	)
}

type fileTreeResult struct {
	Repo      string   `json:"repo"`
	Prefix    string   `json:"prefix,omitempty"`
	Files     []string `json:"files"`
	Total     int      `json:"total"`
	Truncated bool     `json:"truncated"`
}

func (s *graphSnapshot) fileTree(repo, prefix string) (map[string]interface{}, error) {
	s.ensureRepoIndex()

	files := s.repoFiles[repo]
	if files == nil {
		return s.errorResult("not_found", fmt.Sprintf("repository not found: %s", repo)), nil
	}

	var paths []string
	for relPath := range files {
		if prefix != "" && !strings.HasPrefix(relPath, prefix) {
			continue
		}
		paths = append(paths, relPath)
	}
	sort.Strings(paths)

	truncated := len(paths) > maxFileTreeEntries
	if truncated {
		paths = paths[:maxFileTreeEntries]
	}

	r := fileTreeResult{
		Repo:      repo,
		Prefix:    prefix,
		Files:     paths,
		Total:     len(paths),
		Truncated: truncated,
	}
	if r.Files == nil {
		r.Files = []string{}
	}
	return s.structuredResult(
		fmt.Sprintf("file_tree: %d file(s) in %s", len(paths), repo),
		r, false,
	), nil
}

// ---------------------------------------------------------------------------
// Argument helpers
// ---------------------------------------------------------------------------

func decodeOptionalArgs(raw json.RawMessage, dst interface{}) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) {
		return nil
	}
	if len(raw) > maxGraphInputBytes {
		return fmt.Errorf("arguments exceed %d bytes", maxGraphInputBytes)
	}
	if trimmed[0] != '{' {
		return fmt.Errorf("arguments must be a JSON object")
	}
	return json.Unmarshal(raw, dst)
}

func rejectExtraArgs(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) {
		return nil
	}
	return fmt.Errorf("graph_schema accepts no arguments")
}
