package verify

import "regexp"

type patternKey struct {
	lang language
	name string
}

type capturePattern struct {
	re    *regexp.Regexp
	group int
}

type patternSet struct {
	calls       []capturePattern
	types       []capturePattern
	identifiers []capturePattern
}

func compilePatterns(lang language, name string) patternSet {
	q := regexp.QuoteMeta(name)
	boundary := `[^\p{L}\p{N}_$]`

	// The first capture in every language-specific expression is the candidate
	// name. Surrounding syntax is intentionally shallow: this is the regex tier,
	// and comments/strings have already been excluded by the lexical mask.
	call := capturePattern{re: regexp.MustCompile(`(?:^|` + boundary + `)(` + q + `)[\t ]*(?:(?:\[[^\]\r\n]*\]|<[^<>;{}()\r\n]*>)[\t ]*)?\(`), group: 1}

	switch lang {
	case langGo:
		return patternSet{
			calls: []capturePattern{call},
			types: compileAll(1,
				`(?:^|`+boundary+`)(`+q+`)[\t ]*\{`,
				`(?:\*|\[\][\t ]*|\bchan[\t ]+|\bmap\[[^\]\r\n]*\][\t ]*)(`+q+`)(?:$|`+boundary+`)`,
				`\.[\t ]*(`+q+`)(?:$|`+boundary+`)`,
				`(?:^|[,;(][\t ]*)[A-Za-z_][A-Za-z0-9_]*[\t ]+(`+q+`)(?:$|`+boundary+`)`,
			),
		}
	case langCSharp:
		return patternSet{
			calls: []capturePattern{call},
			types: compileAll(1,
				`\bnew[\t ]+(`+q+`)(?:$|`+boundary+`)`,
				`\b(?:typeof|default|sizeof|nameof)[\t ]*\([\t ]*(`+q+`)(?:$|`+boundary+`)`,
				`(?:[:<,]|\b(?:is|as))[\t ]*(`+q+`)(?:$|`+boundary+`)`,
				`(?:^|`+boundary+`)(`+q+`)(?:[\t ]*<[^<>\r\n]*>)?(?:[\t ]*\[\])?[?]?[\t ]+[A-Za-z_][A-Za-z0-9_]*`,
				`\([\t ]*(`+q+`)[\t ]*\)`,
			),
		}
	default:
		// Group 2 is the name because group 1 holds the left boundary. Running
		// this regex (rather than trusting phase 3 blindly) makes the general
		// verifier independently enforce identifier delimitation.
		identifier := capturePattern{
			re:    regexp.MustCompile(`(^|` + boundary + `)(` + q + `)(?:$|` + boundary + `)`),
			group: 2,
		}
		return patternSet{calls: []capturePattern{call}, identifiers: []capturePattern{identifier}}
	}
}

func compileAll(group int, expressions ...string) []capturePattern {
	out := make([]capturePattern, 0, len(expressions))
	for _, expression := range expressions {
		out = append(out, capturePattern{re: regexp.MustCompile(expression), group: group})
	}
	return out
}

func matchesAt(patterns []capturePattern, content []byte, start, end int) bool {
	for _, pattern := range patterns {
		for _, match := range pattern.re.FindAllSubmatchIndex(content, -1) {
			at := pattern.group * 2
			if at+1 < len(match) && match[at] == start && match[at+1] == end {
				return true
			}
		}
	}
	return false
}
