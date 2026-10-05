package semanticindex

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"moedex/internal/semantic"
)

const MaxCallDepth = 8

// IsCallMethodDescriptor excludes nameless documentation IDs returned by some
// Roslyn anonymous-function symbols (for example M:Controller.). Their capture
// identity is retained, but cannot stand for a named enclosing method.
func IsCallMethodDescriptor(descriptor string) bool {
	if !strings.HasPrefix(descriptor, "M:") {
		return false
	}
	name, _, _ := strings.Cut(descriptor[2:], "(")
	dot := strings.LastIndexByte(name, '.')
	return dot > 0 && dot < len(name)-1
}

// CallEdge preserves a resolved invocation, including its original compiler
// target. Depth is the BFS step at which the witness was first encountered.
type CallEdge struct {
	Caller  semantic.Symbol
	Target  semantic.Symbol
	Witness Result
	Depth   int
}
type CallTraceResult struct {
	Supported   bool
	Root        *semantic.Symbol
	Edges       []CallEdge
	RowsVisited int
	Truncated   bool
}

func (x *Index) callerKey(fact uint64) [3]uint64 {
	return [3]uint64{x.field(facts, fact, 10), x.field(facts, fact, 3), fact}
}

func (x *Index) validateCallerPostings() error {
	if x.version < 6 {
		return nil
	}
	bad := func() error { return fmt.Errorf("semanticindex: corrupt caller postings") }
	// The inbound directory has already been validated as a complete, unique
	// enumeration of resolved, owned invocations. Verify the same set outbound.
	if x.sections[callerPostings].n != x.sections[invocationPostings].n {
		return bad()
	}
	for i := uint64(0); i < x.sections[callerPostings].n; i++ {
		f := x.field(callerPostings, i, 0)
		if f >= x.sections[facts].n {
			return bad()
		}
		k := x.invocationKey(f)
		j := uint64(sort.Search(int(x.sections[invocationPostings].n), func(j int) bool {
			return !less3(x.invocationKey(x.field(invocationPostings, uint64(j), 0)), k)
		}))
		if j == x.sections[invocationPostings].n || x.field(invocationPostings, j, 0) != f {
			return bad()
		}
		if i > 0 && !less3(x.callerKey(x.field(callerPostings, i-1, 0)), x.callerKey(f)) {
			return bad()
		}
	}
	return nil
}

// TraceCalls traverses recorded method invocations only. Explicit contexts form
// a union of historical observations, not a compatible build or runtime path.
// Interface/virtual targets are not replaced by implementation candidates.
func (x *Index) TraceCalls(ctx context.Context, symbolID string, contextIDs []string, direction string, depth, limit int) (CallTraceResult, error) {
	out := CallTraceResult{}
	if err := x.check(ctx, limit); err != nil {
		return out, err
	}
	if !strings.HasPrefix(symbolID, "symbol:") || !hexBytes([]byte(strings.TrimPrefix(symbolID, "symbol:")), 64) {
		return out, fmt.Errorf("semanticindex: exact symbol ID required")
	}
	if depth < 1 || depth > MaxCallDepth || (direction != "inbound" && direction != "outbound" && direction != "both") {
		return out, fmt.Errorf("semanticindex: direction inbound/outbound/both and depth 1..8 required")
	}
	if len(contextIDs) == 0 {
		return out, fmt.Errorf("%w: explicit contexts required", ErrContractContextSelection)
	}
	selected, err := x.contractContexts(contextIDs)
	if err != nil {
		return out, err
	}
	if x.version < 6 {
		return out, nil
	}
	out.Supported = true
	root, ok := x.domainSymbolRow(symbolID)
	if !ok {
		return out, nil
	}
	method := func(row uint64) bool { return IsCallMethodDescriptor(x.text(x.field(symbols, row, 4))) }
	if !method(root) {
		return out, fmt.Errorf("semanticindex: method symbol required")
	}
	cost := 2 * x.rowCost(symbols, root, 0, 1, 2, 3, 4, 5)
	if cost > MaxQueryBytes {
		return out, fmt.Errorf("semanticindex: query result exceeds byte budget")
	}
	symbol := x.symbol(root)
	out.Root = &symbol
	type step struct {
		symbol uint64
		depth  int
	}
	queue := []step{{root, 0}}
	seenSymbols := map[uint64]bool{root: true}
	seenFacts := map[uint64]bool{}
	for head := 0; head < len(queue); head++ {
		if err := ctx.Err(); err != nil {
			return CallTraceResult{}, err
		}
		s := queue[head]
		if s.depth == depth {
			continue
		}
		dirs := []int{}
		if direction != "outbound" {
			dirs = append(dirs, invocationPostings)
		}
		if direction != "inbound" {
			dirs = append(dirs, callerPostings)
		}
		for _, dir := range dirs {
			key := x.invocationKey
			if dir == callerPostings {
				key = x.callerKey
			}
			for _, c := range selected {
				start := sort.Search(int(x.sections[dir].n), func(j int) bool {
					k := key(x.field(dir, uint64(j), 0))
					return k[0] > s.symbol+1 || k[0] == s.symbol+1 && k[1] >= c
				})
				for row := uint64(start); row < x.sections[dir].n; row++ {
					if err := ctx.Err(); err != nil {
						return CallTraceResult{}, err
					}
					f := x.field(dir, row, 0)
					k := key(f)
					if k[0] != s.symbol+1 || k[1] != c {
						break
					}
					if out.RowsVisited == ContractWorkLimit {
						out.Truncated = true
						return out, nil
					}
					out.RowsVisited++
					if seenFacts[f] {
						continue
					}
					caller, target := x.field(facts, f, 10)-1, x.field(facts, f, 9)-1
					// Initializers, property bodies, local/lambda fallback owners and
					// constructor references are not promoted to method call edges.
					if !method(caller) || !method(target) {
						continue
					}
					n := x.resultCost(f) + 2*x.rowCost(symbols, caller, 0, 1, 2, 3, 4, 5) + 2*x.rowCost(symbols, target, 0, 1, 2, 3, 4, 5) + 256
					if len(out.Edges) == limit || n > MaxQueryBytes || cost > MaxQueryBytes-n {
						out.Truncated = true
						return out, nil
					}
					cost += n
					seenFacts[f] = true
					out.Edges = append(out.Edges, CallEdge{Caller: x.symbol(caller), Target: x.symbol(target), Witness: x.result(f), Depth: s.depth + 1})
					next := caller
					if dir == callerPostings {
						next = target
					}
					if !seenSymbols[next] {
						seenSymbols[next] = true
						queue = append(queue, step{next, s.depth + 1})
					}
				}
			}
		}
	}
	return out, nil
}
