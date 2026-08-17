package httproute

import "strings"

// Method is an HTTP verb.
//
// MethodAny is the zero value and means "not determined by the source": a
// handler registered for every verb (Go's ServeMux, a bare Flask @app.route),
// or a call whose verb is chosen at runtime. It is compatible with every
// concrete verb, so an unread verb never costs an edge.
type Method uint8

const (
	MethodAny Method = iota
	MethodGet
	MethodPost
	MethodPut
	MethodDelete
	MethodPatch
	MethodHead
	MethodOptions
)

// String renders a Method for diagnostics and tests.
func (m Method) String() string {
	switch m {
	case MethodGet:
		return "GET"
	case MethodPost:
		return "POST"
	case MethodPut:
		return "PUT"
	case MethodDelete:
		return "DELETE"
	case MethodPatch:
		return "PATCH"
	case MethodHead:
		return "HEAD"
	case MethodOptions:
		return "OPTIONS"
	default:
		return "ANY"
	}
}

// ParseMethod maps a verb spelling — an attribute suffix, a router method name,
// a literal argument — to a Method, returning MethodAny for anything it does not
// recognize.
func ParseMethod(s string) Method {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "GET":
		return MethodGet
	case "POST":
		return MethodPost
	case "PUT":
		return MethodPut
	case "DELETE":
		return MethodDelete
	case "PATCH":
		return MethodPatch
	case "HEAD":
		return MethodHead
	case "OPTIONS":
		return MethodOptions
	default:
		return MethodAny
	}
}

// Compatible reports whether a call made with method m could be served by a
// handler declared for other.
//
// This is a necessary-condition filter in the same spirit as the trigram->regex
// reduction: it rejects only a PROVEN mismatch (a GET can never reach a
// POST-only handler) and treats every unknown as compatible, so it can remove
// wrong pairs but never a real one.
func (m Method) Compatible(other Method) bool {
	return m == MethodAny || other == MethodAny || m == other
}

// SegmentKind classifies one '/'-delimited path segment.
type SegmentKind uint8

const (
	// SegLiteral is fixed path text: "api", "orders".
	SegLiteral SegmentKind = iota
	// SegParam is a segment that is entirely one placeholder — "{id}",
	// "{id:int}", ":id", "<int:id>", "${id}", "%d" — and so binds to any single
	// segment.
	SegParam
	// SegMixed interleaves literal text and placeholders inside one segment,
	// e.g. "v{version}" or "{baseUrl}api". It binds like a placeholder, but two
	// mixed segments are structurally identical only when they render alike.
	SegMixed
	// SegCatchAll absorbs every remaining segment: ASP.NET "{*rest}", chi "*",
	// gin "*action", Flask "<path:name>".
	SegCatchAll
)

// String renders a SegmentKind for diagnostics and tests.
func (k SegmentKind) String() string {
	switch k {
	case SegParam:
		return "Param"
	case SegMixed:
		return "Mixed"
	case SegCatchAll:
		return "CatchAll"
	default:
		return "Literal"
	}
}

// Segment is one path segment of a route template or client URL.
//
// Text carries the segment's canonical rendering — literal runs lowercased,
// every placeholder collapsed to "{}" — and is empty for SegParam and
// SegCatchAll, whose spelling carries no routing information. Literals are
// case-folded because most routers match paths case-insensitively; folding both
// sides can only ever ADD candidate matches, never drop a real one.
type Segment struct {
	Kind SegmentKind
	Text string
}

// Template is a parsed route template or client URL path: the segment sequence
// that decides which handler serves a request.
//
// Raw keeps the literal exactly as it was written, so a reported edge can be
// traced back to the source text that produced it.
type Template struct {
	Raw      string
	Segments []Segment

	// TrailingSlash records that the raw path ended in '/'. Only some routers
	// give that meaning (see Prefix); parsing merely observes it.
	TrailingSlash bool

	// Prefix marks a subtree route: the handler also serves every deeper path.
	// Go's net/http gives a trailing-slash pattern exactly this meaning, and
	// Express's app.use mounts a router the same way. Extractors set it; the
	// parser does not guess.
	Prefix bool
}

// Empty reports whether the template names no path segments at all.
func (t Template) Empty() bool { return len(t.Segments) == 0 }

// String renders a template in canonical form for diagnostics and tests.
func (t Template) String() string {
	parts := make([]string, 0, len(t.Segments))
	for _, s := range t.Segments {
		switch s.Kind {
		case SegParam:
			parts = append(parts, "{}")
		case SegCatchAll:
			parts = append(parts, "{*}")
		default:
			parts = append(parts, s.Text)
		}
	}
	out := "/" + strings.Join(parts, "/")
	if t.Prefix {
		out += "/*"
	}
	return out
}

// ParseTemplate turns a raw route template or client URL into a Template.
//
// It accepts every placeholder spelling the supported frameworks use, because
// the whole point is to compare a C# "{id}" against an Express ":id" against a
// Go "%d" that fmt.Sprintf will fill in. A query string, a fragment, and an
// absolute URL's scheme and authority are all dropped: none of them participate
// in route selection, and a client's base address is configuration rather than
// routing.
func ParseTemplate(raw string) Template {
	t := Template{Raw: raw}
	path := raw
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	path = stripAuthority(path)
	t.TrailingSlash = strings.HasSuffix(path, "/")
	for _, seg := range strings.Split(path, "/") {
		if seg == "" {
			continue
		}
		t.Segments = append(t.Segments, parseSegment(seg))
	}
	return t
}

// stripAuthority removes "scheme://host" (and a protocol-relative "//host") so
// an absolute client URL compares against a relative handler template. A path
// that is nothing but an authority yields "".
func stripAuthority(s string) string {
	if i := strings.Index(s, "://"); i >= 0 && !strings.ContainsAny(s[:i], "/{$%") {
		rest := s[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			return rest[j:]
		}
		return ""
	}
	if strings.HasPrefix(s, "//") {
		if j := strings.IndexByte(s[2:], '/'); j >= 0 {
			return s[2+j:]
		}
		return ""
	}
	return s
}

// parseSegment classifies a single segment, recognizing every placeholder form
// the supported frameworks spell:
//
//	{id} {id:int} {id?} {*rest} {**rest}   ASP.NET
//	${id}                                  JS/TS template literals
//	:id                                    Express, gin, chi
//	<id> <int:id> <path:id>                Flask
//	%s %d %v ...                           Go fmt.Sprintf
//	*  *action                             chi, gin catch-all
//	[controller] [action]                  unsubstituted ASP.NET tokens
func parseSegment(raw string) Segment {
	var (
		text     strings.Builder
		params   int
		literals int
		catchAll bool
	)
	param := func(all bool) {
		params++
		text.WriteString("{}")
		if all {
			catchAll = true
		}
	}

	for i := 0; i < len(raw); {
		switch {
		case raw[i] == '$' && i+1 < len(raw) && raw[i+1] == '{':
			end, _ := closeDelim(raw, i+1, '{', '}')
			param(false)
			i = end

		case raw[i] == '{':
			end, closed := closeDelim(raw, i, '{', '}')
			// ASP.NET spells a catch-all "{*rest}" / "{**rest}".
			param(strings.HasPrefix(strings.TrimSpace(delimInner(raw, i, end, closed)), "*"))
			i = end

		case raw[i] == '<':
			end, closed := closeDelim(raw, i, '<', '>')
			// Flask's "path" converter is the one that spans '/' separators.
			param(strings.HasPrefix(strings.TrimSpace(delimInner(raw, i, end, closed)), "path:"))
			i = end

		case raw[i] == '[':
			end, _ := closeDelim(raw, i, '[', ']')
			// [controller]/[action] survive only when the extractor could not
			// resolve them; an unresolved token binds like a placeholder.
			param(false)
			i = end

		case raw[i] == ':' && i == 0 && len(raw) > 1:
			param(false)
			i = len(raw)

		case raw[i] == '*' && i == 0:
			param(true)
			i = len(raw)

		case raw[i] == '%' && i+1 < len(raw):
			if raw[i+1] == '%' { // an escaped percent sign is literal text
				literals++
				text.WriteByte('%')
				i += 2
				continue
			}
			if end, ok := formatVerb(raw, i); ok {
				param(false)
				i = end
				continue
			}
			literals++
			text.WriteByte('%')
			i++

		default:
			literals++
			text.WriteByte(lower(raw[i]))
			i++
		}
	}

	switch {
	case catchAll:
		return Segment{Kind: SegCatchAll}
	case params == 0:
		return Segment{Kind: SegLiteral, Text: text.String()}
	case literals == 0 && params == 1:
		return Segment{Kind: SegParam}
	default:
		return Segment{Kind: SegMixed, Text: text.String()}
	}
}

// closeDelim returns the offset just past the delimiter closing the one at
// open, and whether such a delimiter was found. An unterminated placeholder
// consumes the rest of the segment.
func closeDelim(s string, open int, lo, hi byte) (int, bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case lo:
			depth++
		case hi:
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return len(s), false
}

// delimInner returns the text between a placeholder's delimiters.
//
// The bounds check is not decorative: real corpora contain URL literals with a
// lone "<" or an unbalanced "{" — a template string assembled at runtime, a
// generic type spelled inside a path, a truncated constant — and the naive
// slice underflows on a one-character segment.
func delimInner(raw string, open, end int, closed bool) string {
	lo, hi := open+1, end
	if closed {
		hi = end - 1
	}
	if hi > len(raw) {
		hi = len(raw)
	}
	if lo >= hi {
		return ""
	}
	return raw[lo:hi]
}

// formatVerb recognizes a Go fmt verb at s[i] and returns the offset just past
// it.
//
// Both the accepted letters and the refusal to read flags or a width are
// deliberate: '%' is also the escape character of percent-ENCODING, and
// "%20spaces" would otherwise read as a width-20 %s verb, silently turning a
// literal path segment into a placeholder that matches anything. Percent
// encoding is always '%' followed by two hex digits, so requiring the verb
// letter immediately after '%' separates the two cleanly. A width-specified
// verb in a URL loses, which is the right way round — "%20" appears in real
// paths and "%2d" does not.
func formatVerb(s string, i int) (int, bool) {
	if i+1 >= len(s) {
		return 0, false
	}
	switch s[i+1] {
	case 's', 'd', 'v', 'q', 'x', 't':
		return i + 2, true
	}
	return 0, false
}

func lower(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// Quality grades how a client URL matched a route template.
type Quality uint8

const (
	// NoMatch means the two templates cannot describe the same request.
	NoMatch Quality = iota
	// Parameterized means they align only because a placeholder absorbed
	// something spelled differently on the other side — a concrete "42" binding
	// to "{id}", a catch-all swallowing a tail, an unresolved base address
	// dropped from the front of the call. Real evidence, but inferred.
	Parameterized
	// Exact means the two templates are structurally identical: equal arity,
	// with literals matching literals and placeholders matching placeholders
	// position for position. Nothing had to be guessed to line them up.
	Exact
)

// String renders a Quality for diagnostics and tests.
func (q Quality) String() string {
	switch q {
	case Exact:
		return "Exact"
	case Parameterized:
		return "Parameterized"
	default:
		return "NoMatch"
	}
}

const (
	// minLiteralAgreement is the soundness floor on a match. Templates that
	// line up without a single literal segment agreeing — "/{a}/{b}" against
	// "/orders/{id}" — carry no routing evidence at all: an all-placeholder URL
	// aligns with every route of the same arity. Requiring one literal in
	// common is what keeps a degenerate template from matching the corpus.
	minLiteralAgreement = 1

	// maxBasePrefixDrop bounds how many leading placeholder segments may be
	// discarded from a CALL as an unresolved base address (see MatchTemplates).
	maxBasePrefixDrop = 2
)

// MatchTemplates grades a client URL against a handler route template.
//
// The alignment is attempted twice. First the templates are compared head to
// head; a full structural identity is Exact and anything that needed a
// placeholder to bind is Parameterized. If that fails, leading PLACEHOLDER
// segments are dropped from the client one at a time and the comparison is
// retried, which is what rescues the near-universal `$"{_baseUrl}/api/orders"`
// shape where the service address is injected configuration rather than part of
// the route. Only a placeholder may be dropped — a leading literal is real path
// text, and discarding it would match "/v1/api/orders" to "/api/orders" — and a
// match found that way is never better than Parameterized.
func MatchTemplates(call, handler Template) Quality {
	if q := align(call.Segments, handler.Segments, handler.Prefix); q != NoMatch {
		return q
	}
	for drop := 1; drop <= maxBasePrefixDrop && drop < len(call.Segments); drop++ {
		switch call.Segments[drop-1].Kind {
		case SegParam, SegMixed:
		default:
			return NoMatch // a literal prefix is path, not a base address
		}
		if align(call.Segments[drop:], handler.Segments, handler.Prefix) != NoMatch {
			return Parameterized
		}
	}
	return NoMatch
}

// align walks the two segment lists in lockstep and grades the result. A
// catch-all in final position absorbs the whole remainder of the other side,
// and a prefix (subtree) handler tolerates extra trailing call segments.
func align(call, handler []Segment, handlerPrefix bool) Quality {
	exact := true
	literals := 0

	i, j := 0, 0
	for i < len(call) || j < len(handler) {
		switch {
		case j >= len(handler):
			// The handler ran out of segments first. Only a subtree route
			// serves the deeper path the call is asking for.
			if !handlerPrefix {
				return NoMatch
			}
			exact = false
			i = len(call)

		case i >= len(call):
			// The handler still requires segments the call does not supply.
			return NoMatch

		case handler[j].Kind == SegCatchAll && j == len(handler)-1:
			if call[i].Kind != SegCatchAll || i != len(call)-1 {
				exact = false
			}
			i, j = len(call), len(handler)

		case call[i].Kind == SegCatchAll && i == len(call)-1:
			exact = false // the handler is not a catch-all, or the case above would have run
			i, j = len(call), len(handler)

		default:
			agree, structural, literalPair := matchSegment(call[i], handler[j])
			if !agree {
				return NoMatch
			}
			if !structural {
				exact = false
			}
			if literalPair {
				literals++
			}
			i, j = i+1, j+1
		}
	}

	if literals < minLiteralAgreement {
		return NoMatch
	}
	if exact {
		return Exact
	}
	return Parameterized
}

// matchSegment compares one aligned pair. structural reports that the two
// segments are the SAME KIND of thing spelled the same way (which is what makes
// a whole alignment Exact); literalPair reports the literal-to-literal
// agreement that minLiteralAgreement counts.
func matchSegment(call, handler Segment) (agree, structural, literalPair bool) {
	switch {
	case call.Kind == SegLiteral && handler.Kind == SegLiteral:
		eq := call.Text == handler.Text
		return eq, eq, eq
	case call.Kind == SegParam && handler.Kind == SegParam:
		return true, true, false
	case call.Kind == SegMixed && handler.Kind == SegMixed:
		return true, call.Text == handler.Text, false
	default:
		// One side is a placeholder and the other spells something concrete (or
		// a non-terminal catch-all is standing in for a single segment): a real
		// alignment, but one that required binding.
		return true, false, false
	}
}
