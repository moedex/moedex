package symbol

import "regexp"

// CFExtractor extracts ColdFusion (CFML) definitions with a dependency-free
// regex/byte scan (no cfparser, no cgo, no third-party). CFML is a tag/script
// hybrid; this covers the shapes that dominate the TurnCommerce hugedomains app:
//
//   - <cffunction name="x" ...> ... </cffunction>   -> Func   (tag UDF)
//   - <cfcomponent name=|displayname="x"> ...        -> Type
//   - function x(...) { ... } INSIDE a <cfscript>     -> Func   (script UDF)
//
// Precision over completeness, like the C#/TS extractors, and it never errors.
//
// The one CFML-specific hazard: .cfm templates freely embed JavaScript, whose
// `function f(){}` is syntactically identical to a cfscript UDF. So script
// functions are taken ONLY from inside <cfscript>...</cfscript> regions; the
// unambiguous tag <cffunction> is matched anywhere. This keeps page JS out of the
// symbol set at the cost of missing UDFs in script-only .cfc files (rare in this
// corpus, which is overwhelmingly tag-based .cfm).
//
// Name offsets come from a captured submatch, so NameStart/NameEnd slice the
// exact identifier (the attribute value for tags, the declared name for script).
type CFExtractor struct{}

var (
	// <cffunction ... name="foo" ...>. The name attribute may sit anywhere in the
	// tag and use single or double quotes; the tag/attr names are case-insensitive
	// (CFML is). \bname guards against matching the "name" inside "displayname".
	cfTagFuncRe = regexp.MustCompile(`(?i)<cffunction\b[^>]*?\bname\s*=\s*["']([A-Za-z_][A-Za-z0-9_]*)["']`)

	// <cfcomponent ... name=|displayname="Foo" ...> -> Type. Most components carry
	// no name attribute (the name is the filename, which the extractor cannot see),
	// so only the attributed form is emitted.
	cfTagComponentRe = regexp.MustCompile(`(?i)<cfcomponent\b[^>]*?\b(?:displayname|name)\s*=\s*["']([A-Za-z_][A-Za-z0-9_.]*)["']`)

	// A cfscript UDF declaration: optional access modifier, optional return type,
	// then `function name(`. Anchored at line start (after indentation).
	cfScriptFuncRe = regexp.MustCompile(`(?im)^[ \t]*(?:(?:public|private|remote|package)\s+)?(?:[A-Za-z_][A-Za-z0-9_.$\[\]]*\s+)?function\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

	cfFuncCloseRe   = regexp.MustCompile(`(?i)</cffunction>`)
	cfCompCloseRe   = regexp.MustCompile(`(?i)</cfcomponent>`)
	cfScriptOpenRe  = regexp.MustCompile(`(?i)<cfscript\b[^>]*>`)
	cfScriptCloseRe = regexp.MustCompile(`(?i)</cfscript>`)
)

// Extract scans CFML content for tag/script function and component definitions.
// Always returns a nil error.
func (CFExtractor) Extract(content []byte) ([]Symbol, error) {
	var syms []Symbol

	// Tag UDFs: <cffunction name="x"> ... </cffunction>.
	funcCloses := newCfCloseScanner(content, cfFuncCloseRe)
	for _, m := range cfTagFuncRe.FindAllSubmatchIndex(content, -1) {
		ns, ne := m[2], m[3]
		if ns < 0 || ne < 0 {
			continue
		}
		syms = append(syms, Symbol{
			Name:      string(content[ns:ne]),
			Kind:      Func,
			NameStart: ns,
			NameEnd:   ne,
			BodyStart: m[0],
			BodyEnd:   funcCloses.bodyEnd(content, m[1]),
		})
	}

	// Components: <cfcomponent name|displayname="x"> ... </cfcomponent>.
	compCloses := newCfCloseScanner(content, cfCompCloseRe)
	for _, m := range cfTagComponentRe.FindAllSubmatchIndex(content, -1) {
		ns, ne := m[2], m[3]
		if ns < 0 || ne < 0 {
			continue
		}
		syms = append(syms, Symbol{
			Name:      string(content[ns:ne]),
			Kind:      Type,
			NameStart: ns,
			NameEnd:   ne,
			BodyStart: m[0],
			BodyEnd:   compCloses.bodyEnd(content, m[1]),
		})
	}

	// Script UDFs: function x(...) { ... } — only inside <cfscript> blocks, so the
	// JavaScript that .cfm pages embed is not mistaken for CFML.
	for _, r := range cfScriptRegions(content) {
		seg := content[r[0]:r[1]]
		for _, m := range cfScriptFuncRe.FindAllSubmatchIndex(seg, -1) {
			ns, ne := r[0]+m[2], r[0]+m[3]
			be := bodyRange(content, r[0]+m[1])
			if be <= ns {
				continue
			}
			syms = append(syms, Symbol{
				Name:      string(content[ns:ne]),
				Kind:      Func,
				NameStart: ns,
				NameEnd:   ne,
				BodyStart: r[0] + m[0],
				BodyEnd:   be,
			})
		}
	}

	return syms, nil
}

// cfCloseScanner pairs each open tag's position with the next matching close
// tag via a single forward pass over a precomputed, ascending list of close
// offsets, rather than every open independently rescanning to EOF.
//
// Correctness relies on callers feeding `from` values in non-decreasing
// order (true here: both FindAllSubmatchIndex callers return matches in
// left-to-right order). Since `from` only increases, the close index only
// ever advances forward across calls, giving O(opens + closes) total instead
// of O(opens * content length).
type cfCloseScanner struct {
	closes [][]int
	idx    int
}

func newCfCloseScanner(content []byte, closeRe *regexp.Regexp) *cfCloseScanner {
	return &cfCloseScanner{closes: closeRe.FindAllIndex(content, -1)}
}

// bodyEnd returns the byte offset just past the next case-insensitive close
// tag at/after from, or the end of from's line when the block is unterminated
// (so BodyStart < BodyEnd always holds).
func (s *cfCloseScanner) bodyEnd(content []byte, from int) int {
	if from < 0 {
		from = 0
	}
	for s.idx < len(s.closes) && s.closes[s.idx][0] < from {
		s.idx++
	}
	if s.idx < len(s.closes) {
		return s.closes[s.idx][1]
	}
	return lineEnd(content, from)
}

// cfScriptRegions returns [start,end) byte ranges of each <cfscript> block's
// interior (after the open tag, up to the matching close or end of content).
//
// This is a single forward-advancing pass, not independent searches per open:
// each open is found starting from where the previous region left off, so an
// unclosed (or merely numerous) <cfscript> can consume the rest of the
// content only once instead of being rescanned from every subsequent open.
// That keeps total work O(n) instead of O(n^2) on pathological input.
func cfScriptRegions(content []byte) [][2]int {
	var out [][2]int
	pos := 0
	for pos < len(content) {
		loc := cfScriptOpenRe.FindIndex(content[pos:])
		if loc == nil {
			break
		}
		start := pos + loc[1]
		end := len(content)
		pos = len(content)
		if c := cfScriptCloseRe.FindIndex(content[start:]); c != nil {
			end = start + c[0]
			pos = start + c[1]
		}
		out = append(out, [2]int{start, end})
	}
	return out
}
