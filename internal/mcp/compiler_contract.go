package mcp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"moedex/internal/semanticindex"
)

// Optional capability: existing compiler readers and legacy snapshots continue
// to support binding/definition queries without implementing contract lookup.
type compilerContractReader interface {
	ContractImpact(context.Context, string, []string, int) (semanticindex.ContractImpactResult, error)
}

type compilerContractImpactTool struct{ provider CompilerSessionProvider }

func (*compilerContractImpactTool) Name() string { return "compiler_contract_impact" }

type CompilerContractPath struct {
	Contract     CompilerSymbol     `json:"contract"`
	MatchedRoles []string           `json:"matched_roles"`
	Fact         CompilerDomainFact `json:"fact"`
	Source       CompilerBinding    `json:"source"`
	Owner        *CompilerSymbol    `json:"owner,omitempty"`
}

type CompilerContractResult struct {
	Status                   string                 `json:"status"`
	EvidenceScope            string                 `json:"evidence_scope"`
	SnapshotID               string                 `json:"snapshot_id"`
	ArtifactSHA256           string                 `json:"artifact_sha256"`
	Contract                 *CompilerSymbol        `json:"contract,omitempty"`
	ContextIDs               []string               `json:"context_ids"`
	Contexts                 []CompilerContext      `json:"contexts"`
	Paths                    []CompilerContractPath `json:"paths"`
	Count                    int                    `json:"count"`
	TotalIsExact             bool                   `json:"total_is_exact"`
	Truncated                bool                   `json:"truncated"`
	Limit                    int                    `json:"limit"`
	PostingsVisited          int                    `json:"postings_visited"`
	WorkLimit                int                    `json:"work_limit"`
	MaterializationByteLimit int                    `json:"materialization_byte_limit"`
	Error                    *compilerError         `json:"error,omitempty"`
}

func (t *compilerContractImpactTool) Call(ctx context.Context, raw json.RawMessage) (map[string]interface{}, error) {
	out := CompilerContractResult{Status: "semantic_unavailable", EvidenceScope: "compile_time", ContextIDs: []string{}, Contexts: []CompilerContext{}, Paths: []CompilerContractPath{}, Limit: semanticindex.MaxResults, MaterializationByteLimit: semanticindex.MaxQueryBytes}
	respond := func(message string, bad bool) map[string]interface{} {
		return StructuredResultWithSnapshot(message, out, bad, SnapshotIdentity{Cacheable: false})
	}
	invalid := func(message string) (map[string]interface{}, error) {
		out.Status = "invalid_arguments"
		out.Error = &compilerError{Code: "invalid_arguments", Message: message}
		return respond(message, true), nil
	}
	var args struct {
		Symbol   string   `json:"symbol_id"`
		Contexts []string `json:"context_ids"`
		Limit    *int     `json:"limit"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return invalid("Expected symbol_id, optional context_ids, and limit.")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return invalid("Arguments must be one object.")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return invalid("Arguments must be an object.")
	}
	digest, digestErr := hex.DecodeString(strings.TrimPrefix(args.Symbol, "symbol:"))
	if !strings.HasPrefix(args.Symbol, "symbol:") || digestErr != nil || len(digest) != 32 || strings.ToLower(args.Symbol) != args.Symbol {
		return invalid("An exact domain target symbol_id is required.")
	}
	if args.Limit != nil {
		out.Limit = *args.Limit
	}
	if _, supplied := fields["limit"]; supplied && args.Limit == nil {
		return invalid("limit must be 1..100.")
	}
	if out.Limit < 1 || out.Limit > semanticindex.MaxResults {
		return invalid("limit must be 1..100.")
	}
	if _, supplied := fields["context_ids"]; supplied && (len(args.Contexts) == 0 || len(args.Contexts) > 32) {
		return invalid("context_ids must contain 1..32 unique recorded context IDs.")
	}
	selected := make(map[string]bool, len(args.Contexts))
	for _, id := range args.Contexts {
		if !compilerID(id) || selected[id] {
			return invalid("context_ids must contain 1..32 unique recorded context IDs.")
		}
		selected[id] = true
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.provider == nil {
		return respond("Compiler evidence is unavailable in this snapshot.", false), nil
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
		return respond("Compiler evidence is unavailable in this snapshot.", false), nil
	}
	reader, supported := session.Reader.(compilerContractReader)
	if !supported {
		out.Status = "capability_unavailable"
		return respond("This compiler reader does not support contract-impact lookup; binding and definition tools remain available.", false), nil
	}
	query, err := reader.ContractImpact(ctx, args.Symbol, args.Contexts, out.Limit)
	if err != nil {
		if errors.Is(err, semanticindex.ErrContractContextSelection) {
			out.Status = "invalid_context_selection"
			out.Error = &compilerError{Code: "invalid_context_selection", Message: err.Error()}
			return respond(err.Error(), true), nil
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out.Truncated, out.PostingsVisited, out.WorkLimit = query.Truncated, query.PostingsVisited, query.WorkLimit
	if !query.Supported {
		out.Status = "index_upgrade_required"
		return respond("This compiler index lacks contract-target postings. Rebuild the semantic index to enable this tool; existing binding tools remain available.", false), nil
	}
	if query.Contract != nil {
		symbol := compilerSymbol(*query.Contract)
		out.Contract = &symbol
	}
	if len(args.Contexts) == 0 {
		out.Status = "context_required"
		for _, c := range query.Contexts {
			out.Contexts = append(out.Contexts, CompilerContext{Generated: c.Source.Generated, ContextID: c.Context.ID, Project: c.Context.Project, Repo: c.Snapshot.Repo, Path: c.Source.Path, RawSHA256: c.Source.RawSHA256})
		}
		if len(out.Contexts) == 0 {
			return respond("No recorded framework evidence provides a context choice for this symbol. This does not establish that dependencies are absent.", false), nil
		}
		return respond("Select context_ids before requesting paths. Choices identify captured evidence; variants of the same repository/project cannot be combined.", false), nil
	}
	out.ContextIDs = append(out.ContextIDs, args.Contexts...)
	for _, match := range query.Matches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !selected[match.Result.Context.ID] {
			return nil, fmt.Errorf("compiler contract lookup returned an unselected context")
		}
		binding, err := compilerBinding(match.Result)
		if err != nil {
			return nil, err
		}
		if query.Contract == nil || query.Contract.ID != args.Symbol || match.DomainFactIndex < 0 || match.DomainFactIndex >= len(binding.DomainFacts) {
			return nil, fmt.Errorf("compiler contract lookup returned invalid evidence")
		}
		fact := binding.DomainFacts[match.DomainFactIndex]
		var roles []string
		for _, target := range fact.Targets {
			if target.Symbol.ID == args.Symbol {
				if target.Symbol != *out.Contract {
					return nil, fmt.Errorf("compiler contract lookup returned inconsistent target identity")
				}
				roles = append(roles, target.Role)
			}
		}
		if len(roles) == 0 || !slices.Equal(roles, match.TargetRoles) || fact.EvidenceScope != "compile_time" || binding.BindingStatus != "resolved" {
			return nil, fmt.Errorf("compiler contract lookup returned inconsistent target roles or evidence")
		}
		if match.Owner != nil && match.Owner.ID != binding.EnclosingSymbolID {
			return nil, fmt.Errorf("compiler contract lookup returned inconsistent owner identity")
		}
		binding.DomainFacts = []CompilerDomainFact{fact}
		path := CompilerContractPath{Contract: *out.Contract, MatchedRoles: append([]string{}, match.TargetRoles...), Fact: fact, Source: binding}
		if match.Owner != nil {
			owner := compilerSymbol(*match.Owner)
			path.Owner = &owner
		}
		out.Paths = append(out.Paths, path)
	}
	out.Count = len(out.Paths)
	out.TotalIsExact = !out.Truncated
	out.Status = "ok"
	if out.Count == 0 {
		out.Status = "no_recorded_evidence"
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	message := fmt.Sprintf("%d recorded compile-time contract paths in explicitly selected contexts. These are not runtime delivery, service resolution, or database activity.", out.Count)
	if out.Truncated {
		message += " Results reached a result or work bound; the count is a lower bound."
	}
	if out.Count == 0 {
		message += " Missing captured evidence does not establish that dependencies are absent."
	}
	return respond(message, false), nil
}

func (t *compilerContractImpactTool) Specification() ToolSpecification {
	symbols, binding, fact, choices := compilerEvidenceSchemas()
	contexts := arrayOf(stringSchema())
	contexts["minItems"], contexts["maxItems"], contexts["uniqueItems"] = 1, 32, true
	input := objectSchema([]string{"symbol_id"}, map[string]interface{}{"symbol_id": map[string]interface{}{"type": "string", "pattern": "^symbol:[0-9a-f]{64}$"}, "context_ids": contexts, "limit": map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 100}})
	path := objectSchema([]string{"contract", "matched_roles", "fact", "source"}, map[string]interface{}{"contract": symbols, "matched_roles": arrayOf(stringSchema()), "fact": fact, "source": binding, "owner": symbols})
	props := map[string]interface{}{"status": stringSchema(), "evidence_scope": stringSchema(), "snapshot_id": stringSchema(), "artifact_sha256": stringSchema(), "contract": symbols, "context_ids": arrayOf(stringSchema()), "contexts": arrayOf(choices), "paths": arrayOf(path), "count": intSchema(), "total_is_exact": boolSchema(), "truncated": boolSchema(), "limit": intSchema(), "postings_visited": intSchema(), "work_limit": intSchema(), "materialization_byte_limit": intSchema(), "error": errorPropertySchema()}
	output := objectSchema([]string{"status", "evidence_scope", "snapshot_id", "artifact_sha256", "context_ids", "contexts", "paths", "count", "total_is_exact", "truncated", "limit", "postings_visited", "work_limit", "materialization_byte_limit"}, props)
	return ToolSpecification{Name: t.Name(), Description: "Find recorded compile-time framework evidence for an exact domain target's symbol.id (not the bound framework API method ID). Omit context_ids to discover bounded choices, then explicitly select up to 32 contexts; incompatible variants of a repository/project cannot be combined. Paths link the contract to a matched framework fact and recorded source/owner, preserving descriptors, raw source hashes and context. The 2 MiB index materialization budget is not a serialized response-size ceiling: path fields repeat evidence. Capture-only supported APIs exclude private wrappers and unsupported patterns; missing evidence is not dependency absence. No runtime publisher-consumer links, delivery, service activation or database activity are inferred.", InputSchema: input, OutputSchema: output}
}
