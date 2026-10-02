package semanticindex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"moedex/internal/semantic"
)

const ContractWorkLimit = 10000
const MaxContractContexts = 32

var ErrContractContextSelection = errors.New("semanticindex: invalid contract context selection")

func lessContractPosting(a, b [4]uint64) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
func (x *Index) contractPosting(row uint64) [4]uint64 {
	return [4]uint64{x.field(contractPostings, row, 0), x.field(contractPostings, row, 1), x.field(contractPostings, row, 2), x.field(contractPostings, row, 3)}
}
func (x *Index) contractKey(row uint64) [5]uint64 {
	p := x.contractPosting(row)
	return [5]uint64{p[0], x.field(facts, p[1], 3), p[1], p[2], p[3]}
}
func lessContractKey(a, b [5]uint64) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
func (x *Index) contractStart(symbol, context uint64) uint64 {
	return uint64(sort.Search(int(x.sections[contractPostings].n), func(i int) bool {
		p := x.contractPosting(uint64(i))
		return p[0] > symbol || p[0] == symbol && x.field(facts, p[1], 3) >= context
	}))
}
func (x *Index) domainRow(fact uint64) (uint64, bool) {
	i := uint64(sort.Search(int(x.sections[domainFacts].n), func(i int) bool { return x.field(domainFacts, uint64(i), 0) >= fact }))
	return i, i < x.sections[domainFacts].n && x.field(domainFacts, i, 0) == fact
}
func (x *Index) validateContractPostings() error {
	if x.version < 3 {
		return nil
	}
	bad := func() error { return fmt.Errorf("semanticindex: corrupt contract postings") }
	payloads := map[uint64][]semantic.DomainFact{}
	var expected uint64
	for row := uint64(0); row < x.sections[domainFacts].n; row++ {
		sid := x.field(domainFacts, row, 1)
		domains, ok := payloads[sid]
		if !ok {
			if json.Unmarshal(x.textBytes(sid), &domains) != nil {
				return bad()
			}
			payloads[sid] = domains
		}
		for _, domain := range domains {
			expected += uint64(len(domain.Targets))
		}
	}
	if expected != x.sections[contractPostings].n {
		return bad()
	}
	for row := uint64(0); row < x.sections[contractPostings].n; row++ {
		p := x.contractPosting(row)
		if p[0] >= x.sections[symbols].n || p[1] >= x.sections[facts].n {
			return bad()
		}
		d, ok := x.domainRow(p[1])
		if !ok {
			return bad()
		}
		domains := payloads[x.field(domainFacts, d, 1)]
		if p[2] >= uint64(len(domains)) || p[3] >= uint64(len(domains[p[2]].Targets)) {
			return bad()
		}
		sym, ok := x.domainSymbolRow(domains[p[2]].Targets[p[3]].SymbolID)
		if !ok || sym != p[0] {
			return bad()
		}
		if row > 0 && !lessContractKey(x.contractKey(row-1), x.contractKey(row)) {
			return bad()
		}
	}
	return nil
}
func (x *Index) contractContexts(ids []string) ([]uint64, error) {
	if len(ids) > MaxContractContexts {
		return nil, fmt.Errorf("%w: at most %d contexts", ErrContractContextSelection, MaxContractContexts)
	}
	seen := map[uint64]bool{}
	projects := map[[2]uint64]uint64{}
	var rows []uint64
	for _, id := range ids {
		if !strings.HasPrefix(id, "context:") || !hexBytes([]byte(strings.TrimPrefix(id, "context:")), 64) {
			return nil, fmt.Errorf("%w: malformed context ID", ErrContractContextSelection)
		}
		sid, ok := x.strID(id)
		if !ok {
			return nil, fmt.Errorf("%w: unknown context %s", ErrContractContextSelection, id)
		}
		row := uint64(sort.Search(int(x.sections[contexts].n), func(i int) bool { return x.field(contexts, uint64(i), 0) >= sid }))
		if row == x.sections[contexts].n || x.field(contexts, row, 0) != sid {
			return nil, fmt.Errorf("%w: unknown context %s", ErrContractContextSelection, id)
		}
		if seen[row] {
			return nil, fmt.Errorf("%w: duplicate context ID", ErrContractContextSelection)
		}
		seen[row] = true
		snapshot := x.field(contexts, row, 1)
		project := [2]uint64{x.field(snapshots, snapshot, 1), x.field(contexts, row, 2)}
		if other, exists := projects[project]; exists && other != row {
			return nil, fmt.Errorf("%w: multiple contexts for the same repo/project", ErrContractContextSelection)
		}
		projects[project] = row
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i] < rows[j] })
	return rows, nil
}

// ContractImpact retrieves separate compiler observations of an exact type
// identity. Empty contextIDs discovers contexts only. Explicit distinct projects
// are not asserted to share a compatible dependency closure or runtime process.
func (x *Index) ContractImpact(ctx context.Context, symbolID string, contextIDs []string, limit int) (ContractImpactResult, error) {
	out := ContractImpactResult{WorkLimit: ContractWorkLimit}
	if err := x.check(ctx, limit); err != nil {
		return out, err
	}
	if !strings.HasPrefix(symbolID, "symbol:") || !hexBytes([]byte(strings.TrimPrefix(symbolID, "symbol:")), 64) {
		return out, fmt.Errorf("semanticindex: exact symbol ID required")
	}
	if x.version < 3 {
		return out, nil
	}
	out.Supported = true
	selected, err := x.contractContexts(contextIDs)
	if err != nil {
		return ContractImpactResult{}, err
	}
	symbol, ok := x.domainSymbolRow(symbolID)
	if !ok {
		return out, nil
	}
	cost := 2 * x.rowCost(symbols, symbol, 0, 1, 2, 3, 4, 5)
	if cost > MaxQueryBytes {
		return ContractImpactResult{}, fmt.Errorf("semanticindex: query result exceeds byte budget")
	}
	out.materializationBytes = cost
	contract := x.symbol(symbol)
	out.Contract = &contract
	addContext := func(fact uint64) error {
		s, c := x.field(facts, fact, 2), x.field(facts, fact, 3)
		cost += x.sourceCost(s, c)
		if cost > MaxQueryBytes {
			return fmt.Errorf("semanticindex: query result exceeds byte budget")
		}
		out.materializationBytes = cost
		out.Contexts = append(out.Contexts, x.source(s, c))
		return nil
	}
	if len(contextIDs) == 0 {
		for row := x.contractStart(symbol, 0); row < x.sections[contractPostings].n; {
			if err := ctx.Err(); err != nil {
				return ContractImpactResult{}, err
			}
			p := x.contractPosting(row)
			if p[0] != symbol {
				break
			}
			if len(out.Contexts) == limit || out.PostingsVisited == ContractWorkLimit {
				out.Truncated = true
				break
			}
			out.PostingsVisited++
			if err := addContext(p[1]); err != nil {
				return ContractImpactResult{}, err
			}
			// Directory order permits skipping a whole context without inspecting its
			// potentially millions of occurrences during context discovery.
			row = x.contractStart(symbol, x.field(facts, p[1], 3)+1)
		}
		return out, nil
	}
	for _, selectedContext := range selected {
		contextAdded := false
		for row := x.contractStart(symbol, selectedContext); row < x.sections[contractPostings].n; {
			if err := ctx.Err(); err != nil {
				return ContractImpactResult{}, err
			}
			p := x.contractPosting(row)
			if p[0] != symbol || x.field(facts, p[1], 3) != selectedContext {
				break
			}
			if len(out.Matches) == limit || out.PostingsVisited == ContractWorkLimit {
				out.Truncated = true
				return out, nil
			}
			var domains []semantic.DomainFact
			_ = json.Unmarshal(x.domainPayload(p[1]), &domains)
			domain := domains[p[2]]
			var roles []string
			for _, target := range domain.Targets {
				if target.SymbolID == symbolID {
					roles = append(roles, target.Role)
				}
			}
			if out.PostingsVisited+len(roles) > ContractWorkLimit {
				out.Truncated = true
				return out, nil
			}
			out.PostingsVisited += len(roles)
			if !contextAdded {
				if err := addContext(p[1]); err != nil {
					return ContractImpactResult{}, err
				}
				contextAdded = true
			}
			cost += x.resultCost(p[1]) + uint64(128+len(roles)*32)
			owner := x.field(facts, p[1], 10)
			if owner > 0 {
				cost += 2 * x.rowCost(symbols, owner-1, 0, 1, 2, 3, 4, 5)
			}
			if cost > MaxQueryBytes {
				return ContractImpactResult{}, fmt.Errorf("semanticindex: query result exceeds byte budget")
			}
			out.materializationBytes = cost
			match := ContractImpactMatch{Result: x.result(p[1]), DomainFactIndex: int(p[2]), TargetRoles: roles}
			if owner > 0 {
				s := x.symbol(owner - 1)
				match.Owner = &s
			}
			out.Matches = append(out.Matches, match)
			row += uint64(len(roles))
		}
	}
	return out, nil
}
