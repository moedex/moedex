package semanticindex

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// EvidencePathResult is a bounded bundle of recorded observations about one
// class. It deliberately does not turn registration or forwarding into dispatch.
type EvidencePathResult struct {
	Supported          bool
	Records            []Result
	RequiredContextIDs []string
	RowsVisited        int
	Truncated          bool
}

// EvidencePath collects the class declaration, framework observations naming
// that class, and exact definitions/call witnesses referenced by default
// selections. All reads share one row, record and materialization budget.
// Cross-context witnesses are returned only after explicit context selection.
func (x *Index) EvidencePath(ctx context.Context, symbolID string, contextIDs []string, limit int) (EvidencePathResult, error) {
	out := EvidencePathResult{}
	if err := x.check(ctx, limit); err != nil {
		return out, err
	}
	if !strings.HasPrefix(symbolID, "symbol:") || !hexBytes([]byte(strings.TrimPrefix(symbolID, "symbol:")), 64) {
		return out, fmt.Errorf("semanticindex: exact symbol ID required")
	}
	if len(contextIDs) == 0 {
		return out, fmt.Errorf("%w: explicit contexts required", ErrContractContextSelection)
	}
	selected, err := x.contractContexts(contextIDs)
	if err != nil {
		return out, err
	}
	if x.version < 4 {
		return out, nil
	}
	out.Supported = true
	sym, ok := x.domainSymbolRow(symbolID)
	if !ok {
		return out, nil
	}
	if !strings.HasPrefix(x.text(x.field(symbols, sym, 4)), "T:") {
		return out, fmt.Errorf("semanticindex: class type symbol required")
	}
	selectedRows := map[uint64]bool{}
	selectedIDs := map[string]bool{}
	for _, c := range selected {
		selectedRows[c] = true
		selectedIDs[x.text(x.field(contexts, c, 0))] = true
	}
	seen := map[uint64]bool{}
	required := map[string]bool{}
	var cost uint64
	visit := func() bool {
		if out.RowsVisited == ContractWorkLimit {
			out.Truncated = true
			return false
		}
		out.RowsVisited++
		return true
	}
	add := func(row uint64) bool {
		if seen[row] {
			return true
		}
		n := x.resultCost(row)
		if len(out.Records) == limit || n > MaxQueryBytes || cost > MaxQueryBytes-n {
			out.Truncated = true
			return false
		}
		cost += n
		seen[row] = true
		out.Records = append(out.Records, x.result(row))
		return true
	}
	definitions := func(id string, c uint64) error {
		target, ok := x.domainSymbolRow(id)
		if !ok {
			return nil
		}
		want := target + 1
		start := sort.Search(int(x.sections[definitions].n), func(j int) bool { return x.field(facts, x.field(definitions, uint64(j), 0), 9) >= want })
		for j := uint64(start); j < x.sections[definitions].n; j++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			row := x.field(definitions, j, 0)
			if x.field(facts, row, 9) != want {
				break
			}
			if !visit() {
				break
			}
			if x.field(facts, row, 3) != c {
				continue
			}
			if !add(row) {
				break
			}
		}
		return nil
	}
	// Definitions first so tight limits preserve the requested class identity.
	for _, c := range selected {
		if err := definitions(symbolID, c); err != nil {
			return EvidencePathResult{}, err
		}
		if out.Truncated {
			break
		}
	}
	seeds := append([]Result(nil), out.Records...)
	for _, seed := range seeds {
		if out.Truncated {
			break
		}
		for _, fact := range seed.Binding.ImplementationFacts {
			if err := ctx.Err(); err != nil {
				return EvidencePathResult{}, err
			}
			if fact.Kind != "interface_default_selection" || fact.ImplementingTypeSymbolID != symbolID {
				continue
			}
			f := fact.Forwarding
			if f == nil {
				continue
			}
			if !selectedIDs[f.CallContextID] {
				required[f.CallContextID] = true
			}
			var own uint64
			for _, c := range selected {
				if x.text(x.field(contexts, c, 0)) == seed.Context.ID {
					own = c
					break
				}
			}
			if err := definitions(f.ImplementationSymbolID, own); err != nil {
				return EvidencePathResult{}, err
			}
			if out.Truncated {
				break
			}
			if !selectedIDs[f.CallContextID] {
				continue
			}
			row := uint64(sort.Search(int(x.sections[facts].n), func(i int) bool { return x.text(x.field(facts, uint64(i), 0)) >= f.CallOccurrenceID }))
			if row == x.sections[facts].n || x.text(x.field(facts, row, 0)) != f.CallOccurrenceID {
				return EvidencePathResult{}, fmt.Errorf("semanticindex: missing forwarding witness")
			}
			if !visit() {
				break
			}
			c := x.field(facts, row, 3)
			if !selectedRows[c] || x.text(x.field(contexts, c, 0)) != f.CallContextID {
				return EvidencePathResult{}, fmt.Errorf("semanticindex: inconsistent forwarding context")
			}
			if !add(row) {
				break
			}
			if err := definitions(fact.DefaultTemplateSymbolID, c); err != nil {
				return EvidencePathResult{}, err
			}
			if out.Truncated {
				break
			}
		}
	}
	for _, c := range selected {
		if out.Truncated {
			break
		}
		for row := x.contractStart(sym, c); row < x.sections[contractPostings].n; row++ {
			if err := ctx.Err(); err != nil {
				return EvidencePathResult{}, err
			}
			p := x.contractPosting(row)
			if p[0] != sym || x.field(facts, p[1], 3) != c {
				break
			}
			if !visit() || !add(p[1]) {
				break
			}
		}
	}
	for id := range required {
		out.RequiredContextIDs = append(out.RequiredContextIDs, id)
	}
	sort.Strings(out.RequiredContextIDs)
	if len(out.RequiredContextIDs) > MaxContractContexts {
		out.RequiredContextIDs = out.RequiredContextIDs[:MaxContractContexts]
		out.Truncated = true
	}
	if err := ctx.Err(); err != nil {
		return EvidencePathResult{}, err
	}
	return out, nil
}
