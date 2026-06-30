package query

import "regexp/syntax"

// fromRegexpRequiredLiterals is the slice-1 reduction (required literal
// substrings only). It is retained, test-only, as a measurement baseline so
// the full Cox reduction's (FromRegexp's) selectivity gain can be quantified
// against it — see cox_test.go and measure_test.go. Like FromRegexp it is a
// sound necessary condition, just a less selective one (e.g. it can't reduce
// [ab]cd to acd|bcd the way Cox can).
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
