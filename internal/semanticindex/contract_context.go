package semanticindex

import (
	"context"
	"fmt"
	"sort"

	"moedex/internal/semantic"
)

// ContractContextResult combines exact framework observations and source
// definitions. Each context is a separate captured compilation, not an app.
type ContractContextResult struct {
	Supported             bool
	Contract              *semantic.Symbol
	Contexts              []SourceContext
	Matches               []ContractImpactMatch
	Definitions           []Result
	EvidenceTruncated     bool
	DefinitionsTruncated  bool
	ContextsTruncated     bool
	PostingsVisited       int
	DefinitionRowsVisited int
	WorkLimit             int
}

// ContractContext reserves separate result budgets for evidence and definitions.
// Omitted scopes discover both fact and definition-only contexts. The definition
// directory is scanned once for the exact symbol, including skipped contexts in
// the shared work bound. No persisted format or dependency-closure inference is
// required. Returned records own their data after index closure.
func (x *Index) ContractContext(ctx context.Context, symbolID string, contextIDs []string, evidenceLimit, definitionLimit int) (ContractContextResult, error) {
	var out ContractContextResult
	if evidenceLimit < 1 || definitionLimit < 1 || evidenceLimit >= MaxResults || definitionLimit >= MaxResults || evidenceLimit+definitionLimit > MaxResults {
		return out, fmt.Errorf("semanticindex: positive evidence and definition limits must sum to at most %d", MaxResults)
	}
	limit := evidenceLimit
	if len(contextIDs) == 0 {
		limit = MaxContractContexts
	}
	impact, err := x.ContractImpact(ctx, symbolID, contextIDs, limit)
	if err != nil {
		return out, err
	}
	out = ContractContextResult{Supported: impact.Supported, Contract: impact.Contract, Contexts: impact.Contexts, Matches: impact.Matches, PostingsVisited: impact.PostingsVisited, WorkLimit: ContractWorkLimit}
	if len(contextIDs) == 0 {
		out.ContextsTruncated = impact.Truncated
	} else {
		out.EvidenceTruncated = impact.Truncated
	}
	if !impact.Supported || impact.Contract == nil {
		return out, nil
	}
	selected, err := x.contractContexts(contextIDs)
	if err != nil {
		return ContractContextResult{}, err
	}
	selectedRows := map[uint64]bool{}
	for _, row := range selected {
		selectedRows[row] = true
	}
	seen := map[string]bool{}
	for _, c := range out.Contexts {
		seen[c.Context.ID] = true
	}
	cost := impact.materializationBytes
	symbol, _ := x.domainSymbolRow(symbolID)
	want := symbol + 1
	start := sort.Search(int(x.sections[definitions].n), func(j int) bool { return x.field(facts, x.field(definitions, uint64(j), 0), 9) >= want })
	for j := uint64(start); j < x.sections[definitions].n; j++ {
		if err := ctx.Err(); err != nil {
			return ContractContextResult{}, err
		}
		i := x.field(definitions, j, 0)
		if x.field(facts, i, 9) != want {
			break
		}
		if out.PostingsVisited+out.DefinitionRowsVisited == ContractWorkLimit {
			if len(contextIDs) == 0 {
				out.ContextsTruncated = true
			} else {
				out.DefinitionsTruncated = true
			}
			break
		}
		out.DefinitionRowsVisited++
		s, c := x.field(facts, i, 2), x.field(facts, i, 3)
		if len(contextIDs) > 0 && !selectedRows[c] {
			continue
		}
		id := x.text(x.field(contexts, c, 0))
		if len(contextIDs) == 0 && seen[id] {
			continue
		}
		if len(contextIDs) == 0 && len(out.Contexts) == MaxContractContexts {
			out.ContextsTruncated = true
			break
		}
		if len(contextIDs) > 0 && len(out.Definitions) == definitionLimit {
			out.DefinitionsTruncated = true
			break
		}
		addedCost := uint64(0)
		if !seen[id] {
			addedCost += x.sourceCost(s, c)
		}
		if len(contextIDs) > 0 {
			addedCost += x.resultCost(i)
		}
		if addedCost > MaxQueryBytes || cost > MaxQueryBytes-addedCost {
			return ContractContextResult{}, fmt.Errorf("semanticindex: query result exceeds byte budget")
		}
		cost += addedCost
		if !seen[id] {
			out.Contexts = append(out.Contexts, x.source(s, c))
			seen[id] = true
		}
		if len(contextIDs) > 0 {
			out.Definitions = append(out.Definitions, x.result(i))
		}
	}
	sort.Slice(out.Contexts, func(i, j int) bool { return out.Contexts[i].Context.ID < out.Contexts[j].Context.ID })
	return out, nil
}
