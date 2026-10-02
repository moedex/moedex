package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"moedex/internal/semantic"
	"moedex/internal/semanticindex"
)

type compilerContractPathsReader interface {
	ContractPaths(context.Context, string, []string, int) (semanticindex.ContractPathsResult, error)
}
type compilerContractPathsTool struct{ provider CompilerSessionProvider }

func (*compilerContractPathsTool) Name() string { return "compiler_contract_paths" }

type CompilerImplementationEvidence struct {
	Kind             string          `json:"kind"`
	Rule             string          `json:"rule"`
	EvidenceScope    string          `json:"evidence_scope"`
	Source           CompilerBinding `json:"source"`
	InterfaceMember  CompilerSymbol  `json:"interface_member"`
	ImplementingType CompilerSymbol  `json:"implementing_type"`
}
type CompilerWrapperPath struct {
	Seed           CompilerContractPath            `json:"seed"`
	Caller         CompilerBinding                 `json:"caller"`
	CallerOwner    CompilerSymbol                  `json:"caller_owner"`
	Implementation *CompilerImplementationEvidence `json:"implementation,omitempty"`
}
type CompilerContractPathsResult struct {
	Status                   string                 `json:"status"`
	EvidenceScope            string                 `json:"evidence_scope"`
	PathSemantics            string                 `json:"path_semantics"`
	SnapshotID               string                 `json:"snapshot_id"`
	ArtifactSHA256           string                 `json:"artifact_sha256"`
	ContextIDs               []string               `json:"context_ids"`
	Seeds                    []CompilerContractPath `json:"seeds"`
	Paths                    []CompilerWrapperPath  `json:"paths"`
	Limit                    int                    `json:"limit"`
	Records                  int                    `json:"records"`
	RowsVisited              int                    `json:"rows_visited"`
	WorkLimit                int                    `json:"work_limit"`
	MaterializationByteLimit int                    `json:"materialization_byte_limit"`
	SeedsTruncated           bool                   `json:"seeds_truncated"`
	PathsTruncated           bool                   `json:"paths_truncated"`
	TotalIsExact             bool                   `json:"total_is_exact"`
	Limitations              []string               `json:"limitations"`
	Error                    *compilerError         `json:"error,omitempty"`
}

func (t *compilerContractPathsTool) Call(ctx context.Context, raw json.RawMessage) (map[string]interface{}, error) {
	out := CompilerContractPathsResult{Status: "semantic_unavailable", EvidenceScope: "compile_time", PathSemantics: "candidate_static", ContextIDs: []string{}, Seeds: []CompilerContractPath{}, Paths: []CompilerWrapperPath{}, Limit: 100, WorkLimit: semanticindex.ContractWorkLimit, MaterializationByteLimit: semanticindex.MaxQueryBytes, Limitations: []string{"One recorded caller step to a direct publisher, optionally through an explicit interface implementation. No runtime dispatch, delivery, activation, or DI selection is established.", "Each hop retains its captured compilation. Selected cross-project contexts do not certify dependency-closure compatibility.", "Only captured exact calls and supported interface implementations are searched. No recursive call traversal or synthetic publisher facts; missing paths are not absence."}}
	respond := func(bad bool) map[string]interface{} {
		return StructuredResultWithSnapshot("Candidate static wrapper paths from source-linked compiler evidence; see scope, limitations, and truncation.", out, bad, SnapshotIdentity{Cacheable: false})
	}
	invalid := func(message string) (map[string]interface{}, error) {
		out.Status = "invalid_arguments"
		out.Error = &compilerError{Code: out.Status, Message: message}
		return respond(true), nil
	}
	var args struct {
		Symbol   string   `json:"symbol_id"`
		Contexts []string `json:"context_ids"`
		Limit    *int     `json:"limit"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&args) != nil {
		return invalid("Expected symbol_id, required context_ids, optional limit.")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return invalid("Expected one object.")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return invalid("Expected one object.")
	}
	if !exactContractID(args.Symbol, "symbol:") || len(args.Contexts) < 1 || len(args.Contexts) > 32 {
		return invalid("Exact symbol_id and 1..32 explicit context_ids required.")
	}
	selected := map[string]bool{}
	for _, id := range args.Contexts {
		if !exactContractID(id, "context:") || selected[id] {
			return invalid("Unique exact context_ids required.")
		}
		selected[id] = true
	}
	if args.Limit != nil {
		out.Limit = *args.Limit
	}
	if _, ok := fields["limit"]; ok && args.Limit == nil {
		return invalid("limit must be 1..100.")
	}
	if out.Limit < 1 || out.Limit > 100 {
		return invalid("limit must be 1..100.")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out.ContextIDs = append(out.ContextIDs, args.Contexts...)
	if t.provider == nil {
		return respond(false), nil
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
		return respond(false), nil
	}
	reader, ok := session.Reader.(compilerContractPathsReader)
	if !ok {
		out.Status = "capability_unavailable"
		return respond(false), nil
	}
	q, err := reader.ContractPaths(ctx, args.Symbol, args.Contexts, out.Limit)
	if err != nil {
		if errors.Is(err, semanticindex.ErrContractContextSelection) {
			out.Status = "invalid_context_selection"
			out.Error = &compilerError{Code: out.Status, Message: err.Error()}
			return respond(true), nil
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !q.Supported {
		out.Status = "index_upgrade_required"
		return respond(false), nil
	}
	if len(q.Seeds) > 20 || len(q.Paths) > out.Limit || q.Records > out.Limit || q.Records < 0 || q.RowsVisited < 0 || q.RowsVisited > semanticindex.ContractWorkLimit {
		return nil, fmt.Errorf("compiler paths reader exceeded bounds")
	}
	seedValue := func(m semanticindex.ContractImpactMatch) (CompilerContractPath, error) {
		r := m.Result
		bad := func() (CompilerContractPath, error) {
			return CompilerContractPath{}, fmt.Errorf("compiler paths returned inconsistent publisher seed")
		}
		if q.Contract == nil || q.Contract.ID != args.Symbol || !selected[r.Context.ID] || r.Binding.Status != "resolved" || r.Occurrence.Kind != "invocation" || r.Symbol == nil || m.Owner == nil || m.Owner.ID != r.Binding.EnclosingSymbolID || m.DomainFactIndex < 0 || m.DomainFactIndex >= len(r.Binding.DomainFacts) {
			return bad()
		}
		f := r.Binding.DomainFacts[m.DomainFactIndex]
		if f.Kind != "message_publish" || f.EvidenceScope != "compile_time" || len(f.Targets) != 1 || f.Targets[0].Role != "message" || f.Targets[0].SymbolID != args.Symbol {
			return bad()
		}
		b, err := compilerBinding(r)
		if err != nil {
			return CompilerContractPath{}, err
		}
		fact := b.DomainFacts[m.DomainFactIndex]
		if len(fact.Targets) != 1 || fact.Targets[0].Symbol != compilerSymbol(*q.Contract) {
			return bad()
		}
		b.DomainFacts = []CompilerDomainFact{fact}
		owner := compilerSymbol(*m.Owner)
		return CompilerContractPath{Contract: compilerSymbol(*q.Contract), MatchedRoles: []string{"message"}, Fact: fact, Source: b, Owner: &owner}, nil
	}
	records := 0
	for _, seed := range q.Seeds {
		s, e := seedValue(seed)
		if e != nil {
			return nil, e
		}
		out.Seeds = append(out.Seeds, s)
		records++
	}
	for _, p := range q.Paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s, e := seedValue(p.Seed)
		if e != nil {
			return nil, e
		}
		r := p.Caller
		if !selected[r.Context.ID] || r.Binding.Status != "resolved" || r.Occurrence.Kind != "invocation" || r.Occurrence.Role != "reference" || r.Symbol == nil || p.CallerOwner.ID != r.Binding.EnclosingSymbolID {
			return nil, fmt.Errorf("compiler paths returned inconsistent caller")
		}
		caller, err := compilerBinding(r)
		if err != nil {
			return nil, err
		}
		v := CompilerWrapperPath{Seed: s, Caller: caller, CallerOwner: compilerSymbol(p.CallerOwner)}
		target := s.Owner.ID
		records += 2
		if p.Implementation != nil {
			impl := p.Implementation
			if !selected[impl.Context.ID] || impl.Context.ID != p.Seed.Result.Context.ID || impl.Binding.Status != "resolved" || impl.Occurrence.Role != "declaration" || impl.Symbol == nil || impl.Symbol.ID != target || p.ImplementationFactIndex < 0 || p.ImplementationFactIndex >= len(impl.Binding.ImplementationFacts) {
				return nil, fmt.Errorf("compiler paths returned inconsistent implementation")
			}
			f := impl.Binding.ImplementationFacts[p.ImplementationFactIndex]
			sy := map[string]semantic.Symbol{}
			for _, symbol := range impl.ImplementationSymbols {
				sy[symbol.ID] = symbol
			}
			iface, ifaceOK := sy[f.InterfaceSymbolID]
			typ, typeOK := sy[f.ImplementingTypeSymbolID]
			if f.Kind == "interface_default_selection" || f.Rule == "csharp-interface-default-v1" || !ifaceOK || !typeOK || semantic.ValidateImplementationFacts([]semantic.ImplementationFact{f}) != nil || semantic.ValidateImplementationSymbols(f, *impl.Symbol, iface, typ) != nil {
				return nil, fmt.Errorf("compiler paths returned invalid implementation relationship")
			}
			implementation, err := compilerBinding(*impl)
			if err != nil {
				return nil, err
			}
			v.Implementation = &CompilerImplementationEvidence{Kind: f.Kind, Rule: f.Rule, EvidenceScope: f.EvidenceScope, Source: implementation, InterfaceMember: compilerSymbol(iface), ImplementingType: compilerSymbol(typ)}
			target = f.InterfaceSymbolID
			records++
		}
		if r.Symbol.ID != target {
			return nil, fmt.Errorf("compiler paths caller target does not match next hop")
		}
		out.Paths = append(out.Paths, v)
	}
	if records != q.Records || records > out.Limit {
		return nil, fmt.Errorf("compiler paths record accounting mismatch")
	}
	out.Records, out.RowsVisited = q.Records, q.RowsVisited
	out.SeedsTruncated, out.PathsTruncated = q.SeedsTruncated, q.PathsTruncated
	out.TotalIsExact = !q.SeedsTruncated && !q.PathsTruncated
	out.Status = "ok"
	if len(out.Paths) == 0 {
		out.Status = "no_recorded_paths"
	}
	return respond(false), nil
}
func (t *compilerContractPathsTool) Specification() ToolSpecification {
	sy, binding, domain, _ := compilerEvidenceSchemas()
	seed := objectSchema([]string{"contract", "matched_roles", "fact", "source"}, map[string]interface{}{"contract": sy, "matched_roles": arrayOf(stringSchema()), "fact": domain, "source": binding, "owner": sy})
	impl := objectSchema([]string{"kind", "rule", "evidence_scope", "source", "interface_member", "implementing_type"}, map[string]interface{}{"kind": stringSchema(), "rule": stringSchema(), "evidence_scope": stringSchema(), "source": binding, "interface_member": sy, "implementing_type": sy})
	path := objectSchema([]string{"seed", "caller", "caller_owner"}, map[string]interface{}{"seed": seed, "caller": binding, "caller_owner": sy, "implementation": impl})
	props := map[string]interface{}{"context_ids": arrayOf(stringSchema()), "seeds": arrayOf(seed), "paths": arrayOf(path), "limitations": arrayOf(stringSchema()), "error": errorPropertySchema()}
	required := []string{"context_ids", "seeds", "paths", "limitations"}
	for _, k := range []string{"status", "evidence_scope", "path_semantics", "snapshot_id", "artifact_sha256"} {
		props[k] = stringSchema()
		required = append(required, k)
	}
	for _, k := range []string{"limit", "records", "rows_visited", "work_limit", "materialization_byte_limit"} {
		props[k] = intSchema()
		required = append(required, k)
	}
	for _, k := range []string{"seeds_truncated", "paths_truncated", "total_is_exact"} {
		props[k] = boolSchema()
		required = append(required, k)
	}
	contexts := arrayOf(stringSchema())
	contexts["minItems"], contexts["maxItems"], contexts["uniqueItems"] = 1, 32, true
	return ToolSpecification{Name: t.Name(), Description: "Find bounded candidate static wrapper paths to an exact message contract's direct publishers. Requires explicit context_ids; no two variants of one repo/project. At most one caller step, optionally through a recorded interface implementation. No runtime dispatch, delivery, DI resolution, recursion, or synthetic publisher facts. limit1..100 counts seeds and repeated evidence across paths; at most20 seeds,10000 inspected rows,2MiB materialization. Separate seed/path truncation.", InputSchema: objectSchema([]string{"symbol_id", "context_ids"}, map[string]interface{}{"symbol_id": stringSchema(), "context_ids": contexts, "limit": map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 100}}), OutputSchema: objectSchema(required, props)}
}
