package semanticindex

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"moedex/internal/semantic"
	"moedex/internal/sourcescope"
)

const MaxQueryBytes = 2 << 20

func (x *Index) check(ctx context.Context, limit int) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if x == nil || len(x.data) == 0 {
		return fmt.Errorf("semanticindex: closed index")
	}
	if limit < 1 || limit > MaxResults {
		return fmt.Errorf("semanticindex: limit must be 1..%d", MaxResults)
	}
	return nil
}
func (x *Index) strID(s string) (uint64, bool) {
	i := sort.Search(int(x.sections[strdir].n), func(i int) bool { return bytes.Compare(x.textBytes(uint64(i)), []byte(s)) >= 0 })
	return uint64(i), i < int(x.sections[strdir].n) && bytes.Equal(x.textBytes(uint64(i)), []byte(s))
}
func (x *Index) source(s, c uint64) SourceContext {
	si := x.field(sources, s, 1)
	return SourceContext{
		Snapshot: semantic.SourceSnapshot{ID: x.text(x.field(snapshots, si, 0)), Repo: x.text(x.field(snapshots, si, 1)), ProjectID: x.text(x.field(snapshots, si, 2)), Commit: x.text(x.field(snapshots, si, 3)), InputFingerprint: x.text(x.field(snapshots, si, 4))},
		Context:  semantic.BuildContext{ID: x.text(x.field(contexts, c, 0)), SnapshotID: x.text(x.field(snapshots, si, 0)), Project: x.text(x.field(contexts, c, 2)), InputFingerprint: x.text(x.field(contexts, c, 3)), Extractor: x.text(x.field(contexts, c, 4)), ExtractorVersion: x.text(x.field(contexts, c, 5)), Status: x.text(x.field(contexts, c, 6))},
		Source:   semantic.Source{ID: x.text(x.field(sources, s, 0)), SnapshotID: x.text(x.field(snapshots, si, 0)), Path: x.text(x.field(sources, s, 2)), RawSHA256: x.text(x.field(sources, s, 3)), ByteSize: x.field(sources, s, 4), Generated: x.field(sources, s, 5) == 1},
	}
}
func (x *Index) symbol(i uint64) semantic.Symbol {
	return semantic.Symbol{ID: x.text(x.field(symbols, i, 0)), Key: semantic.SymbolKey{Language: x.text(x.field(symbols, i, 1)), NamespaceKind: x.text(x.field(symbols, i, 2)), Namespace: x.text(x.field(symbols, i, 3)), Descriptor: x.text(x.field(symbols, i, 4)), DescriptorKind: x.text(x.field(symbols, i, 5))}}
}
func (x *Index) rowCost(t int, row uint64, cols ...uint64) uint64 {
	cost := uint64(512)
	for _, col := range cols {
		cost += uint64(len(x.textBytes(x.field(t, row, col))))
	}
	return cost
}
func (x *Index) sourceCost(s, c uint64) uint64 {
	snap := x.field(sources, s, 1)
	return x.rowCost(sources, s, 0, 2, 3) + x.rowCost(contexts, c, 0, 2, 3, 4, 5, 6) + 3*x.rowCost(snapshots, snap, 0, 1, 2, 3, 4)
}
func (x *Index) resultCost(i uint64) uint64 {
	cost := x.sourceCost(x.field(facts, i, 2), x.field(facts, i, 3)) + 2*x.rowCost(facts, i, 0, 1, 6, 7, 8, 13, 14, 15)
	for _, col := range []uint64{9, 10} {
		s := x.field(facts, i, col)
		if s > 0 {
			cost += 2 * x.rowCost(symbols, s-1, 0, 1, 2, 3, 4, 5)
		}
	}
	start, n := x.field(facts, i, 11), x.field(facts, i, 12)
	if n > MaxQueryBytes/512 {
		return MaxQueryBytes + 1
	}
	for j := uint64(0); j < n; j++ {
		cost += 2 * x.rowCost(symbols, x.field(candidates, start+j, 0), 0, 1, 2, 3, 4, 5)
		if cost > MaxQueryBytes {
			return cost
		}
	}
	return cost + x.domainResultCost(i) + x.implementationResultCost(i)
}
func (x *Index) result(i uint64) Result {
	sc := x.source(x.field(facts, i, 2), x.field(facts, i, 3))
	o := semantic.Occurrence{ID: x.text(x.field(facts, i, 0)), SourceID: sc.Source.ID, ContextID: sc.Context.ID, Role: x.text(x.field(facts, i, 6)), Kind: x.text(x.field(facts, i, 7)), Offset: x.field(facts, i, 4), Length: x.field(facts, i, 5)}
	b := semantic.Binding{ID: x.text(x.field(facts, i, 1)), OccurrenceID: o.ID, Status: x.text(x.field(facts, i, 8)), Method: x.text(x.field(facts, i, 13)), Extractor: x.text(x.field(facts, i, 14)), ExtractorVersion: x.text(x.field(facts, i, 15))}
	r := Result{Snapshot: sc.Snapshot, Context: sc.Context, Source: sc.Source, Occurrence: o, Binding: b}
	if s := x.field(facts, i, 9); s > 0 {
		v := x.symbol(s - 1)
		r.Symbol = &v
		r.Binding.SymbolID = v.ID
	}
	if s := x.field(facts, i, 10); s > 0 {
		r.Binding.EnclosingSymbolID = x.text(x.field(symbols, s-1, 0))
	}
	start, n := x.field(facts, i, 11), x.field(facts, i, 12)
	for j := uint64(0); j < n; j++ {
		v := x.symbol(x.field(candidates, start+j, 0))
		r.Candidates = append(r.Candidates, v)
		r.Binding.CandidateSymbolIDs = append(r.Binding.CandidateSymbolIDs, v.ID)
	}
	r.Binding.DomainFacts, r.DomainSymbols = x.resultDomainFacts(i)
	r.Binding.ImplementationFacts, r.ImplementationSymbols = x.resultImplementationFacts(i)
	return r
}
func validPosition(p Position) error {
	if p.Repo == "" || p.Path == "" || strings.HasSuffix(p.Path, "/") {
		return fmt.Errorf("semanticindex: repo and exact path required")
	}
	return (sourcescope.Scope{Repo: p.Repo, PathPrefix: p.Path}).Validate()
}
func (x *Index) positionKey(i uint64) [3]uint64 {
	s := x.field(facts, i, 2)
	return [3]uint64{x.field(snapshots, x.field(sources, s, 1), 1), x.field(sources, s, 2), x.field(facts, i, 4)}
}
func less3(a, b [3]uint64) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
func (x *Index) Bindings(ctx context.Context, p Position, limit int) (QueryResult, error) {
	var out QueryResult
	if e := x.check(ctx, limit); e != nil {
		return out, e
	}
	if e := validPosition(p); e != nil {
		return out, e
	}
	repo, ok := x.strID(p.Repo)
	if !ok {
		return out, nil
	}
	path, ok := x.strID(p.Path)
	if !ok {
		return out, nil
	}
	want := [3]uint64{repo, path, p.Offset}
	start := sort.Search(int(x.sections[positions].n), func(j int) bool { return !less3(x.positionKey(x.field(positions, uint64(j), 0)), want) })
	var cost uint64
	for j := uint64(start); j < x.sections[positions].n; j++ {
		if e := ctx.Err(); e != nil {
			return QueryResult{}, e
		}
		i := x.field(positions, j, 0)
		if x.positionKey(i) != want {
			break
		}
		if p.BuildContextID != "" && !bytes.Equal(x.textBytes(x.field(contexts, x.field(facts, i, 3), 0)), []byte(p.BuildContextID)) {
			continue
		}
		if len(out.Results) == limit {
			out.Truncated = true
			break
		}
		cost += x.resultCost(i)
		if cost > MaxQueryBytes {
			return QueryResult{}, fmt.Errorf("semanticindex: query result exceeds byte budget")
		}
		out.Results = append(out.Results, x.result(i))
	}
	return out, nil
}
func (x *Index) Definitions(ctx context.Context, symbolID string, f Filter, limit int) (QueryResult, error) {
	var out QueryResult
	if e := x.check(ctx, limit); e != nil {
		return out, e
	}
	if e := (sourcescope.Scope{Repo: f.Repo, PathPrefix: f.PathPrefix}).Validate(); e != nil {
		return out, e
	}
	if symbolID == "" {
		return out, fmt.Errorf("semanticindex: symbol ID required")
	}
	sid, ok := x.strID(symbolID)
	if !ok {
		return out, nil
	}
	row := sort.Search(int(x.sections[symbols].n), func(i int) bool { return x.field(symbols, uint64(i), 0) >= sid })
	if row == int(x.sections[symbols].n) || x.field(symbols, uint64(row), 0) != sid {
		return out, nil
	}
	want := uint64(row) + 1
	start := sort.Search(int(x.sections[definitions].n), func(j int) bool { return x.field(facts, x.field(definitions, uint64(j), 0), 9) >= want })
	var cost uint64
	for j := uint64(start); j < x.sections[definitions].n; j++ {
		if e := ctx.Err(); e != nil {
			return QueryResult{}, e
		}
		i := x.field(definitions, j, 0)
		if x.field(facts, i, 9) != want {
			break
		}
		s, c := x.field(facts, i, 2), x.field(facts, i, 3)
		if !x.matches(s, c, f) {
			continue
		}
		if len(out.Results) == limit {
			out.Truncated = true
			break
		}
		cost += x.resultCost(i)
		if cost > MaxQueryBytes {
			return QueryResult{}, fmt.Errorf("semanticindex: query result exceeds byte budget")
		}
		out.Results = append(out.Results, x.result(i))
	}
	return out, nil
}
func (x *Index) matches(s, c uint64, f Filter) bool {
	if f.Repo != "" && !bytes.Equal(x.textBytes(x.field(snapshots, x.field(sources, s, 1), 1)), []byte(f.Repo)) {
		return false
	}
	if f.BuildContextID != "" && !bytes.Equal(x.textBytes(x.field(contexts, c, 0)), []byte(f.BuildContextID)) {
		return false
	}
	prefix := strings.TrimSuffix(f.PathPrefix, "/")
	p := x.textBytes(x.field(sources, s, 2))
	return prefix == "" || bytes.Equal(p, []byte(prefix)) || (len(p) > len(prefix) && bytes.HasPrefix(p, []byte(prefix)) && p[len(prefix)] == '/')
}
func (x *Index) SourceContexts(ctx context.Context, repo, path string, limit int) ([]SourceContext, bool, error) {
	return x.sourceContexts(ctx, repo, path, "", limit)
}
func (x *Index) SourceContext(ctx context.Context, repo, path, contextID string) (SourceContext, bool, error) {
	if contextID == "" {
		return SourceContext{}, false, fmt.Errorf("semanticindex: context ID required")
	}
	rows, _, e := x.sourceContexts(ctx, repo, path, contextID, 1)
	if e != nil || len(rows) == 0 {
		return SourceContext{}, false, e
	}
	return rows[0], true, nil
}
func (x *Index) sourceContexts(ctx context.Context, repo, path, contextID string, limit int) ([]SourceContext, bool, error) {
	if e := x.check(ctx, limit); e != nil {
		return nil, false, e
	}
	if e := validPosition(Position{Repo: repo, Path: path}); e != nil {
		return nil, false, e
	}
	r, ok := x.strID(repo)
	if !ok {
		return nil, false, nil
	}
	p, ok := x.strID(path)
	if !ok {
		return nil, false, nil
	}
	want := [4]uint64{r, p, 0, 0}
	start := sort.Search(int(x.sections[memberships].n), func(i int) bool { return !lessKey(x.memberKey(uint64(i)), want) })
	var out []SourceContext
	var cost uint64
	for i := uint64(start); i < x.sections[memberships].n; i++ {
		if e := ctx.Err(); e != nil {
			return nil, false, e
		}
		k := x.memberKey(i)
		if k[0] != r || k[1] != p {
			break
		}
		s, c := x.field(memberships, i, 0), x.field(memberships, i, 1)
		if contextID != "" && !bytes.Equal(x.textBytes(x.field(contexts, c, 0)), []byte(contextID)) {
			continue
		}
		if len(out) == limit {
			return out, true, nil
		}
		cost += x.sourceCost(s, c)
		if cost > MaxQueryBytes {
			return nil, false, fmt.Errorf("semanticindex: query result exceeds byte budget")
		}
		out = append(out, x.source(s, c))
	}
	return out, false, nil
}
