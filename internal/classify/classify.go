// Package classify promotes syntactic symbols to framework-aware architectural
// kinds. The pass is deliberately trigram-first: each classifier asks the
// content index for blobs containing a cheap framework marker, then runs a C#
// regexp only over that candidate set. The regexp is the precision gate; the
// trigram query is only a sound, potentially over-approximating filter.
package classify

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"moedex/internal/index"
	"moedex/internal/query"
	"moedex/internal/symbol"
)

// Match records one symbol classification and the source evidence that caused
// it. EvidenceStart is a byte offset in EvidenceBlob, like symbol.Symbol's
// ranges; evidence may live in a different blob from the named definition.
type Match struct {
	Blob          uint64
	Name          string
	NameStart     int
	Previous      symbol.Kind
	Kind          symbol.Kind
	Evidence      string
	EvidenceBlob  uint64
	EvidenceStart int
}

// Report is the result of one architectural classifier. CandidateBlobs is the
// number of unique blobs returned by the framework-marker trigram queries,
// before regexp confirmation. Matches is deterministic (blob, then symbol
// offset) and contains at most one entry per promoted symbol.
type Report struct {
	Kind           symbol.Kind
	CandidateBlobs int
	Matches        []Match
}

// Promoted reports how many matches changed from a generic kind during this
// invocation. Re-running a classifier is idempotent: already-classified symbols
// remain in Matches as confirmed evidence but are not counted as newly promoted.
func (r Report) Promoted() int {
	n := 0
	for _, m := range r.Matches {
		if m.Previous != m.Kind {
			n++
		}
	}
	return n
}

// Summary contains the reports from the complete C# framework pass.
type Summary struct {
	Routes   Report
	Events   Report
	Tables   Report
	Queues   Report
	Services Report
}

// ClassifyAll runs every C# classifier. The order is intentional: specific
// endpoint/data/messaging roles win over the broader DI Service role when one
// definition has evidence for multiple roles. Promote never overwrites a
// different architectural kind.
func ClassifyAll(ix *index.Index, sx *symbol.Index) Summary {
	return Summary{
		Routes:   ClassifyRoutes(ix, sx),
		Events:   ClassifyEvents(ix, sx),
		Tables:   ClassifyTables(ix, sx),
		Queues:   ClassifyQueues(ix, sx),
		Services: ClassifyServices(ix, sx),
	}
}

var (
	// Attribute arguments accept quoted route templates containing ']', such as
	// the very common [Route("api/[controller]")] form.
	apiControllerRE = regexp.MustCompile(`\[(?:ApiController)(?:Attribute)?(?:\s*\((?:[^"\r\n]|"(?:\\.|[^"\\])*")*\))?\s*\]`)
	routeRE         = regexp.MustCompile(`\[(?:Route)(?:Attribute)?(?:\s*\((?:[^"\r\n]|"(?:\\.|[^"\\])*")*\))?\s*\]`)
	httpMethodRE    = regexp.MustCompile(`\[(?:Http(?:Get|Post|Put|Delete))(?:Attribute)?(?:\s*\((?:[^"\r\n]|"(?:\\.|[^"\\])*")*\))?\s*\]`)

	consumerRE     = regexp.MustCompile(`\bIConsumer\s*<\s*([A-Za-z_][A-Za-z0-9_.]*)`)
	consumerNameRE = regexp.MustCompile(`(?m)\b(?:class|record(?:\s+class)?)\s+([A-Za-z_][A-Za-z0-9_]*Consumer)\b`)

	dbSetRE     = regexp.MustCompile(`\bDbSet\s*<\s*([A-Za-z_][A-Za-z0-9_.]*)`)
	dbFactoryRE = regexp.MustCompile(`\bIDbContextFactory\s*<\s*([A-Za-z_][A-Za-z0-9_.]*)`)

	serviceRegistrationRE = regexp.MustCompile(`\bAdd(?:Scoped|Transient|Singleton)\s*<`)
	typeIdentifierRE      = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

	publishEndpointRE = regexp.MustCompile(`\bIPublishEndpoint\b`)
	busPublishRE      = regexp.MustCompile(`\bIBus\s*\.\s*Publish\b`)
	typedBusRE        = regexp.MustCompile(`\bIBus\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
)

// ClassifyRoutes promotes the declaration following ASP.NET [Route],
// [ApiController], and [HttpGet/Post/Put/Delete] attributes. ApiController is
// restricted to a type, HTTP verb attributes to a method/function, and Route
// may decorate either.
func ClassifyRoutes(ix *index.Index, sx *symbol.Index) Report {
	r := newRun(ix, sx, symbol.Route, []string{
		"[Route", "[ApiController", "[HttpGet", "[HttpPost", "[HttpPut", "[HttpDelete",
	})
	for _, id := range r.candidates {
		content, mask, ok := r.csharp(id)
		if !ok {
			continue
		}
		r.attributes(id, content, mask, apiControllerRE, kindSet(symbol.Type))
		r.attributes(id, content, mask, routeRE, kindSet(symbol.Type, symbol.Method, symbol.Func))
		r.attributes(id, content, mask, httpMethodRE, kindSet(symbol.Method, symbol.Func))
	}
	return r.finish()
}

// ClassifyEvents recognizes MassTransit consumers. IConsumer<T> promotes the
// named message type T when its definition is present; otherwise the containing
// consumer type is the best local architectural symbol. A *Consumer type in a
// candidate blob follows the same rule, first trying the conventional message
// name obtained by trimming "Consumer"; this also supports global MassTransit
// imports where the consumer file has no local framework namespace reference.
func ClassifyEvents(ix *index.Index, sx *symbol.Index) Report {
	r := newRun(ix, sx, symbol.Event, []string{"IConsumer", "Consumer"})
	for _, id := range r.candidates {
		content, mask, ok := r.csharp(id)
		if !ok {
			continue
		}
		for _, m := range consumerRE.FindAllSubmatchIndex(content, -1) {
			if masked(mask, m[0]) {
				continue
			}
			name := lastName(string(content[m[2]:m[3]]))
			if !r.promoteNamed(name, kindSet(symbol.Type), "IConsumer<T>", id, m[0]) {
				r.promoteEnclosing(id, m[0], kindSet(symbol.Type), "IConsumer<T>")
			}
		}

		// The *Consumer suffix is itself the MassTransit convention. It cannot
		// require a local `using MassTransit`: many C# projects provide that as a
		// global using in another file.
		for _, m := range consumerNameRE.FindAllSubmatchIndex(content, -1) {
			if masked(mask, m[0]) {
				continue
			}
			consumer := string(content[m[2]:m[3]])
			message := strings.TrimSuffix(consumer, "Consumer")
			if message != "" && r.promoteNamed(message, kindSet(symbol.Type), "MassTransit *Consumer", id, m[0]) {
				continue
			}
			r.promoteAtNameStart(id, m[2], "MassTransit *Consumer", m[0])
		}
	}
	return r.finish()
}

// ClassifyTables recognizes Entity Framework DbSet<T> and
// IDbContextFactory<T>. It promotes the named entity/context type when that
// definition is available, falling back to the containing type for partial
// corpora where the referenced definition is outside the shard.
func ClassifyTables(ix *index.Index, sx *symbol.Index) Report {
	r := newRun(ix, sx, symbol.Table, []string{"DbSet", "IDbContextFactory"})
	for _, id := range r.candidates {
		content, mask, ok := r.csharp(id)
		if !ok {
			continue
		}
		for _, spec := range []struct {
			re       *regexp.Regexp
			evidence string
		}{{dbSetRE, "DbSet<T>"}, {dbFactoryRE, "IDbContextFactory<T>"}} {
			for _, m := range spec.re.FindAllSubmatchIndex(content, -1) {
				if masked(mask, m[0]) {
					continue
				}
				name := lastName(string(content[m[2]:m[3]]))
				if !r.promoteNamed(name, kindSet(symbol.Type), spec.evidence, id, m[0]) {
					r.promoteEnclosing(id, m[0], kindSet(symbol.Type), spec.evidence)
				}
			}
		}
	}
	return r.finish()
}

// ClassifyServices recognizes Microsoft DI AddScoped/AddTransient/AddSingleton
// generic registrations and promotes the implementation (the last registered
// type argument). Single-type registrations promote that sole type.
func ClassifyServices(ix *index.Index, sx *symbol.Index) Report {
	r := newRun(ix, sx, symbol.Service, []string{"AddScoped", "AddTransient", "AddSingleton"})
	for _, id := range r.candidates {
		content, mask, ok := r.csharp(id)
		if !ok {
			continue
		}
		for _, m := range serviceRegistrationRE.FindAllIndex(content, -1) {
			if masked(mask, m[0]) {
				continue
			}
			open := m[1] - 1
			close := matchingAngle(content, open, mask)
			if close < 0 {
				continue
			}
			name := lastTypeArgument(string(content[open+1 : close]))
			if name != "" {
				r.promoteNamed(name, kindSet(symbol.Type), string(content[m[0]:close+1]), id, m[0])
			}
		}
	}
	return r.finish()
}

// ClassifyQueues recognizes MassTransit publisher APIs. IPublishEndpoint marks
// its containing type as a publisher. An explicit IBus.Publish call marks the
// innermost method/function containing the call (falling back to its type). The
// common typed-field form `IBus bus; ... bus.Publish(...)` is resolved within
// the candidate blob as well.
func ClassifyQueues(ix *index.Index, sx *symbol.Index) Report {
	r := newRun(ix, sx, symbol.Queue, []string{"IPublishEndpoint", "IBus"})
	for _, id := range r.candidates {
		content, mask, ok := r.csharp(id)
		if !ok {
			continue
		}
		for _, m := range publishEndpointRE.FindAllIndex(content, -1) {
			if !masked(mask, m[0]) {
				r.promoteEnclosing(id, m[0], kindSet(symbol.Type), "IPublishEndpoint")
			}
		}
		for _, m := range busPublishRE.FindAllIndex(content, -1) {
			if !masked(mask, m[0]) {
				r.promotePublishSite(id, m[0], "IBus.Publish")
			}
		}
		for _, decl := range typedBusRE.FindAllSubmatchIndex(content, -1) {
			if masked(mask, decl[0]) {
				continue
			}
			name := regexp.QuoteMeta(string(content[decl[2]:decl[3]]))
			callRE := regexp.MustCompile(`\b` + name + `\s*\.\s*Publish\b`)
			for _, call := range callRE.FindAllIndex(content, -1) {
				if !masked(mask, call[0]) {
					r.promotePublishSite(id, call[0], "IBus variable Publish")
				}
			}
		}
	}
	return r.finish()
}

type run struct {
	ix         *index.Index
	sx         *symbol.Index
	kind       symbol.Kind
	candidates []uint64
	matches    []Match
	seen       map[symbolKey]bool
}

type symbolKey struct {
	blob      uint64
	nameStart int
}

func newRun(ix *index.Index, sx *symbol.Index, kind symbol.Kind, markers []string) *run {
	return &run{
		ix:         ix,
		sx:         sx,
		kind:       kind,
		candidates: markerCandidates(ix, markers),
		seen:       map[symbolKey]bool{},
	}
}

func (r *run) finish() Report {
	sort.Slice(r.matches, func(i, j int) bool {
		if r.matches[i].Blob != r.matches[j].Blob {
			return r.matches[i].Blob < r.matches[j].Blob
		}
		return r.matches[i].NameStart < r.matches[j].NameStart
	})
	return Report{Kind: r.kind, CandidateBlobs: len(r.candidates), Matches: r.matches}
}

func (r *run) csharp(id uint64) ([]byte, []bool, bool) {
	if r.ix == nil || r.sx == nil {
		return nil, nil, false
	}
	b := r.ix.Blob(id)
	if b == nil || len(b.Files) == 0 || !strings.EqualFold(filepath.Ext(b.Files[0].RelPath), ".cs") {
		return nil, nil, false
	}
	return b.Content, literalMask(b.Content), true
}

func (r *run) attributes(id uint64, content []byte, mask []bool, re *regexp.Regexp, allowed map[symbol.Kind]bool) {
	for _, m := range re.FindAllIndex(content, -1) {
		if masked(mask, m[0]) {
			continue
		}
		if s, ok := r.next(id, m[1], allowed); ok {
			r.promote(id, s, string(content[m[0]:m[1]]), id, m[0])
		}
	}
}

func (r *run) next(blob uint64, after int, allowed map[symbol.Kind]bool) (symbol.Symbol, bool) {
	var best symbol.Symbol
	found := false
	for _, s := range r.sx.Symbols(blob) {
		if s.NameStart < after || !eligible(s.Kind, r.kind, allowed) {
			continue
		}
		if !found || s.NameStart < best.NameStart {
			best, found = s, true
		}
	}
	return best, found
}

func (r *run) promoteNamed(name string, allowed map[symbol.Kind]bool, evidence string, evidenceBlob uint64, evidenceStart int) bool {
	if name == "" || r.sx == nil {
		return false
	}
	matched := false
	for _, ref := range r.sx.Definitions(name) {
		if !isCSharpBlob(r.ix, ref.Blob) {
			continue
		}
		s, ok := symbolAt(r.sx, ref.Blob, ref.Start)
		if !ok || !eligible(s.Kind, r.kind, allowed) {
			continue
		}
		if r.promote(ref.Blob, s, evidence, evidenceBlob, evidenceStart) {
			matched = true
		}
	}
	return matched
}

func (r *run) promoteAtNameStart(blob uint64, nameStart int, evidence string, evidenceStart int) bool {
	s, ok := symbolAt(r.sx, blob, nameStart)
	if !ok {
		return false
	}
	return r.promote(blob, s, evidence, blob, evidenceStart)
}

func (r *run) promoteEnclosing(blob uint64, off int, allowed map[symbol.Kind]bool, evidence string) bool {
	s, ok := enclosing(r.sx, blob, off, r.kind, allowed)
	if !ok {
		return false
	}
	return r.promote(blob, s, evidence, blob, off)
}

func (r *run) promotePublishSite(blob uint64, off int, evidence string) {
	if r.promoteEnclosing(blob, off, kindSet(symbol.Method, symbol.Func), evidence) {
		return
	}
	r.promoteEnclosing(blob, off, kindSet(symbol.Type), evidence)
}

// promote keeps the classified symbol's blob independent from the evidence
// blob. DI/ORM/event evidence commonly names a type defined elsewhere in the
// same shard.
func (r *run) promote(blob uint64, s symbol.Symbol, evidence string, evidenceBlob uint64, evidenceStart int) bool {
	key := symbolKey{blob: blob, nameStart: s.NameStart}
	if r.seen[key] {
		return true
	}
	previous, ok := r.sx.Promote(blob, s.NameStart, r.kind)
	if !ok {
		return false
	}
	r.seen[key] = true
	r.matches = append(r.matches, Match{
		Blob:          blob,
		Name:          s.Name,
		NameStart:     s.NameStart,
		Previous:      previous,
		Kind:          r.kind,
		Evidence:      evidence,
		EvidenceBlob:  evidenceBlob,
		EvidenceStart: evidenceStart,
	})
	return true
}

// markerCandidates unions the sound trigram-query candidates for literal
// framework markers. query.FromRegexp may deliberately return ALL for a short
// or selectively-unindexed marker; regexp confirmation still preserves
// precision in that case.
func markerCandidates(ix *index.Index, markers []string) []uint64 {
	if ix == nil {
		return nil
	}
	seen := map[uint64]bool{}
	for _, marker := range markers {
		q, err := query.FromRegexp(regexp.QuoteMeta(marker))
		if err != nil {
			continue
		}
		for _, id := range q.Eval(ix) {
			seen[id] = true
		}
	}
	out := make([]uint64, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func symbolAt(sx *symbol.Index, blob uint64, nameStart int) (symbol.Symbol, bool) {
	if sx == nil {
		return symbol.Symbol{}, false
	}
	for _, s := range sx.Symbols(blob) {
		if s.NameStart == nameStart {
			return s, true
		}
	}
	return symbol.Symbol{}, false
}

func enclosing(sx *symbol.Index, blob uint64, off int, target symbol.Kind, allowed map[symbol.Kind]bool) (symbol.Symbol, bool) {
	if sx == nil {
		return symbol.Symbol{}, false
	}
	var best symbol.Symbol
	found := false
	for _, s := range sx.Symbols(blob) {
		if off < s.BodyStart || off >= s.BodyEnd || !eligible(s.Kind, target, allowed) {
			continue
		}
		if !found || s.BodyEnd-s.BodyStart < best.BodyEnd-best.BodyStart {
			best, found = s, true
		}
	}
	return best, found
}

func eligible(current, target symbol.Kind, allowed map[symbol.Kind]bool) bool {
	return current == target || allowed[current]
}

func kindSet(kinds ...symbol.Kind) map[symbol.Kind]bool {
	out := make(map[symbol.Kind]bool, len(kinds))
	for _, k := range kinds {
		out[k] = true
	}
	return out
}

func lastName(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i+1:]
	}
	return name
}

func lastTypeArgument(args string) string {
	start := 0
	depth := 0
	for i, c := range args {
		switch c {
		case '<', '(', '[':
			depth++
		case '>', ')', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				start = i + 1
			}
		}
	}
	last := args[start:]
	ids := typeIdentifierRE.FindAllString(last, -1)
	if len(ids) == 0 {
		return ""
	}
	return ids[len(ids)-1]
}

func matchingAngle(content []byte, open int, mask []bool) int {
	if open < 0 || open >= len(content) || content[open] != '<' {
		return -1
	}
	depth := 0
	for i := open; i < len(content); i++ {
		if masked(mask, i) {
			continue
		}
		switch content[i] {
		case '<':
			depth++
		case '>':
			depth--
			if depth == 0 {
				return i
			}
		case ';', '{', '}':
			return -1
		}
	}
	return -1
}

func isCSharpBlob(ix *index.Index, id uint64) bool {
	if ix == nil {
		return false
	}
	b := ix.Blob(id)
	return b != nil && len(b.Files) > 0 && strings.EqualFold(filepath.Ext(b.Files[0].RelPath), ".cs")
}

func masked(mask []bool, off int) bool {
	return off < 0 || off >= len(mask) || mask[off]
}

// literalMask marks comments and string/character literals so framework-looking
// text in documentation or configuration strings cannot classify the following
// declaration. It mirrors the deliberately small lexer used by the symbol
// extractors and is linear in the candidate blob size.
func literalMask(content []byte) []bool {
	mask := make([]bool, len(content))
	mark := func(lo, hi int) {
		if hi > len(mask) {
			hi = len(mask)
		}
		for i := lo; i < hi; i++ {
			mask[i] = true
		}
	}
	for i := 0; i < len(content); {
		switch {
		case content[i] == '/' && i+1 < len(content) && content[i+1] == '/':
			start := i
			i += 2
			for i < len(content) && content[i] != '\n' {
				i++
			}
			mark(start, i)
		case content[i] == '/' && i+1 < len(content) && content[i+1] == '*':
			start := i
			i += 2
			for i+1 < len(content) && !(content[i] == '*' && content[i+1] == '/') {
				i++
			}
			if i+1 < len(content) {
				i += 2
			}
			mark(start, i)
		case content[i] == '"' || content[i] == '\'':
			start, quote := i, content[i]
			if quote == '"' && i+2 < len(content) && content[i+1] == '"' && content[i+2] == '"' {
				i += 3
				for i+2 < len(content) && !(content[i] == '"' && content[i+1] == '"' && content[i+2] == '"') {
					i++
				}
				if i+2 < len(content) {
					i += 3
				}
				mark(start, i)
				continue
			}
			verbatim := quote == '"' && i > 0 && content[i-1] == '@'
			i++
			for i < len(content) {
				if verbatim && content[i] == '"' && i+1 < len(content) && content[i+1] == '"' {
					i += 2
					continue
				}
				if !verbatim && content[i] == '\\' {
					i += 2
					continue
				}
				i++
				if content[i-1] == quote {
					break
				}
			}
			mark(start, i)
		default:
			i++
		}
	}
	return mask
}
