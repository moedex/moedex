package semanticindex

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
)

// SymbolDiscoveryResult contains declaration variants, not merged identities or
// selected build contexts. RowsVisited includes nonmatching source occurrences.
type SymbolDiscoveryResult struct {
	QueryResult
	RowsVisited int
}

// FileSymbols seeks an exact indexed source, then inspects at most
// ContractWorkLimit occurrences. Query is a case-sensitive descriptor substring,
// never an identity. All context variants remain separate and carry provenance.
// Existing position postings suffice; this works with older index versions too.
func (x *Index) FileSymbols(ctx context.Context, repo, path, query string, limit int) (SymbolDiscoveryResult, error) {
	var out SymbolDiscoveryResult
	if err := x.check(ctx, limit); err != nil {
		return out, err
	}
	if err := validPosition(Position{Repo: repo, Path: path}); err != nil {
		return out, err
	}
	if len(query) > 256 || strings.ContainsAny(query, "\x00\r\n") {
		return out, fmt.Errorf("semanticindex: invalid descriptor query")
	}
	r, ok := x.strID(repo)
	if !ok {
		return out, nil
	}
	p, ok := x.strID(path)
	if !ok {
		return out, nil
	}
	want := [3]uint64{r, p, 0}
	start := sort.Search(int(x.sections[positions].n), func(j int) bool { return !less3(x.positionKey(x.field(positions, uint64(j), 0)), want) })
	var cost uint64
	for j := uint64(start); j < x.sections[positions].n; j++ {
		if err := ctx.Err(); err != nil {
			return SymbolDiscoveryResult{}, err
		}
		row := x.field(positions, j, 0)
		key := x.positionKey(row)
		if key[0] != r || key[1] != p {
			break
		}
		if out.RowsVisited == ContractWorkLimit {
			out.Truncated = true
			break
		}
		out.RowsVisited++
		// Role and resolved symbol are validated on Open. References and unresolved
		// declarations cannot supply an exact declaration identity.
		if !bytes.Equal(x.textBytes(x.field(facts, row, 6)), []byte("declaration")) {
			continue
		}
		symbol := x.field(facts, row, 9)
		if symbol == 0 || !bytes.Contains(x.textBytes(x.field(symbols, symbol-1, 4)), []byte(query)) {
			continue
		}
		if len(out.Results) == limit {
			out.Truncated = true
			break
		}
		// Include the separate context-choice projection conservatively per row.
		cost += x.resultCost(row) + x.sourceCost(x.field(facts, row, 2), x.field(facts, row, 3))
		if cost > MaxQueryBytes {
			out.Truncated = true
			break
		}
		out.Results = append(out.Results, x.result(row))
	}
	return out, nil
}
