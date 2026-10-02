package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"moedex/internal/semantic"
	"moedex/internal/semanticindex"
)

const EvidencePathResponseBytes = 64 << 10

type compilerEvidencePathReader interface {
	EvidencePath(context.Context, string, []string, int) (semanticindex.EvidencePathResult, error)
}
type compilerEvidencePathTool struct{ provider CompilerSessionProvider }

func (*compilerEvidencePathTool) Name() string { return "compiler_evidence_path" }

type CompilerPathRecord struct {
	ContextID           string                        `json:"context_id"`
	Repo                string                        `json:"repo"`
	Project             string                        `json:"project"`
	SourceSnapshotID    string                        `json:"source_snapshot_id"`
	Source              CompilerContractLocation      `json:"source"`
	SymbolID            string                        `json:"symbol_id"`
	OwnerID             string                        `json:"owner_id,omitempty"`
	DomainFacts         []semantic.DomainFact         `json:"domain_facts,omitempty"`
	ImplementationFacts []semantic.ImplementationFact `json:"implementation_facts,omitempty"`
}
type CompilerEvidencePathResult struct {
	Status             string               `json:"status"`
	EvidenceScope      string               `json:"evidence_scope"`
	SnapshotID         string               `json:"snapshot_id"`
	ArtifactSHA256     string               `json:"artifact_sha256"`
	SymbolID           string               `json:"symbol_id"`
	ContextIDs         []string             `json:"context_ids"`
	RequiredContextIDs []string             `json:"required_context_ids"`
	Symbols            []CompilerSymbol     `json:"symbols"`
	Records            []CompilerPathRecord `json:"records"`
	Truncated          bool                 `json:"truncated"`
	RowsVisited        int                  `json:"rows_visited"`
	WorkLimit          int                  `json:"work_limit"`
	ResponseByteLimit  int                  `json:"response_byte_limit"`
	Error              *compilerError       `json:"error,omitempty"`
}

func (t *compilerEvidencePathTool) Call(ctx context.Context, raw json.RawMessage) (map[string]interface{}, error) {
	out := CompilerEvidencePathResult{Status: "semantic_unavailable", EvidenceScope: "compile_time", ContextIDs: []string{}, RequiredContextIDs: []string{}, Symbols: []CompilerSymbol{}, Records: []CompilerPathRecord{}, WorkLimit: semanticindex.ContractWorkLimit, ResponseByteLimit: EvidencePathResponseBytes}
	respond := func() (map[string]interface{}, error) {
		result := StructuredResultWithSnapshot("Recorded class, framework configuration and default-forwarding evidence. Missing records are not absence; cast success, runtime dispatch and delivery remain unproved.", out, out.Error != nil, SnapshotIdentity{Cacheable: false})
		wire, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		if len(wire) > EvidencePathResponseBytes {
			return nil, fmt.Errorf("compiler evidence path response exceeds byte budget")
		}
		return result, nil
	}
	invalid := func(message string) (map[string]interface{}, error) {
		out.Status = "invalid_arguments"
		out.Error = &compilerError{Code: out.Status, Message: message}
		return respond()
	}
	var args struct {
		Symbol   string   `json:"symbol_id"`
		Contexts []string `json:"context_ids"`
		Limit    *int     `json:"limit"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&args) != nil {
		return invalid("Expected symbol_id, context_ids and optional limit.")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return invalid("Arguments must be one object.")
	}
	if !exactContractID(args.Symbol, "symbol:") || len(args.Contexts) == 0 || len(args.Contexts) > 32 {
		return invalid("Exact type symbol_id and 1..32 explicit context_ids required.")
	}
	selected := map[string]bool{}
	for _, id := range args.Contexts {
		if !exactContractID(id, "context:") || selected[id] {
			return invalid("Context IDs must be unique recorded identities.")
		}
		selected[id] = true
	}
	limit := 20
	if args.Limit != nil {
		limit = *args.Limit
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if limit < 1 || limit > 100 || (fields["limit"] != nil && args.Limit == nil) {
		return invalid("limit must be 1..100.")
	}
	out.SymbolID = args.Symbol
	out.ContextIDs = args.Contexts
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
	reader, ok := session.Reader.(compilerEvidencePathReader)
	if !ok {
		out.Status = "capability_unavailable"
		return respond()
	}
	q, err := reader.EvidencePath(ctx, args.Symbol, args.Contexts, limit)
	if errors.Is(err, semanticindex.ErrContractContextSelection) {
		out.Status = "invalid_context_selection"
		out.Error = &compilerError{Code: out.Status, Message: err.Error()}
		return respond()
	}
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if !q.Supported {
		out.Status = "index_upgrade_required"
		return respond()
	}
	if len(q.Records) > limit || q.RowsVisited < 0 || q.RowsVisited > semanticindex.ContractWorkLimit || len(q.RequiredContextIDs) > 32 {
		return nil, fmt.Errorf("compiler evidence path reader exceeded bounds")
	}
	out.Status = "no_recorded_evidence"
	out.Truncated = q.Truncated
	out.RowsVisited = q.RowsVisited
	required := map[string]bool{}
	for _, id := range q.RequiredContextIDs {
		if !exactContractID(id, "context:") || selected[id] || required[id] {
			return nil, fmt.Errorf("compiler evidence path invalid required context")
		}
		required[id] = true
		out.RequiredContextIDs = append(out.RequiredContextIDs, id)
	}

	allowedDefinitions := map[string]bool{args.Symbol: true}
	allowedCalls := map[string]bool{}
	for _, r := range q.Records {
		if r.Binding.SymbolID != args.Symbol || r.Occurrence.Role != "declaration" {
			continue
		}
		for _, fact := range r.Binding.ImplementationFacts {
			if fact.Kind != "interface_default_selection" {
				continue
			}
			allowedDefinitions[fact.DefaultTemplateSymbolID] = true
			if f := fact.Forwarding; f != nil {
				allowedDefinitions[f.ImplementationSymbolID] = true
				allowedCalls[f.CallOccurrenceID] = true
			}
		}
	}
	symbols := map[string]CompilerSymbol{}
	seen := map[string]bool{}
	projects := map[string]string{}
	for _, r := range q.Records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.Source.ID != r.Source.ComputeID() || !exactContractID(r.Snapshot.ID, "snapshot:") || !exactContractID("source:"+r.Source.RawSHA256, "source:") || r.Occurrence.Length == 0 || r.Occurrence.Offset > r.Source.ByteSize || r.Occurrence.Length > r.Source.ByteSize-r.Occurrence.Offset || !selected[r.Context.ID] || r.Context.Status != "complete" || r.Occurrence.ContextID != r.Context.ID || r.Occurrence.SourceID != r.Source.ID || r.Occurrence.ID != r.Occurrence.ComputeID() || r.Source.SnapshotID != r.Snapshot.ID || r.Context.SnapshotID != r.Snapshot.ID || r.Binding.Status != "resolved" || r.Symbol == nil || r.Binding.SymbolID != r.Symbol.ID || r.Binding.OccurrenceID != r.Occurrence.ID || !compilerRelative(r.Source.Path) || !compilerRelative(r.Snapshot.Repo) || !compilerRelative(r.Context.Project) {
			return nil, fmt.Errorf("compiler evidence path inconsistent record")
		}
		key := r.Snapshot.Repo + "/" + r.Context.Project
		if projects[key] != "" && projects[key] != r.Context.ID {
			return nil, fmt.Errorf("compiler evidence path incompatible contexts")
		}
		projects[key] = r.Context.ID
		if seen[r.Occurrence.ID] {
			return nil, fmt.Errorf("compiler evidence path duplicate record")
		}
		seen[r.Occurrence.ID] = true

		connected := r.Occurrence.Role == "declaration" && allowedDefinitions[r.Binding.SymbolID] || allowedCalls[r.Occurrence.ID]
		for _, fact := range r.Binding.DomainFacts {
			for _, target := range fact.Targets {
				if target.SymbolID == args.Symbol {
					connected = true
				}
			}
		}
		if !connected {
			return nil, fmt.Errorf("compiler evidence path returned unrelated record")
		}
		if err := semantic.ValidateDomainFacts(r.Binding.DomainFacts); err != nil {
			return nil, err
		}
		for _, fact := range r.Binding.DomainFacts {
			if err := semantic.ValidateDomainAPI(fact, *r.Symbol); err != nil {
				return nil, err
			}
			if err := semantic.ValidateDomainExtractor(fact, r.Binding.ExtractorVersion); err != nil {
				return nil, err
			}
		}
		if _, err := compilerBinding(r); err != nil {
			return nil, err
		}
		for _, s := range append(append([]semantic.Symbol{*r.Symbol}, r.DomainSymbols...), r.ImplementationSymbols...) {
			if s.ID != s.ComputeID() {
				return nil, fmt.Errorf("compiler evidence path inconsistent symbol")
			}
			value := compilerSymbol(s)
			if previous, ok := symbols[s.ID]; ok && previous != value {
				return nil, fmt.Errorf("compiler evidence path inconsistent symbol identity")
			}
			symbols[s.ID] = value
		}
		out.Records = append(out.Records, CompilerPathRecord{ContextID: r.Context.ID, Repo: r.Snapshot.Repo, Project: r.Context.Project, SourceSnapshotID: r.Snapshot.ID, Source: contractLocation(r), SymbolID: r.Symbol.ID, OwnerID: r.Binding.EnclosingSymbolID, DomainFacts: r.Binding.DomainFacts, ImplementationFacts: r.Binding.ImplementationFacts})
	}
	// Trim complete records and their now-unused descriptors together. The bound
	// covers the whole tool result envelope, not just materialized index records.
	for {
		needed := map[string]bool{}
		for _, r := range out.Records {
			needed[r.SymbolID] = true
			for _, f := range r.DomainFacts {
				for _, target := range f.Targets {
					needed[target.SymbolID] = true
				}
			}
			for _, f := range r.ImplementationFacts {
				for _, id := range f.SymbolIDs() {
					needed[id] = true
				}
			}
		}
		out.Symbols = []CompilerSymbol{}
		for id := range needed {
			s, ok := symbols[id]
			if !ok {
				return nil, fmt.Errorf("compiler evidence path missing symbol descriptor")
			}
			out.Symbols = append(out.Symbols, s)
		}
		sort.Slice(out.Symbols, func(i, j int) bool { return out.Symbols[i].ID < out.Symbols[j].ID })
		if len(out.Records) > 0 {
			out.Status = "ok"
		} else {
			out.Status = "no_recorded_evidence"
		}
		result, err := respond()
		if err == nil {
			return result, nil
		}
		if len(out.Records) == 0 {
			return nil, err
		}
		out.Records = out.Records[:len(out.Records)-1]
		out.Truncated = true
	}
}

func (t *compilerEvidencePathTool) Specification() ToolSpecification {
	// Record fields use the same validated wire types as the other compiler tools.
	symbols, binding, _, _ := compilerEvidenceSchemas()
	bp := binding["properties"].(map[string]interface{})
	locationProps := map[string]interface{}{}
	locationRequired := []string{}
	for _, k := range []string{"occurrence_id", "source_id", "path", "raw_sha256", "byte_offset", "byte_length", "generated", "reference_kind", "role", "binding_status", "method", "extractor", "extractor_version"} {
		locationProps[k] = bp[k]
		locationRequired = append(locationRequired, k)
	}
	recordProps := map[string]interface{}{"source": objectSchema(locationRequired, locationProps), "domain_facts": arrayOf(objectSchema([]string{"kind", "rule", "evidence_scope", "targets"}, map[string]interface{}{"kind": stringSchema(), "rule": stringSchema(), "evidence_scope": stringSchema(), "targets": arrayOf(objectSchema([]string{"role", "symbol_id"}, map[string]interface{}{"role": stringSchema(), "symbol_id": stringSchema()})), "lifetime": stringSchema(), "table": stringSchema(), "schema": stringSchema(), "route_pattern": stringSchema()})), "implementation_facts": bp["implementation_facts"]}
	recordRequired := []string{"source"}
	for _, k := range []string{"context_id", "repo", "project", "source_snapshot_id", "symbol_id", "owner_id"} {
		recordProps[k] = stringSchema()
		if k != "owner_id" {
			recordRequired = append(recordRequired, k)
		}
	}
	record := objectSchema(recordRequired, recordProps)
	props := map[string]interface{}{"symbols": arrayOf(symbols), "records": arrayOf(record), "context_ids": arrayOf(stringSchema()), "required_context_ids": arrayOf(stringSchema()), "truncated": boolSchema(), "error": errorPropertySchema()}
	required := []string{"symbols", "records", "context_ids", "required_context_ids", "truncated"}
	for _, key := range []string{"status", "evidence_scope", "snapshot_id", "artifact_sha256", "symbol_id"} {
		props[key] = stringSchema()
		required = append(required, key)
	}
	for _, key := range []string{"rows_visited", "work_limit", "response_byte_limit"} {
		props[key] = intSchema()
		required = append(required, key)
	}
	return ToolSpecification{Name: t.Name(), Description: "Collect source class declarations, framework observations naming that class, and recorded default-interface forwarding targets/template/call witnesses in one immutable session. Start with a type symbol_id from compiler_symbols and explicit context_ids; missing witness contexts are returned in required_context_ids for explicit selection. Descriptors occur once in symbols. No recursive traversal, automatic context selection, runtime dispatch, successful cast or delivery claim. Missing evidence is not absence. Shared 10000-row/2MiB materialization budget, 1..100 records (default20), and 64KiB serialized tool-result cap; inspect truncated and required_context_ids.", InputSchema: objectSchema([]string{"symbol_id", "context_ids"}, map[string]interface{}{"symbol_id": stringSchema(), "context_ids": arrayOf(stringSchema()), "limit": map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 100}}), OutputSchema: objectSchema(required, props)}
}
