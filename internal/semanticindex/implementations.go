package semanticindex

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"moedex/internal/semantic"
)

func (x *Index) interfacePosting(row uint64) [3]uint64 {
	return [3]uint64{x.field(interfacePostings, row, 0), x.field(interfacePostings, row, 1), x.field(interfacePostings, row, 2)}
}

func (x *Index) interfaceStart(symbol, context uint64) uint64 {
	return uint64(sort.Search(int(x.sections[interfacePostings].n), func(i int) bool {
		p := x.interfacePosting(uint64(i))
		return p[0] > symbol || p[0] == symbol && x.field(facts, p[1], 3) >= context
	}))
}

func (x *Index) validateInterfacePostings() error {
	if x.version < 5 {
		return nil
	}
	bad := func() error { return fmt.Errorf("semanticindex: corrupt interface implementation postings") }
	// The already validated implementation directory contains exactly one row
	// per relation. Equal cardinality, strict uniqueness, and exact target joins
	// below prove that the reverse directory contains every relation once.
	if x.sections[interfacePostings].n != x.sections[implementationPostings].n {
		return bad()
	}
	for row := uint64(0); row < x.sections[interfacePostings].n; row++ {
		p := x.interfacePosting(row)
		if p[0] >= x.sections[symbols].n || p[1] >= x.sections[facts].n {
			return bad()
		}
		var fs []semantic.ImplementationFact
		if json.Unmarshal(x.implementationPayload(p[1]), &fs) != nil || p[2] >= uint64(len(fs)) {
			return bad()
		}
		symbol, ok := x.domainSymbolRow(fs[p[2]].InterfaceSymbolID)
		if !ok || symbol != p[0] {
			return bad()
		}
		if row > 0 && !lessContractPosting(x.implementationKey(x.interfacePosting(row-1)), x.implementationKey(p)) {
			return bad()
		}
	}
	return nil
}

// Implementations discovers only contexts with recorded implementation facts
// when contextIDs is empty. Selected contexts return exact declaration evidence,
// not runtime dispatch, inherited implementation inference, or deployment scope.
func (x *Index) Implementations(ctx context.Context, symbolID string, contextIDs []string, limit int) (ImplementationsResult, error) {
	out := ImplementationsResult{WorkLimit: ContractWorkLimit}
	if err := x.check(ctx, limit); err != nil {
		return out, err
	}
	if !strings.HasPrefix(symbolID, "symbol:") || !hexBytes([]byte(strings.TrimPrefix(symbolID, "symbol:")), 64) {
		return out, fmt.Errorf("semanticindex: exact symbol ID required")
	}
	if x.version < 5 {
		return out, nil
	}
	out.Supported = true
	selected, err := x.contractContexts(contextIDs)
	if err != nil {
		return ImplementationsResult{}, err
	}
	symbol, ok := x.domainSymbolRow(symbolID)
	if !ok {
		return out, nil
	}
	start := x.interfaceStart(symbol, 0)
	if start == x.sections[interfacePostings].n || x.interfacePosting(start)[0] != symbol {
		// A known symbol without implementation evidence is not promoted to an
		// interface identity merely because its name resembles one.
		return out, nil
	}
	cost := 2 * x.rowCost(symbols, symbol, 0, 1, 2, 3, 4, 5)
	if cost > MaxQueryBytes {
		return ImplementationsResult{}, fmt.Errorf("semanticindex: query result exceeds byte budget")
	}
	iface := x.symbol(symbol)
	out.Interface = &iface
	addContext := func(fact uint64) error {
		s, c := x.field(facts, fact, 2), x.field(facts, fact, 3)
		cost += x.sourceCost(s, c)
		if cost > MaxQueryBytes {
			return fmt.Errorf("semanticindex: query result exceeds byte budget")
		}
		out.Contexts = append(out.Contexts, x.source(s, c))
		return nil
	}
	if len(contextIDs) == 0 {
		contextLimit := min(limit, MaxContractContexts)
		for row := start; row < x.sections[interfacePostings].n; {
			if err := ctx.Err(); err != nil {
				return ImplementationsResult{}, err
			}
			p := x.interfacePosting(row)
			if p[0] != symbol {
				break
			}
			if len(out.Contexts) == contextLimit || out.RowsVisited == ContractWorkLimit {
				out.Truncated = true
				break
			}
			out.RowsVisited++
			if err := addContext(p[1]); err != nil {
				return ImplementationsResult{}, err
			}
			// Seek directly past all declarations in this context. Discovery
			// never scans an unbounded list of matching implementations.
			row = x.interfaceStart(symbol, x.field(facts, p[1], 3)+1)
		}
	} else {
		for _, c := range selected {
			contextAdded := false
			for row := x.interfaceStart(symbol, c); row < x.sections[interfacePostings].n; row++ {
				if err := ctx.Err(); err != nil {
					return ImplementationsResult{}, err
				}
				p := x.interfacePosting(row)
				if p[0] != symbol || x.field(facts, p[1], 3) != c {
					break
				}
				if len(out.Matches) == limit || out.RowsVisited == ContractWorkLimit {
					out.Truncated = true
					return out, nil
				}
				out.RowsVisited++
				if !contextAdded {
					if err := addContext(p[1]); err != nil {
						return ImplementationsResult{}, err
					}
					contextAdded = true
				}
				cost += x.resultCost(p[1]) + 128
				if cost > MaxQueryBytes {
					return ImplementationsResult{}, fmt.Errorf("semanticindex: query result exceeds byte budget")
				}
				out.Matches = append(out.Matches, ImplementationMatch{Result: x.result(p[1]), ImplementationFactIndex: int(p[2])})
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return ImplementationsResult{}, err
	}
	return out, nil
}
