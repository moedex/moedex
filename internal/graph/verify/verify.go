// Package verify applies the graph layer's cheap, language-aware regex tier to
// the recall-complete edge work list produced by internal/graph/candidates.
//
// Verification is deliberately confidence assignment, not filtering. A source
// occurrence recognized as a call, import, type use, or unmasked identifier is
// promoted to Pattern confidence. Everything else remains in the returned slice
// at Candidate confidence, including comments, quoted strings, malformed source,
// and candidates whose evidence blob is no longer available. This invariant is
// what lets later (LSP or human) tiers reconsider weak evidence without rerunning
// the corpus-wide fan-out.
package verify

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"

	"moedex/internal/graph"
	"moedex/internal/graph/candidates"
	"moedex/internal/index"
)

// Tier is the shared graph confidence enum.
type Tier = graph.ConfidenceTier

const (
	Candidate = graph.Candidate
	Pattern   = graph.Pattern
	Verified  = graph.Verified
	Proven    = graph.Proven
)

// ReferenceKind records which cheap pattern promoted an edge. It is evidence
// metadata, not a type-resolved graph relation: a same-named call can still be
// paired with several candidate definitions.
type ReferenceKind uint8

const (
	Unverified ReferenceKind = iota
	Call
	Import
	TypeReference
	// IdentifierMatch is the fallback for an unmasked, identifier-delimited
	// occurrence in a language without a more specific verifier. Despite the
	// name, quoted string literals do not qualify.
	IdentifierMatch
)

// String renders a ReferenceKind for diagnostics and tests.
func (k ReferenceKind) String() string {
	switch k {
	case Call:
		return "Call"
	case Import:
		return "Import"
	case TypeReference:
		return "TypeReference"
	case IdentifierMatch:
		return "IdentifierMatch"
	default:
		return "Unverified"
	}
}

// Edge is a phase-3 candidate annotated with regex-tier evidence. Embedding the
// candidate preserves its source/target identity and evidence type verbatim.
type Edge struct {
	candidates.Edge
	Kind ReferenceKind
}

// ScoredEdge is the descriptive name for Edge at API boundaries.
type ScoredEdge = Edge

type language uint8

const (
	langGeneral language = iota
	langGo
	langCSharp
)

type region uint8

const (
	regionCode region = iota
	regionString
	regionComment
)

type regionKey struct {
	blob *index.Blob
	lang language
}

// Verify applies the regex tier to every candidate and returns exactly one
// scored edge for every input edge, in the same order. Generated candidates
// carry their evidence blob with them; a hand-built candidate without evidence
// is retained at Candidate confidence.
func Verify(input []candidates.Edge) []Edge {
	return NewSession(nil).Verify(input)
}

const maxSessionPatterns = 128

// Session reuses bounded compiled patterns across verification batches for one
// name. It is not concurrency-safe; independently prepared names use separate
// sessions and may share the concurrency-safe corpus RegionCache.
type Session struct {
	regions  *RegionCache
	patterns map[patternKey]patternSet
}

func NewSession(regions *RegionCache) *Session {
	return &Session{regions: regions, patterns: make(map[patternKey]patternSet)}
}

// Verify preserves Verify's ordering and one-result-per-input contract.
func (s *Session) Verify(input []candidates.Edge) []Edge {
	if len(input) == 0 {
		return nil
	}

	out := make([]Edge, len(input))
	regions := make(map[regionKey][]region)
	for i, candidate := range input {
		out[i] = Edge{
			Edge: candidate,
			Kind: Unverified,
		}
		out[i].Confidence = graph.Candidate
		blob := candidate.EvidenceBlob()
		if blob == nil || candidate.Type == candidates.SiblingDefinition || !validSite(candidate, blob.Content) {
			continue
		}

		for _, lang := range languages(blob) {
			key := regionKey{blob: blob, lang: lang}
			mask, ok := regions[key]
			if !ok {
				mask = s.regions.regions(blob, lang)
				regions[key] = mask
			}
			if len(s.patterns) >= maxSessionPatterns {
				if _, cached := s.patterns[patternKey{lang: lang, name: candidate.Name}]; !cached {
					clear(s.patterns)
				}
			}
			kind := verifyOne(candidate, blob.Content, lang, mask, s.patterns)
			if kind == Unverified {
				continue
			}
			out[i].Confidence = graph.Pattern
			out[i].Kind = kind
			break
		}
	}
	return out
}

func validSite(candidate candidates.Edge, content []byte) bool {
	s := candidate.Source
	return candidate.Name != "" && s.Start >= 0 && s.End == s.Start+len(candidate.Name) &&
		s.End <= len(content) && string(content[s.Start:s.End]) == candidate.Name &&
		identifierAt(content, s.Start, s.End)
}

func languages(blob *index.Blob) []language {
	seen := map[language]bool{}
	var out []language
	for _, file := range blob.Files {
		lang := langGeneral
		switch strings.ToLower(filepath.Ext(file.RelPath)) {
		case ".go":
			lang = langGo
		case ".cs":
			lang = langCSharp
		}
		if !seen[lang] {
			seen[lang] = true
			out = append(out, lang)
		}
	}
	if len(out) == 0 {
		out = append(out, langGeneral)
	}
	return out
}

func verifyOne(candidate candidates.Edge, content []byte, lang language, mask []region, cache map[patternKey]patternSet) ReferenceKind {
	start, end := candidate.Source.Start, candidate.Source.End

	// An import path is necessarily quoted in Go, so recognize a real import
	// declaration before applying the general string-literal rejection.
	if lang == langGo && goImportAt(content, mask, start, end) {
		return Import
	}
	if start >= len(mask) || mask[start] != regionCode {
		return Unverified
	}
	if lang == langCSharp && csharpUsingAt(content, mask, start, end) {
		return Import
	}

	key := patternKey{lang: lang, name: candidate.Name}
	set, ok := cache[key]
	if !ok {
		set = compilePatterns(lang, candidate.Name)
		cache[key] = set
	}
	line, relStart, relEnd := sourceLine(content, start, end)
	if matchesAt(set.calls, line, relStart, relEnd) {
		return Call
	}
	if matchesAt(set.types, line, relStart, relEnd) {
		return TypeReference
	}
	if lang == langGeneral && matchesAt(set.identifiers, line, relStart, relEnd) {
		return IdentifierMatch
	}
	return Unverified
}

func sourceLine(content []byte, start, end int) ([]byte, int, int) {
	lineStart := start
	for lineStart > 0 && content[lineStart-1] != '\n' {
		lineStart--
	}
	lineEnd := end
	for lineEnd < len(content) && content[lineEnd] != '\n' && content[lineEnd] != '\r' {
		lineEnd++
	}
	return content[lineStart:lineEnd], start - lineStart, end - lineStart
}

func identifierAt(content []byte, start, end int) bool {
	return (start == 0 || !identifierByte(content[start-1])) &&
		(end == len(content) || !identifierByte(content[end]))
}

func identifierByte(b byte) bool {
	return b == '_' || b == '$' || b >= 0x80 ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

var (
	goImportRE = regexp.MustCompile(`(?ms)^[\t ]*import[\t ]*(?:\([^)]*\)|[^\r\n]*)`)
	csUsingRE  = regexp.MustCompile(`(?m)^[\t ]*(?:global[\t ]+)?using[\t ]+(?:static[\t ]+)?(?:[A-Za-z_][A-Za-z0-9_]*[\t ]*=[\t ]*)?(?:global::)?[A-Za-z_][A-Za-z0-9_]*(?:[\t ]*\.[\t ]*[A-Za-z_][A-Za-z0-9_]*)*[\t ]*;`)
)

func goImportAt(content []byte, mask []region, start, end int) bool {
	for _, match := range goImportRE.FindAllIndex(content, -1) {
		if match[0] >= len(mask) || mask[match[0]] != regionCode || start < match[0] || end > match[1] {
			continue
		}
		// An import alias is code and an import path is a string. A comment
		// inside an import block is neither and must stay a Candidate.
		if mask[start] != regionComment && identifierAt(content, start, end) {
			return true
		}
	}
	return false
}

func csharpUsingAt(content []byte, mask []region, start, end int) bool {
	if mask[start] != regionCode {
		return false
	}
	// The using pattern is anchored at a line start and permits no LF inside
	// a match. Only the LF-delimited line containing this occurrence can match.
	// Do not split at CR: regexp's multiline ^ does not treat bare CR as a
	// line boundary, and sourceLine's CR handling would change the result.
	lineStart := bytes.LastIndexByte(content[:start], '\n') + 1
	lineEnd := len(content)
	if next := bytes.IndexByte(content[start:], '\n'); next >= 0 {
		lineEnd = start + next
	}
	if end > lineEnd {
		return false
	}
	match := csUsingRE.FindIndex(content[lineStart:lineEnd])
	return match != nil && mask[lineStart+match[0]] == regionCode &&
		start >= lineStart+match[0] && end <= lineStart+match[1]
}
