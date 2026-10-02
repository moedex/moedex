package mcp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"moedex/internal/semantic"
	"moedex/internal/semanticindex"
)

type compilerContractContextReader interface {
	ContractContext(context.Context, string, []string, int, int) (semanticindex.ContractContextResult, error)
}
type compilerContractContextTool struct{ provider CompilerSessionProvider }

func (*compilerContractContextTool) Name() string { return "compiler_contract_context" }

type CompilerContractLocation struct {
	OccurrenceID     string `json:"occurrence_id"`
	SourceID         string `json:"source_id"`
	Path             string `json:"path"`
	RawSHA256        string `json:"raw_sha256"`
	ByteOffset       uint64 `json:"byte_offset"`
	ByteLength       uint64 `json:"byte_length"`
	Generated        bool   `json:"generated"`
	ReferenceKind    string `json:"reference_kind"`
	Role             string `json:"role"`
	BindingStatus    string `json:"binding_status"`
	Method           string `json:"method"`
	Extractor        string `json:"extractor"`
	ExtractorVersion string `json:"extractor_version"`
}
type CompilerContractEvidence struct {
	Source       CompilerContractLocation `json:"source"`
	APISymbolID  string                   `json:"api_symbol_id"`
	MatchedRoles []string                 `json:"matched_roles"`
	Fact         semantic.DomainFact      `json:"fact"`
}
type CompilerContractGroup struct {
	ContextID        string                     `json:"context_id"`
	Repo             string                     `json:"repo"`
	Project          string                     `json:"project"`
	SourceSnapshotID string                     `json:"source_snapshot_id"`
	OwnerID          string                     `json:"owner_id,omitempty"`
	Evidence         []CompilerContractEvidence `json:"evidence"`
}
type CompilerContractDefinition struct {
	ContextID        string                   `json:"context_id"`
	Repo             string                   `json:"repo"`
	Project          string                   `json:"project"`
	SourceSnapshotID string                   `json:"source_snapshot_id"`
	Source           CompilerContractLocation `json:"source"`
}
type CompilerContractContextResult struct {
	Status                   string                       `json:"status"`
	EvidenceScope            string                       `json:"evidence_scope"`
	SnapshotID               string                       `json:"snapshot_id"`
	ArtifactSHA256           string                       `json:"artifact_sha256"`
	ContractID               string                       `json:"contract_id"`
	ContextIDs               []string                     `json:"context_ids"`
	Contexts                 []CompilerContext            `json:"contexts"`
	Symbols                  []CompilerSymbol             `json:"symbols"`
	Groups                   []CompilerContractGroup      `json:"groups"`
	Definitions              []CompilerContractDefinition `json:"definitions"`
	Count                    int                          `json:"count"`
	EvidenceLimit            int                          `json:"evidence_limit"`
	DefinitionLimit          int                          `json:"definition_limit"`
	EvidenceTruncated        bool                         `json:"evidence_truncated"`
	DefinitionsTruncated     bool                         `json:"definitions_truncated"`
	ContextsTruncated        bool                         `json:"contexts_truncated"`
	TotalIsExact             bool                         `json:"total_is_exact"`
	PostingsVisited          int                          `json:"postings_visited"`
	DefinitionRowsVisited    int                          `json:"definition_rows_visited"`
	WorkLimit                int                          `json:"work_limit"`
	MaterializationByteLimit int                          `json:"materialization_byte_limit"`
	Limitations              []string                     `json:"limitations"`
	Error                    *compilerError               `json:"error,omitempty"`
}

func contractLocation(r semanticindex.Result) CompilerContractLocation {
	return CompilerContractLocation{r.Occurrence.ID, r.Source.ID, r.Source.Path, r.Source.RawSHA256, r.Occurrence.Offset, r.Occurrence.Length, r.Source.Generated, r.Occurrence.Kind, r.Occurrence.Role, r.Binding.Status, r.Binding.Method, r.Binding.Extractor, r.Binding.ExtractorVersion}
}
func exactContractID(id, prefix string) bool {
	if !strings.HasPrefix(id, prefix) || strings.ToLower(id) != id {
		return false
	}
	b, err := hex.DecodeString(strings.TrimPrefix(id, prefix))
	return err == nil && len(b) == 32
}
func (t *compilerContractContextTool) Call(ctx context.Context, raw json.RawMessage) (map[string]interface{}, error) {
	out := CompilerContractContextResult{Status: "semantic_unavailable", EvidenceScope: "compile_time", ContextIDs: []string{}, Contexts: []CompilerContext{}, Symbols: []CompilerSymbol{}, Groups: []CompilerContractGroup{}, Definitions: []CompilerContractDefinition{}, EvidenceLimit: 80, DefinitionLimit: 20, WorkLimit: semanticindex.ContractWorkLimit, MaterializationByteLimit: semanticindex.MaxQueryBytes, Limitations: []string{"Captured compiler observations only; no runtime delivery, activation, service resolution, or database activity.", "Explicit contexts remain separate compilations; no dependency-closure compatibility is inferred.", "Only captured supported API patterns are represented; wrappers, runtime configuration, and missing captures can hide dependencies."}}
	respond := func(message string, bad bool) map[string]interface{} {
		return StructuredResultWithSnapshot(message, out, bad, SnapshotIdentity{Cacheable: false})
	}
	invalid := func(message string) (map[string]interface{}, error) {
		out.Status = "invalid_arguments"
		out.Error = &compilerError{Code: out.Status, Message: message}
		return respond(message, true), nil
	}
	var args struct {
		Symbol          string   `json:"symbol_id"`
		Contexts        []string `json:"context_ids"`
		EvidenceLimit   *int     `json:"evidence_limit"`
		DefinitionLimit *int     `json:"definition_limit"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return invalid("Expected symbol_id, optional context_ids, evidence_limit and definition_limit.")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return invalid("Arguments must be one object.")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return invalid("Arguments must be one object.")
	}
	if !exactContractID(args.Symbol, "symbol:") {
		return invalid("An exact symbol_id is required.")
	}
	if args.EvidenceLimit != nil {
		out.EvidenceLimit = *args.EvidenceLimit
	}
	if args.DefinitionLimit != nil {
		out.DefinitionLimit = *args.DefinitionLimit
	}
	for _, k := range []string{"evidence_limit", "definition_limit"} {
		if v, ok := fields[k]; ok && bytes.Equal(v, []byte("null")) {
			return invalid("Limits must be positive integers summing to at most 100.")
		}
	}
	if out.EvidenceLimit < 1 || out.DefinitionLimit < 1 || out.EvidenceLimit > 99 || out.DefinitionLimit > 99 || out.EvidenceLimit+out.DefinitionLimit > 100 {
		return invalid("Limits must be positive integers summing to at most 100.")
	}
	if _, ok := fields["context_ids"]; ok && (len(args.Contexts) == 0 || len(args.Contexts) > semanticindex.MaxContractContexts) {
		return invalid("Select 1..32 unique context_ids or omit them for discovery.")
	}
	selected := map[string]bool{}
	for _, id := range args.Contexts {
		if !exactContractID(id, "context:") || selected[id] {
			return invalid("Select unique exact context_ids.")
		}
		selected[id] = true
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.provider == nil {
		return respond("Compiler evidence is unavailable.", false), nil
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
		return respond("Compiler evidence is unavailable.", false), nil
	}
	reader, ok := session.Reader.(compilerContractContextReader)
	if !ok {
		out.Status = "capability_unavailable"
		return respond("This reader does not support combined contract context.", false), nil
	}
	query, err := reader.ContractContext(ctx, args.Symbol, args.Contexts, out.EvidenceLimit, out.DefinitionLimit)
	if err != nil {
		if errors.Is(err, semanticindex.ErrContractContextSelection) {
			out.Status = "invalid_context_selection"
			out.Error = &compilerError{Code: out.Status, Message: err.Error()}
			return respond(err.Error(), true), nil
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !query.Supported {
		out.Status = "index_upgrade_required"
		return respond("Rebuild the compiler index to enable combined contract context.", false), nil
	}
	if len(query.Matches) > out.EvidenceLimit || len(query.Definitions) > out.DefinitionLimit || len(query.Contexts) > semanticindex.MaxContractContexts || query.PostingsVisited < 0 || query.DefinitionRowsVisited < 0 || query.PostingsVisited > semanticindex.ContractWorkLimit || query.DefinitionRowsVisited > semanticindex.ContractWorkLimit-query.PostingsVisited {
		return nil, fmt.Errorf("compiler context reader exceeded query bounds")
	}
	out.EvidenceTruncated, out.DefinitionsTruncated, out.ContextsTruncated = query.EvidenceTruncated, query.DefinitionsTruncated, query.ContextsTruncated
	out.PostingsVisited, out.DefinitionRowsVisited = query.PostingsVisited, query.DefinitionRowsVisited
	symbols := map[string]CompilerSymbol{}
	addSymbol := func(s semantic.Symbol) error {
		v := compilerSymbol(s)
		if old, ok := symbols[s.ID]; ok && old != v {
			return fmt.Errorf("compiler context returned contradictory symbol identity")
		}
		symbols[s.ID] = v
		return nil
	}
	if query.Contract != nil {
		if query.Contract.ID != args.Symbol {
			return nil, fmt.Errorf("compiler context returned wrong contract")
		}
		out.ContractID = query.Contract.ID
		if err := addSymbol(*query.Contract); err != nil {
			return nil, err
		}
	}
	for _, c := range query.Contexts {
		if len(args.Contexts) > 0 && !selected[c.Context.ID] {
			return nil, fmt.Errorf("compiler context returned unselected choice")
		}
		out.Contexts = append(out.Contexts, CompilerContext{Generated: c.Source.Generated, ContextID: c.Context.ID, Project: c.Context.Project, Repo: c.Snapshot.Repo, Path: c.Source.Path, RawSHA256: c.Source.RawSHA256})
	}
	if len(args.Contexts) == 0 {
		if len(query.Matches) > 0 || len(query.Definitions) > 0 {
			return nil, fmt.Errorf("compiler context discovery returned unscoped evidence")
		}
		out.Status = "context_required"
	} else {
		out.ContextIDs = append(out.ContextIDs, args.Contexts...)
		groupRows := map[[2]string]int{}
		for _, match := range query.Matches {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			r := match.Result
			if query.Contract == nil || !selected[r.Context.ID] || r.Symbol == nil || r.Binding.Status != "resolved" || match.DomainFactIndex < 0 || match.DomainFactIndex >= len(r.Binding.DomainFacts) {
				return nil, fmt.Errorf("compiler context returned invalid observation")
			}
			fact := r.Binding.DomainFacts[match.DomainFactIndex]
			var roles []string
			for _, target := range fact.Targets {
				if target.SymbolID == args.Symbol {
					roles = append(roles, target.Role)
				}
			}
			a, _ := json.Marshal(roles)
			b, _ := json.Marshal(match.TargetRoles)
			if len(roles) == 0 || !bytes.Equal(a, b) || fact.EvidenceScope != "compile_time" {
				return nil, fmt.Errorf("compiler context returned inconsistent target roles")
			}
			if err := addSymbol(*r.Symbol); err != nil {
				return nil, err
			}
			for _, s := range r.DomainSymbols {
				if err := addSymbol(s); err != nil {
					return nil, err
				}
			}
			for _, target := range fact.Targets {
				if _, ok := symbols[target.SymbolID]; !ok {
					return nil, fmt.Errorf("compiler context lacks target descriptor")
				}
			}
			owner := ""
			if match.Owner != nil {
				owner = match.Owner.ID
				if owner != r.Binding.EnclosingSymbolID {
					return nil, fmt.Errorf("compiler context returned inconsistent owner")
				}
				if err := addSymbol(*match.Owner); err != nil {
					return nil, err
				}
			} else if r.Binding.EnclosingSymbolID != "" {
				return nil, fmt.Errorf("compiler context lacks owner descriptor")
			}
			key := [2]string{r.Context.ID, owner}
			n, ok := groupRows[key]
			if !ok {
				n = len(out.Groups)
				groupRows[key] = n
				out.Groups = append(out.Groups, CompilerContractGroup{ContextID: r.Context.ID, Repo: r.Snapshot.Repo, Project: r.Context.Project, SourceSnapshotID: r.Snapshot.ID, OwnerID: owner, Evidence: []CompilerContractEvidence{}})
			}
			if out.Groups[n].Repo != r.Snapshot.Repo || out.Groups[n].Project != r.Context.Project || out.Groups[n].SourceSnapshotID != r.Snapshot.ID {
				return nil, fmt.Errorf("compiler context returned inconsistent group provenance")
			}
			out.Groups[n].Evidence = append(out.Groups[n].Evidence, CompilerContractEvidence{Source: contractLocation(r), APISymbolID: r.Symbol.ID, MatchedRoles: append([]string{}, roles...), Fact: fact})
			out.Count++
		}
		for _, r := range query.Definitions {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !selected[r.Context.ID] || r.Symbol == nil || r.Symbol.ID != args.Symbol || r.Occurrence.Role != "declaration" {
				return nil, fmt.Errorf("compiler context returned invalid definition")
			}
			if err := addSymbol(*r.Symbol); err != nil {
				return nil, err
			}
			out.Definitions = append(out.Definitions, CompilerContractDefinition{ContextID: r.Context.ID, Repo: r.Snapshot.Repo, Project: r.Context.Project, SourceSnapshotID: r.Snapshot.ID, Source: contractLocation(r)})
		}
		out.Status = "ok"
		if out.Count == 0 && len(out.Definitions) == 0 {
			out.Status = "no_recorded_evidence"
		}
		out.TotalIsExact = !out.EvidenceTruncated && !out.DefinitionsTruncated
	}
	for _, s := range symbols {
		out.Symbols = append(out.Symbols, s)
	}
	sort.Slice(out.Symbols, func(i, j int) bool { return out.Symbols[i].ID < out.Symbols[j].ID })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return respond("Source-linked compiler observations and contract definitions. Select contexts explicitly; missing evidence is not dependency absence. See limitations and separate truncation flags.", false), nil
}

func (t *compilerContractContextTool) Specification() ToolSpecification {
	symbols, _, _, choices := compilerEvidenceSchemas()
	contexts := arrayOf(stringSchema())
	contexts["minItems"], contexts["maxItems"], contexts["uniqueItems"] = 1, 32, true
	limit := map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 99}
	input := objectSchema([]string{"symbol_id"}, map[string]interface{}{"symbol_id": map[string]interface{}{"type": "string", "pattern": "^symbol:[0-9a-f]{64}$"}, "context_ids": contexts, "evidence_limit": limit, "definition_limit": limit})
	lp := map[string]interface{}{}
	required := []string{}
	for _, k := range []string{"occurrence_id", "source_id", "path", "raw_sha256", "reference_kind", "role", "binding_status", "method", "extractor", "extractor_version"} {
		lp[k] = stringSchema()
		required = append(required, k)
	}
	lp["generated"] = boolSchema()
	lp["byte_offset"] = intSchema()
	lp["byte_length"] = intSchema()
	required = append(required, "generated", "byte_offset", "byte_length")
	location := objectSchema(required, lp)
	target := objectSchema([]string{"role", "symbol_id"}, map[string]interface{}{"role": stringSchema(), "symbol_id": stringSchema()})
	fact := objectSchema([]string{"kind", "rule", "evidence_scope", "targets"}, map[string]interface{}{"kind": stringSchema(), "rule": stringSchema(), "evidence_scope": stringSchema(), "targets": arrayOf(target), "lifetime": stringSchema(), "table": stringSchema(), "schema": stringSchema(), "route_pattern": stringSchema()})
	evidence := objectSchema([]string{"source", "api_symbol_id", "matched_roles", "fact"}, map[string]interface{}{"source": location, "api_symbol_id": stringSchema(), "matched_roles": arrayOf(stringSchema()), "fact": fact})
	group := objectSchema([]string{"context_id", "repo", "project", "source_snapshot_id", "evidence"}, map[string]interface{}{"context_id": stringSchema(), "repo": stringSchema(), "project": stringSchema(), "source_snapshot_id": stringSchema(), "owner_id": stringSchema(), "evidence": arrayOf(evidence)})
	definition := objectSchema([]string{"context_id", "repo", "project", "source_snapshot_id", "source"}, map[string]interface{}{"context_id": stringSchema(), "repo": stringSchema(), "project": stringSchema(), "source_snapshot_id": stringSchema(), "source": location})
	props := map[string]interface{}{"context_ids": arrayOf(stringSchema()), "contexts": arrayOf(choices), "symbols": arrayOf(symbols), "groups": arrayOf(group), "definitions": arrayOf(definition), "limitations": arrayOf(stringSchema()), "error": errorPropertySchema()}
	required = []string{"context_ids", "contexts", "symbols", "groups", "definitions", "limitations"}
	for _, k := range []string{"status", "evidence_scope", "snapshot_id", "artifact_sha256", "contract_id"} {
		props[k] = stringSchema()
		required = append(required, k)
	}
	for _, k := range []string{"count", "evidence_limit", "definition_limit", "postings_visited", "definition_rows_visited", "work_limit", "materialization_byte_limit"} {
		props[k] = intSchema()
		required = append(required, k)
	}
	for _, k := range []string{"evidence_truncated", "definitions_truncated", "contexts_truncated", "total_is_exact"} {
		props[k] = boolSchema()
		required = append(required, k)
	}
	return ToolSpecification{Name: t.Name(), Description: "Retrieve a compact task context for an exact contract: recorded framework observations grouped by captured project and source owner, plus contract source definitions, in one immutable session. Symbol descriptors occur once in symbols; source hashes/spans preserve navigation. Omit context_ids to discover fact and definition-only contexts, then explicitly select compatible variants (no two contexts of one repo/project). Evidence and definition limits default80/20, must each be positive and sum to at most100; separate truncation flags and a shared10000-row/2MiB materialization budget apply. No runtime activation/delivery/dependency-closure inference; missing evidence is not absence.", InputSchema: input, OutputSchema: objectSchema(required, props)}
}
