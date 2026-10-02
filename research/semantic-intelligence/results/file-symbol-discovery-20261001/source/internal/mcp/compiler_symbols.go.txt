package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"moedex/internal/semanticindex"
)

type compilerSymbolsReader interface {
	FileSymbols(context.Context, string, string, string, int) (semanticindex.SymbolDiscoveryResult, error)
}
type compilerSymbolsTool struct{ provider CompilerSessionProvider }

func (*compilerSymbolsTool) Name() string { return "compiler_symbols" }

type CompilerSymbolsResult struct {
	CompilerResult
	RowsVisited              int `json:"rows_visited"`
	WorkLimit                int `json:"work_limit"`
	MaterializationByteLimit int `json:"materialization_byte_limit"`
}

func (t *compilerSymbolsTool) Call(ctx context.Context, raw json.RawMessage) (map[string]interface{}, error) {
	out := CompilerSymbolsResult{CompilerResult: CompilerResult{Status: "semantic_unavailable", Evidence: "recorded-compiler-context", Results: []CompilerBinding{}, Contexts: []CompilerContext{}}, WorkLimit: semanticindex.ContractWorkLimit, MaterializationByteLimit: semanticindex.MaxQueryBytes}
	respond := func(message string, bad bool) map[string]interface{} {
		return StructuredResultWithSnapshot(message, out, bad, SnapshotIdentity{Cacheable: false})
	}
	invalid := func() (map[string]interface{}, error) {
		out.Status = "invalid_arguments"
		out.Error = &compilerError{Code: out.Status, Message: "Expected repo, exact path, optional descriptor query (up to 256 bytes) and limit (1..100)."}
		return respond(out.Error.Message, true), nil
	}
	var args struct {
		Repo  string `json:"repo"`
		Path  string `json:"path"`
		Query string `json:"query"`
		Limit *int   `json:"limit"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&args) != nil {
		return invalid()
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return invalid()
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return invalid()
	}
	for _, v := range fields {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return invalid()
		}
	}
	limit := semanticindex.MaxResults
	if args.Limit != nil {
		limit = *args.Limit
	}
	if !compilerRelative(args.Repo) || !compilerRelative(args.Path) || len(args.Query) > 256 || strings.ContainsAny(args.Query, "\x00\r\n") || limit < 1 || limit > semanticindex.MaxResults {
		return invalid()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.provider == nil {
		return respond("Compiler evidence unavailable.", false), nil
	}
	session, err := t.provider.AcquireCompilerSession(ctx)
	if session.Release != nil {
		defer session.Release()
	}
	if err != nil {
		return nil, err
	}
	out.SnapshotID, out.ArtifactSHA256 = session.SnapshotID, session.ArtifactSHA256
	if session.Reader == nil {
		return respond("Compiler evidence unavailable.", false), nil
	}
	reader, ok := session.Reader.(compilerSymbolsReader)
	if !ok {
		out.Status = "capability_unavailable"
		return respond("This reader lacks file symbol discovery.", false), nil
	}
	q, err := reader.FileSymbols(ctx, args.Repo, args.Path, args.Query, limit)
	if err != nil {
		return nil, err
	}
	if len(q.Results) > limit || q.RowsVisited < 0 || q.RowsVisited > out.WorkLimit {
		return nil, fmt.Errorf("compiler symbol discovery exceeded bounds")
	}
	out.Truncated, out.RowsVisited = q.Truncated, q.RowsVisited
	seen := map[string]bool{}
	for _, r := range q.Results {
		if r.Snapshot.Repo != args.Repo || r.Source.Path != args.Path || r.Occurrence.Role != "declaration" || r.Binding.Status != "resolved" || r.Symbol == nil || r.Binding.SymbolID != r.Symbol.ID || !strings.Contains(r.Symbol.Key.Descriptor, args.Query) {
			return nil, fmt.Errorf("compiler symbol discovery returned inconsistent declaration")
		}
		b, err := compilerBinding(r)
		if err != nil {
			return nil, err
		}
		out.Results = append(out.Results, b)
		key := r.Context.ID + "\x00" + r.Source.ID
		if !seen[key] {
			seen[key] = true
			out.Contexts = append(out.Contexts, CompilerContext{Generated: r.Source.Generated, ContextID: r.Context.ID, Project: r.Context.Project, Repo: r.Snapshot.Repo, Path: r.Source.Path, RawSHA256: r.Source.RawSHA256})
		}
	}
	out.Status = "ok"
	if len(out.Results) == 0 {
		out.Status = "no_recorded_declaration"
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return respond("Recorded declaration alternatives; select context IDs explicitly for subsequent queries. Truncated results do not establish absence. Historical compiler evidence does not prove runtime dispatch.", false), nil
}
func (t *compilerSymbolsTool) Specification() ToolSpecification {
	_, binding, _, choices := compilerEvidenceSchemas()
	props := map[string]interface{}{"repo": stringSchema(), "path": stringSchema(), "query": stringSchema(), "limit": map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 100}}
	output := objectSchema([]string{"status", "evidence", "snapshot_id", "artifact_sha256", "results", "contexts", "truncated", "rows_visited", "work_limit", "materialization_byte_limit"}, map[string]interface{}{"status": stringSchema(), "evidence": stringSchema(), "snapshot_id": stringSchema(), "artifact_sha256": stringSchema(), "results": arrayOf(binding), "contexts": arrayOf(choices), "truncated": boolSchema(), "rows_visited": intSchema(), "work_limit": intSchema(), "materialization_byte_limit": intSchema(), "error": errorPropertySchema()})
	return ToolSpecification{Name: t.Name(), Description: "Discover recorded compiler declarations in one exact repo/path obtained from search_context or file_tree. Optional query is a case-sensitive descriptor substring, not an identity. Returns exact symbol IDs, raw offsets/hashes and separate context variants without reading source or computing offsets. Results and contexts contain only matching declaration alternatives, not a complete context inventory. At most 100 results, 10000 inspected occurrences and 2 MiB materialized evidence; inspect truncated even for an empty result. No pagination: narrow to a smaller source file or use compiler_binding_at when truncated. Select contexts explicitly for downstream tools; alternatives are not one combined build or proof of runtime dispatch.", InputSchema: objectSchema([]string{"repo", "path"}, props), OutputSchema: output}
}
