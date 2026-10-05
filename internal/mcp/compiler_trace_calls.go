package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"moedex/internal/semanticindex"
)

const CallTraceResponseBytes = 64 << 10

type compilerCallTraceReader interface {
	TraceCalls(context.Context, string, []string, string, int, int) (semanticindex.CallTraceResult, error)
}
type compilerCallTraceTool struct{ provider CompilerSessionProvider }

func (*compilerCallTraceTool) Name() string { return "compiler_trace_calls" }

type CompilerCallEdge struct {
	Caller  CompilerSymbol  `json:"caller"`
	Witness CompilerBinding `json:"witness"`
	Commit  string          `json:"commit"`
	Depth   int             `json:"depth"`
}
type CompilerCallTraceResult struct {
	Status            string             `json:"status"`
	EvidenceScope     string             `json:"evidence_scope"`
	SnapshotID        string             `json:"snapshot_id"`
	ArtifactSHA256    string             `json:"artifact_sha256"`
	SymbolID          string             `json:"symbol_id"`
	ContextIDs        []string           `json:"context_ids"`
	Direction         string             `json:"direction"`
	Depth             int                `json:"depth"`
	Root              *CompilerSymbol    `json:"root,omitempty"`
	Edges             []CompilerCallEdge `json:"edges"`
	RowsVisited       int                `json:"rows_visited"`
	WorkLimit         int                `json:"work_limit"`
	ResponseByteLimit int                `json:"response_byte_limit"`
	Truncated         bool               `json:"truncated"`
	Error             *compilerError     `json:"error,omitempty"`
}

func (t *compilerCallTraceTool) Call(ctx context.Context, raw json.RawMessage) (map[string]interface{}, error) {
	out := CompilerCallTraceResult{Status: "semantic_unavailable", EvidenceScope: "compile_time", ContextIDs: []string{}, Edges: []CompilerCallEdge{}, WorkLimit: semanticindex.ContractWorkLimit, ResponseByteLimit: CallTraceResponseBytes}
	respond := func() (map[string]interface{}, error) {
		r := StructuredResultWithSnapshot("Recorded compiler method invocations in explicitly selected historical contexts. Targets preserve static binding; runtime dispatch, compatible dependency closure and absence of callers are unproved. Confidence-scored graph candidates remain available through trace_calls.", out, out.Error != nil, SnapshotIdentity{Cacheable: false})
		wire, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		if len(wire) > CallTraceResponseBytes {
			return nil, fmt.Errorf("compiler call trace response exceeds byte budget")
		}
		return r, nil
	}
	invalid := func(message string) (map[string]interface{}, error) {
		out.Status = "invalid_arguments"
		out.Error = &compilerError{out.Status, message}
		return respond()
	}
	var args struct {
		Symbol    string   `json:"symbol_id"`
		Contexts  []string `json:"context_ids"`
		Direction *string  `json:"direction"`
		Depth     *int     `json:"depth"`
		Limit     *int     `json:"limit"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&args) != nil {
		return invalid("Expected symbol_id, context_ids, optional direction, depth and limit.")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return invalid("Arguments must be one object.")
	}
	if !exactContractID(args.Symbol, "symbol:") || len(args.Contexts) == 0 || len(args.Contexts) > semanticindex.MaxContractContexts {
		return invalid("Exact method symbol_id and 1..32 explicit context_ids required.")
	}
	selected := map[string]bool{}
	for _, id := range args.Contexts {
		if !exactContractID(id, "context:") || selected[id] {
			return invalid("Context IDs must be unique recorded identities.")
		}
		selected[id] = true
	}
	direction, depth, limit := "outbound", 1, 20
	if args.Direction != nil {
		direction = *args.Direction
	}
	if args.Depth != nil {
		depth = *args.Depth
	}
	if args.Limit != nil {
		limit = *args.Limit
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if (fields["direction"] != nil && args.Direction == nil) || (fields["depth"] != nil && args.Depth == nil) || (fields["limit"] != nil && args.Limit == nil) || (direction != "inbound" && direction != "outbound" && direction != "both") || depth < 1 || depth > semanticindex.MaxCallDepth || limit < 1 || limit > semanticindex.MaxResults {
		return invalid("direction must be inbound/outbound/both, depth 1..8, limit 1..100; null is invalid.")
	}
	out.SymbolID, out.ContextIDs, out.Direction, out.Depth = args.Symbol, args.Contexts, direction, depth
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.provider == nil {
		return respond()
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
		return respond()
	}
	reader, ok := session.Reader.(compilerCallTraceReader)
	if !ok {
		out.Status = "index_upgrade_required"
		return respond()
	}
	q, err := reader.TraceCalls(ctx, args.Symbol, args.Contexts, direction, depth, limit)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if errors.Is(err, semanticindex.ErrContractContextSelection) {
		return invalid(err.Error())
	}
	if err != nil {
		return nil, err
	}
	if !q.Supported {
		out.Status = "index_upgrade_required"
		return respond()
	}
	if len(q.Edges) > limit || q.RowsVisited < 0 || q.RowsVisited > semanticindex.ContractWorkLimit || q.RowsVisited < len(q.Edges) {
		return nil, fmt.Errorf("compiler call trace reader exceeded bounds")
	}
	if q.Root != nil {
		if q.Root.ID != args.Symbol || q.Root.ID != q.Root.ComputeID() || !semanticindex.IsCallMethodDescriptor(q.Root.Key.Descriptor) {
			return nil, fmt.Errorf("compiler call trace inconsistent root")
		}
		r := compilerSymbol(*q.Root)
		out.Root = &r
	} else if len(q.Edges) > 0 {
		return nil, fmt.Errorf("compiler call trace missing root")
	}
	out.Truncated, out.RowsVisited = q.Truncated, q.RowsVisited
	seen := map[string]bool{}
	distances := map[string]int{args.Symbol: 0}
	projects := map[string]string{}
	for _, edge := range q.Edges {
		r := edge.Witness
		if edge.Caller.ID != edge.Caller.ComputeID() || edge.Target.ID != edge.Target.ComputeID() || !semanticindex.IsCallMethodDescriptor(edge.Caller.Key.Descriptor) || !semanticindex.IsCallMethodDescriptor(edge.Target.Key.Descriptor) || edge.Depth < 1 || edge.Depth > depth || r.Binding.EnclosingSymbolID != edge.Caller.ID || r.Binding.SymbolID != edge.Target.ID || r.Symbol == nil || *r.Symbol != edge.Target || r.Binding.Status != "resolved" || r.Occurrence.Role != "reference" || r.Occurrence.Kind != "invocation" || len(r.Candidates) > 0 || r.Source.ID != r.Source.ComputeID() || !exactContractID(r.Snapshot.ID, "snapshot:") || !exactContractID("source:"+r.Source.RawSHA256, "source:") || r.Occurrence.Length == 0 || r.Occurrence.Offset > r.Source.ByteSize || r.Occurrence.Length > r.Source.ByteSize-r.Occurrence.Offset || !selected[r.Context.ID] || r.Context.Status != "complete" || r.Occurrence.ContextID != r.Context.ID || r.Occurrence.SourceID != r.Source.ID || r.Occurrence.ID != r.Occurrence.ComputeID() || r.Binding.OccurrenceID != r.Occurrence.ID || r.Source.SnapshotID != r.Snapshot.ID || r.Context.SnapshotID != r.Snapshot.ID || r.Binding.Extractor != r.Context.Extractor || r.Binding.ExtractorVersion != r.Context.ExtractorVersion || !compilerRelative(r.Source.Path) || !compilerRelative(r.Snapshot.Repo) || !compilerRelative(r.Context.Project) {
			return nil, fmt.Errorf("compiler call trace inconsistent witness")
		}
		key := r.Snapshot.Repo + "/" + r.Context.Project
		if projects[key] != "" && projects[key] != r.Context.ID {
			return nil, fmt.Errorf("compiler call trace incompatible contexts")
		}
		projects[key] = r.Context.ID
		if seen[r.Occurrence.ID] {
			return nil, fmt.Errorf("compiler call trace duplicate witness")
		}
		seen[r.Occurrence.ID] = true
		connected := false
		if d, ok := distances[edge.Caller.ID]; direction != "inbound" && ok && d == edge.Depth-1 {
			connected = true
		}
		if d, ok := distances[edge.Target.ID]; direction != "outbound" && ok && d == edge.Depth-1 {
			connected = true
		}
		if !connected {
			return nil, fmt.Errorf("compiler call trace unrelated edge or depth")
		}
		if direction != "inbound" {
			if _, ok := distances[edge.Target.ID]; !ok {
				distances[edge.Target.ID] = edge.Depth
			}
		}
		if direction != "outbound" {
			if _, ok := distances[edge.Caller.ID]; !ok {
				distances[edge.Caller.ID] = edge.Depth
			}
		}
		b, err := compilerBinding(r)
		if err != nil {
			return nil, err
		}
		out.Edges = append(out.Edges, CompilerCallEdge{Caller: compilerSymbol(edge.Caller), Witness: b, Commit: r.Snapshot.Commit, Depth: edge.Depth})
	}
	for {
		out.Status = "no_recorded_calls"
		if len(out.Edges) > 0 {
			out.Status = "ok"
		}
		r, err := respond()
		if err == nil {
			return r, nil
		}
		if len(out.Edges) == 0 {
			return nil, err
		}
		out.Edges = out.Edges[:len(out.Edges)-1]
		out.Truncated = true
	}
}

func (t *compilerCallTraceTool) Specification() ToolSpecification {
	symbol, binding, _, _ := compilerEvidenceSchemas()
	props := map[string]interface{}{"root": symbol, "context_ids": arrayOf(stringSchema()), "edges": arrayOf(objectSchema([]string{"caller", "witness", "commit", "depth"}, map[string]interface{}{"caller": symbol, "witness": binding, "commit": stringSchema(), "depth": intSchema()})), "truncated": boolSchema(), "error": errorPropertySchema()}
	required := []string{"context_ids", "edges", "truncated"}
	for _, key := range []string{"status", "evidence_scope", "snapshot_id", "artifact_sha256", "symbol_id", "direction"} {
		props[key] = stringSchema()
		required = append(required, key)
	}
	for _, key := range []string{"depth", "rows_visited", "work_limit", "response_byte_limit"} {
		props[key] = intSchema()
		required = append(required, key)
	}
	return ToolSpecification{Name: t.Name(), Description: "Trace exact recorded compiler method invocations. Discover a method symbol_id and contexts with compiler_symbols or compiler_binding_at; supply 1..32 explicit context_ids. direction inbound/outbound/both (default outbound), depth 1..8 (default1), limit 1..100 call-site witnesses (default20). Preserves qualified static targets, caller, commit, context and raw source spans. Selected projects form a union of observations, not a compatible build or runtime path. No inferred interface/virtual dispatch, unresolved/method-group/constructor edges or syntax-name joins. Missing records are not absence; use trace_calls for confidence-scored graph candidates. Requires semantic index v6, rebuildable from captured artifacts. Shared 10000-row/2MiB query bound, 64KiB serialized response cap; inspect truncated.", InputSchema: objectSchema([]string{"symbol_id", "context_ids"}, map[string]interface{}{"symbol_id": stringSchema(), "context_ids": arrayOf(stringSchema()), "direction": map[string]interface{}{"type": "string", "enum": []string{"inbound", "outbound", "both"}}, "depth": map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 8}, "limit": map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 100}}), OutputSchema: objectSchema(required, props)}
}
