package query

import (
	"regexp/syntax"
	"sort"
	"unicode"

	"moedex/internal/trigram"
)

// This file implements Russ Cox's "Regular Expression Matching with a Trigram
// Index" reduction (regexp4.html): a bottom-up analysis that computes, per
// regex node, a small amount of structural information —
//
//	emptyable: can this node match the empty string?
//	exact:     the (finite, capped) set of ALL strings this node matches, or
//	           "unknown" when that set is infinite or too large to enumerate.
//	prefix:    a capped set covering every string that a match can BEGIN with.
//	suffix:    a capped set covering every string that a match can END with.
//	match:     a trigram Query that is a necessary condition for a match.
//
// The win over the slice-1 "required literal substring" reduction is twofold:
//
//  1. Alternations of short non-literal pieces (e.g. [ab]cd) get expanded into
//     their concrete string set ({acd, bcd}) and turned into an OR of trigram
//     ANDs, instead of collapsing to All.
//  2. Concatenation synthesizes BOUNDARY trigrams that straddle the join. For
//     "foo" concat [ab]"r", the suffix of the left ({foo}) crosses the prefix of
//     the right ({ar, br}) to require trigrams oar/bar etc. that neither side
//     alone could see.
//
// SAFETY (the non-negotiable invariant). The returned match query must be a
// *necessary* condition for a real match: it may admit blobs that don't match
// (verification removes those) but must never reject a blob that does. Every
// rule below preserves this:
//
//   - exact/prefix/suffix are always OVER-approximations: the true set of
//     matching strings (resp. their prefixes/suffixes) is a SUBSET of what we
//     store. We only ever drop information by widening (a capped set that
//     overflows becomes "unknown" or contributes via folding its trigrams into
//     match before being widened), never by narrowing.
//   - trigrams(S) for a string set S yields a query satisfied by every string
//     that has one of S's members as a (sub)string; when ANY member is too
//     short to yield a trigram, trigrams(S) degrades to All. So a real match,
//     whose relevant substring is some member of (a superset of) S, always
//     satisfies trigrams(S).
//   - The And/Or combinators already simplify All/None soundly.
//
// When in doubt at any node we fall back to emptyable=false?-irrelevant, exact
// unknown, prefix/suffix {""}, match All — the maximally permissive (always
// safe) result.

// reCap bounds the size of exact/prefix/suffix sets. Cox uses small constants;
// the exact value only trades selectivity vs. work, never correctness. Above
// this, a set's information is folded into the match query and the set is
// widened ("unknown" for exact, truncated toward {""} for prefix/suffix).
const reCap = 8

// stringSet is a small set of strings with insertion that respects the cap by
// signaling overflow. The empty (nil) set means "no strings"; an "unknown"
// exact set is represented separately by reInfo.exactKnown=false.
type stringSet map[string]struct{}

func newSet(ss ...string) stringSet {
	s := make(stringSet, len(ss))
	for _, x := range ss {
		s[x] = struct{}{}
	}
	return s
}

func (s stringSet) slice() []string {
	out := make([]string, 0, len(s))
	for x := range s {
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}

func (s stringSet) clone() stringSet {
	c := make(stringSet, len(s))
	for x := range s {
		c[x] = struct{}{}
	}
	return c
}

func unionSet(a, b stringSet) stringSet {
	out := make(stringSet, len(a)+len(b))
	for x := range a {
		out[x] = struct{}{}
	}
	for x := range b {
		out[x] = struct{}{}
	}
	return out
}

// cross returns the cartesian concatenation a×b. The result can be as large as
// |a|*|b|; callers cap it afterward.
func cross(a, b stringSet) stringSet {
	out := make(stringSet, len(a)*len(b))
	for x := range a {
		for y := range b {
			out[x+y] = struct{}{}
		}
	}
	return out
}

// reInfo is the analysis result for one regex node.
type reInfo struct {
	emptyable  bool
	exactKnown bool      // false == "unknown" (infinite/too-large set)
	exact      stringSet // valid only when exactKnown
	prefix     stringSet
	suffix     stringSet
	match      Query
}

// anyInfo is the maximally permissive, always-safe result.
func anyInfo() reInfo {
	return reInfo{
		emptyable:  true,
		exactKnown: false,
		prefix:     newSet(""),
		suffix:     newSet(""),
		match:      All,
	}
}

// buildCox is the Cox-style reduction entry point used by FromRegexp.
func buildCox(re *syntax.Regexp) Query {
	info := analyze(re)
	return simplifyMatch(info)
}

// simplifyMatch folds the node's residual exact set into the match query, since
// once we stop analyzing we must require its trigrams too.
func simplifyMatch(info reInfo) Query {
	q := info.match
	if info.exactKnown {
		q = And(q, trigramsOfSet(info.exact))
	}
	return q
}

func analyze(re *syntax.Regexp) reInfo {
	switch re.Op {
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine,
		syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary,
		syntax.OpNoWordBoundary:
		// Zero-width: matches the empty string, contributes nothing.
		return reInfo{
			emptyable:  true,
			exactKnown: true,
			exact:      newSet(""),
			prefix:     newSet(""),
			suffix:     newSet(""),
			match:      All,
		}

	case syntax.OpLiteral:
		return literalInfo(re)

	case syntax.OpCharClass:
		return charClassInfo(re)

	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		// Matches any single character — an exact set too large to enumerate.
		return reInfo{
			emptyable:  false,
			exactKnown: false,
			prefix:     newSet(""),
			suffix:     newSet(""),
			match:      All,
		}

	case syntax.OpCapture:
		return analyze(re.Sub[0])

	case syntax.OpConcat:
		info := analyze(re.Sub[0])
		for _, s := range re.Sub[1:] {
			info = concat(info, analyze(s))
		}
		return info

	case syntax.OpAlternate:
		info := analyze(re.Sub[0])
		for _, s := range re.Sub[1:] {
			info = alternate(info, analyze(s))
		}
		return info

	case syntax.OpStar:
		return star(analyze(re.Sub[0]))

	case syntax.OpQuest:
		return quest(analyze(re.Sub[0]))

	case syntax.OpPlus:
		return plus(analyze(re.Sub[0]))

	case syntax.OpRepeat:
		return repeat(re)

	default:
		return anyInfo()
	}
}

// literalInfo handles a literal rune run. A case-folded literal is handled by
// foldedLiteralInfo (which builds a case-aware trigram constraint); a plain
// literal contributes its exact bytes.
func literalInfo(re *syntax.Regexp) reInfo {
	if re.Flags&syntax.FoldCase != 0 {
		return foldedLiteralInfo(re.Rune)
	}
	s := string(re.Rune)
	if s == "" {
		return reInfo{
			emptyable:  true,
			exactKnown: true,
			exact:      newSet(""),
			prefix:     newSet(""),
			suffix:     newSet(""),
			match:      All,
		}
	}
	return reInfo{
		emptyable:  false,
		exactKnown: true,
		exact:      newSet(s),
		prefix:     newSet(s),
		suffix:     newSet(s),
		match:      All,
	}
}

// foldedLiteralInfo handles a case-insensitive literal run (`(?i)foo`). Go's `(?i)`
// matches any run whose runes are case-fold-equivalent (unicode.SimpleFold) to the
// pattern's, so the necessary trigram condition is:
//
//	AND over byte-trigram positions of ( OR over the fold variants of that
//	position's three bytes )
//
// A real match places, at each position, one fold variant of that byte, so it
// satisfies the OR at that position and hence the AND — a sound necessary
// condition (it only ever OVER-approximates by also admitting other variants).
//
// Two restrictions keep byte positions aligned and the condition sound:
//
//   - Non-ASCII pattern runes ⇒ bail to All. A non-ASCII rune occupies multiple
//     bytes and its fold orbit can change byte length (e.g. ß↔ẞ), so byte-trigram
//     positions would not align across variants. These are rare in code and not
//     the latency problem; All is always safe.
//   - A trigram position is SKIPPED (contributes no constraint) when any of its
//     three runes has a fold variant outside ASCII — notably k↔K↔U+212A (Kelvin)
//     and s↔S↔U+017F (long s). Such a match could carry a non-ASCII byte we cannot
//     represent as one aligned byte, so we must not require an ASCII-only trigram
//     there. Skipping a position only weakens the AND (keeps it a superset), never
//     drops a true match. (A literal whose every position is "dirty", e.g. "sks",
//     reduces to All — still sound.)
//
// The full case-variant string set is exponential, so we never enumerate it: the
// constraint lives entirely in match, and exact is left unknown.
func foldedLiteralInfo(runes []rune) reInfo {
	for _, r := range runes {
		if r >= 0x80 {
			return anyInfo() // multi-byte fold orbits break byte alignment
		}
	}
	if len(runes) < trigram.N {
		return anyInfo() // no byte-trigram to require
	}

	variants := make([][]byte, len(runes))
	clean := make([]bool, len(runes))
	for i, r := range runes {
		variants[i], clean[i] = asciiFoldVariants(r)
	}

	var positions []Query
	for i := 0; i+trigram.N <= len(runes); i++ {
		if !clean[i] || !clean[i+1] || !clean[i+2] {
			continue // a non-ASCII fold variant could appear here; require nothing
		}
		var tris []Query
		for _, a := range variants[i] {
			for _, b := range variants[i+1] {
				for _, c := range variants[i+2] {
					tris = append(tris, triQ{trigram.Trigram{a, b, c}})
				}
			}
		}
		positions = append(positions, Or(tris...))
	}
	return reInfo{
		emptyable:  false,
		exactKnown: false, // case-variant set is exponential; match carries the constraint
		prefix:     newSet(""),
		suffix:     newSet(""),
		match:      And(positions...), // no clean position -> All (safe)
	}
}

// asciiFoldVariants returns the distinct ASCII bytes a single ASCII rune can take
// under Go's `(?i)` folding, plus whether its ENTIRE fold orbit stays ASCII. The
// orbit is the unicode.SimpleFold cycle; ASCII letters whose orbit leaves ASCII
// (k→U+212A, s→U+017F) report allASCII=false so callers skip positions that
// include them rather than under-approximate.
func asciiFoldVariants(r rune) (bytes []byte, allASCII bool) {
	allASCII = true
	for c := r; ; {
		if c < 0x80 {
			bytes = append(bytes, byte(c))
		} else {
			allASCII = false
		}
		c = unicode.SimpleFold(c)
		if c == r {
			break // SimpleFold cycles back to the start
		}
	}
	return bytes, allASCII
}

// charClassInfo expands a character class into the set of single-character
// strings it matches, capped. re.Rune holds inclusive [lo,hi] rune pairs.
func charClassInfo(re *syntax.Regexp) reInfo {
	// Count members up to cap+1 to detect overflow without materializing huge
	// classes (e.g. [^x] or \w).
	var members []string
	overflow := false
	for i := 0; i+1 < len(re.Rune); i += 2 {
		lo, hi := re.Rune[i], re.Rune[i+1]
		for r := lo; r <= hi; r++ {
			if len(members) >= reCap {
				overflow = true
				break
			}
			members = append(members, string(r))
		}
		if overflow {
			break
		}
	}
	if overflow || len(members) == 0 {
		// Too many alternatives (or an empty class): no usable exact set. A
		// single-char class has no trigram, so prefix/suffix can't help either.
		return reInfo{
			emptyable:  false,
			exactKnown: false,
			prefix:     newSet(""),
			suffix:     newSet(""),
			match:      All,
		}
	}
	set := newSet(members...)
	return reInfo{
		emptyable:  false,
		exactKnown: true,
		exact:      set,
		prefix:     set.clone(),
		suffix:     set.clone(),
		match:      All,
	}
}

// concat combines two adjacent nodes, synthesizing boundary trigrams.
func concat(x, y reInfo) reInfo {
	var out reInfo
	out.emptyable = x.emptyable && y.emptyable

	// match: both sides required, PLUS boundary trigrams across the join.
	out.match = And(x.match, y.match, trigramsOfSet(cross(x.suffix, y.prefix)))

	// exact: cross product when both known, else unknown.
	if x.exactKnown && y.exactKnown {
		ex := cross(x.exact, y.exact)
		if len(ex) <= reCap {
			out.exactKnown = true
			out.exact = ex
		} else {
			// Overflowed: fold its trigrams into match, then widen to unknown.
			out.match = And(out.match, trigramsOfSet(ex))
			out.exactKnown = false
		}
	} else {
		out.exactKnown = false
	}

	// prefix.
	switch {
	case x.exactKnown:
		out.prefix = capPrefix(cross(x.exact, y.prefix), &out.match)
	case x.emptyable:
		out.prefix = capPrefix(unionSet(x.prefix, y.prefix), &out.match)
	default:
		out.prefix = capPrefix(x.prefix.clone(), &out.match)
	}

	// suffix (symmetric).
	switch {
	case y.exactKnown:
		out.suffix = capSuffix(cross(x.suffix, y.exact), &out.match)
	case y.emptyable:
		out.suffix = capSuffix(unionSet(y.suffix, x.suffix), &out.match)
	default:
		out.suffix = capSuffix(y.suffix.clone(), &out.match)
	}

	if out.exactKnown {
		// Keep prefix/suffix consistent with the (small) exact set.
		out.prefix = out.exact.clone()
		out.suffix = out.exact.clone()
	}
	return out
}

func alternate(x, y reInfo) reInfo {
	var out reInfo
	out.emptyable = x.emptyable || y.emptyable
	out.match = Or(x.match, y.match)

	if x.exactKnown && y.exactKnown {
		ex := unionSet(x.exact, y.exact)
		if len(ex) <= reCap {
			out.exactKnown = true
			out.exact = ex
		} else {
			out.match = And(out.match, trigramsOfSet(ex))
			out.exactKnown = false
		}
	} else {
		// At least one branch's exact set is unknown, so the union is not an
		// exact set. For an alternation the necessary condition is
		//   (necessary condition for x) OR (necessary condition for y),
		// where each branch's necessary condition folds in ITS OWN exact-set
		// trigrams. Those trigrams must NOT be AND'd across the whole
		// alternation: a real match may go entirely through the other branch,
		// which need not contain them. (Doing so was an under-approximation —
		// foo|[0-9]+ wrongly required tri(foo) and dropped digit-only matches.)
		out.match = Or(
			And(x.match, exactTrigrams(x)),
			And(y.match, exactTrigrams(y)),
		)
		out.exactKnown = false
	}

	out.prefix = capPrefix(unionSet(x.prefix, y.prefix), &out.match)
	out.suffix = capSuffix(unionSet(x.suffix, y.suffix), &out.match)
	if out.exactKnown {
		out.prefix = out.exact.clone()
		out.suffix = out.exact.clone()
	}
	return out
}

// star: e* matches the empty string, so nothing is required.
func star(reInfo) reInfo { return anyInfo() }

// quest: e? matches exact(e) ∪ {""}; prefix/suffix collapse to {""} and match
// becomes All because zero occurrences is allowed.
func quest(reInfo) reInfo { return anyInfo() }

// plus: e+ requires at least one e, so e's match/prefix/suffix carry over, but
// exact becomes unknown (repetition is unbounded).
func plus(x reInfo) reInfo {
	return reInfo{
		emptyable:  x.emptyable,
		exactKnown: false,
		prefix:     x.prefix.clone(),
		suffix:     x.suffix.clone(),
		match:      And(x.match, exactTrigrams(x)),
	}
}

// repeat handles {min,max}. If min>=1 at least one occurrence is required (like
// plus over the sub); otherwise zero occurrences allowed -> anyInfo.
func repeat(re *syntax.Regexp) reInfo {
	if re.Min >= 1 {
		return plus(analyze(re.Sub[0]))
	}
	return anyInfo()
}

// capPrefix/capSuffix enforce the size cap on a prefix/suffix set, folding the
// set's trigrams into match before widening so no information is lost. On
// overflow we truncate toward {""} (the always-safe widening: every string has
// "" as a prefix and suffix).
func capPrefix(s stringSet, match *Query) stringSet {
	if len(s) <= reCap {
		return s
	}
	*match = And(*match, trigramsOfSet(s))
	return newSet("")
}

func capSuffix(s stringSet, match *Query) stringSet {
	if len(s) <= reCap {
		return s
	}
	*match = And(*match, trigramsOfSet(s))
	return newSet("")
}

// exactTrigrams folds a node's exact set into a trigram query, but only when
// the exact set is actually known; an unknown exact set yields no constraint.
func exactTrigrams(x reInfo) Query {
	if !x.exactKnown {
		return All
	}
	return trigramsOfSet(x.exact)
}

// trigramsOfSet converts a string set into a trigram query that is a necessary
// condition for containing one of the set's members as a substring:
//
//	OR over members m of  AND over trigrams t of m  tri(t)
//
// If ANY member is shorter than 3 bytes it yields no trigram constraint, so the
// whole OR degrades to All (that member could match with no required trigram).
// An empty set means "no constraint" -> All.
func trigramsOfSet(s stringSet) Query {
	if len(s) == 0 {
		return All
	}
	branches := make([]Query, 0, len(s))
	for _, m := range s.slice() {
		branches = append(branches, trigramsOfString(m))
	}
	return Or(branches...)
}

// trigramsOfString requires every byte-trigram of m to be present. A string
// shorter than 3 bytes has no trigram -> All (no necessary constraint).
func trigramsOfString(m string) Query {
	if len(m) < trigram.N {
		return All
	}
	qs := make([]Query, 0, len(m)-trigram.N+1)
	for i := 0; i+trigram.N <= len(m); i++ {
		qs = append(qs, triQ{trigram.Trigram{m[i], m[i+1], m[i+2]}})
	}
	return And(qs...)
}
