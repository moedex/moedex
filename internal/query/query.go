// Package query turns a regex into a boolean trigram query and evaluates it
// against the index to produce candidate blobs.
//
// Safety property (the thing that guarantees ripgrep parity): a query is only
// ever a *necessary* condition for a match — it may over-approximate (return
// blobs that don't actually match) but it never under-approximates (it never
// excludes a blob that does match). Final verification with the real regex
// engine, done by the search package, removes the false positives. Any path
// here where we're unsure degrades to All (scan everything) rather than risk
// dropping a true match.
//
// FromRegexp uses the full Cox-style reduction (cox.go): per-node
// exact/prefix/suffix string sets plus a match query, with boundary-trigram
// synthesis across concatenation and size-capped sets that fold into trigram
// constraints. This filters patterns the slice-1 "required literal substring"
// reduction could not (e.g. [ab]cd -> acd|bcd). The slice-1 reduction is kept
// as buildRequiredLiterals so the selectivity gain can be measured; both are
// sound necessary conditions, so the richer one only changes speed, not
// correctness.
package query

import (
	"regexp/syntax"
	"strings"

	"moedex/internal/index"
	"moedex/internal/trigram"
)

// Query is a boolean expression over trigrams. Eval returns a sorted, distinct
// list of candidate blob IDs.
type Query interface {
	Eval(ix *index.Index) []uint64
	String() string
}

// All matches every blob (no filtering); None matches nothing.
type allQ struct{}
type noneQ struct{}
type triQ struct{ t trigram.Trigram }
type andQ struct{ subs []Query }
type orQ struct{ subs []Query }

// All is the no-filter query: every blob is a candidate.
var All Query = allQ{}

func (allQ) Eval(ix *index.Index) []uint64 {
	out := make([]uint64, ix.NumBlobs())
	for i := range out {
		out[i] = uint64(i)
	}
	return out
}
func (allQ) String() string { return "ALL" }

func (noneQ) Eval(*index.Index) []uint64 { return nil }
func (noneQ) String() string             { return "NONE" }

// Eval is the central parity gate for the selective index. A triQ on a gram the
// index did NOT materialize (IndexedGram false) has UNKNOWN postings — its empty
// list does not mean "zero occurrences" — so it must evaluate to the full blob
// set (All semantics), forcing the candidate set to widen and the real regexp
// engine (in package search) to verify. Intersecting a deselected gram to empty
// would drop true matches and break ripgrep parity; returning All here can only
// ever over-approximate, which verification then cleans up.
//
// On the default (all-trigram) build IndexedGram is universally true and
// Selective() is false, so this short-circuits to the original behavior exactly,
// at the cost of one cheap bool check.
func (q triQ) Eval(ix *index.Index) []uint64 {
	if ix.Selective() && !ix.IndexedGram(q.t) {
		return allQ{}.Eval(ix) // non-indexed gram: force scan, never intersect to empty
	}
	ps := ix.Postings(q.t)
	var out []uint64
	for i, p := range ps {
		if i == 0 || p.Blob != out[len(out)-1] {
			out = append(out, p.Blob)
		}
	}
	return out
}
func (q triQ) String() string { return "tri(" + q.t.String() + ")" }

func (q andQ) Eval(ix *index.Index) []uint64 {
	acc := q.subs[0].Eval(ix)
	for _, s := range q.subs[1:] {
		if len(acc) == 0 {
			return nil
		}
		acc = intersect(acc, s.Eval(ix))
	}
	return acc
}
func (q andQ) String() string { return "(" + join(q.subs, " AND ") + ")" }

func (q orQ) Eval(ix *index.Index) []uint64 {
	acc := q.subs[0].Eval(ix)
	for _, s := range q.subs[1:] {
		acc = union(acc, s.Eval(ix))
	}
	return acc
}
func (q orQ) String() string { return "(" + join(q.subs, " OR ") + ")" }

func join(qs []Query, sep string) string {
	parts := make([]string, len(qs))
	for i, q := range qs {
		parts[i] = q.String()
	}
	return strings.Join(parts, sep)
}

// And builds a conjunction, simplifying away All and short-circuiting None.
func And(qs ...Query) Query {
	var out []Query
	for _, q := range qs {
		switch v := q.(type) {
		case allQ:
			continue
		case noneQ:
			return noneQ{}
		case andQ:
			out = append(out, v.subs...)
		default:
			out = append(out, q)
		}
	}
	switch len(out) {
	case 0:
		return allQ{} // no constraints
	case 1:
		return out[0]
	default:
		return andQ{out}
	}
}

// Or builds a disjunction. If any branch is unfilterable (All), the whole
// disjunction is unfilterable.
func Or(qs ...Query) Query {
	var out []Query
	for _, q := range qs {
		switch v := q.(type) {
		case allQ:
			return allQ{}
		case noneQ:
			continue
		case orQ:
			out = append(out, v.subs...)
		default:
			out = append(out, q)
		}
	}
	switch len(out) {
	case 0:
		return noneQ{}
	case 1:
		return out[0]
	default:
		return orQ{out}
	}
}

// FromRegexp parses a regex and reduces it to a boolean trigram query using the
// Cox-style analysis (see cox.go). The reduction is a necessary condition for a
// match: it may over-approximate but never under-approximates, so verification
// can recover exact ripgrep parity.
func FromRegexp(pattern string) (Query, error) {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, err
	}
	return buildCox(re.Simplify()), nil
}

// fromRegexpRequiredLiterals is the slice-1 reduction (required literal
// substrings only). It is retained, unexported, so the Cox reduction's
// selectivity gain can be measured against it. Like FromRegexp it is a sound
// necessary condition.
func fromRegexpRequiredLiterals(pattern string) (Query, error) {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, err
	}
	return buildRequiredLiterals(re.Simplify()), nil
}

func buildRequiredLiterals(re *syntax.Regexp) Query {
	switch re.Op {
	case syntax.OpLiteral:
		// A case-folded literal could match many concrete strings; rather than
		// enumerate, stay conservative.
		if re.Flags&syntax.FoldCase != 0 {
			return allQ{}
		}
		return literalQuery(string(re.Rune))
	case syntax.OpConcat:
		qs := make([]Query, len(re.Sub))
		for i, s := range re.Sub {
			qs[i] = buildRequiredLiterals(s)
		}
		return And(qs...)
	case syntax.OpAlternate:
		qs := make([]Query, len(re.Sub))
		for i, s := range re.Sub {
			qs[i] = buildRequiredLiterals(s)
		}
		return Or(qs...)
	case syntax.OpCapture:
		return buildRequiredLiterals(re.Sub[0])
	case syntax.OpPlus:
		// x+ requires at least one x.
		return buildRequiredLiterals(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min >= 1 {
			return buildRequiredLiterals(re.Sub[0])
		}
		return allQ{}
	case syntax.OpStar, syntax.OpQuest:
		// Zero occurrences allowed -> the subexpression is not required.
		return allQ{}
	default:
		// OpCharClass, OpAnyChar(NotNL), anchors, empty-width, etc.: no usable
		// required trigram.
		return allQ{}
	}
}

// literalQuery requires every byte-trigram of a literal run to be present.
// Every string containing the literal contains all of these, so this is a sound
// necessary condition.
func literalQuery(s string) Query {
	if len(s) < trigram.N {
		return allQ{}
	}
	qs := make([]Query, 0, len(s)-trigram.N+1)
	for i := 0; i+trigram.N <= len(s); i++ {
		qs = append(qs, triQ{trigram.Trigram{s[i], s[i+1], s[i+2]}})
	}
	return And(qs...)
}

// intersect returns the sorted intersection of two sorted, distinct slices.
func intersect(a, b []uint64) []uint64 {
	var out []uint64
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, a[i])
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	return out
}

// union returns the sorted union of two sorted, distinct slices.
func union(a, b []uint64) []uint64 {
	out := make([]uint64, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, a[i])
			i++
			j++
		case a[i] < b[j]:
			out = append(out, a[i])
			i++
		default:
			out = append(out, b[j])
			j++
		}
	}
	out = append(out, a[i:]...)
	out = append(out, b[j:]...)
	return out
}
