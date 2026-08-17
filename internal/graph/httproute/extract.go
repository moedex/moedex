package httproute

import (
	"regexp"
	"strings"

	"moedex/internal/index"
	"moedex/internal/symbol"
)

// Role separates the two halves of an HTTP edge.
type Role uint8

const (
	// Handler is a route declaration: the server side that serves a path.
	Handler Role = iota
	// Call is an outbound request: the client side that asks for a path.
	Call
)

// String renders a Role for diagnostics and tests.
func (r Role) String() string {
	if r == Call {
		return "Call"
	}
	return "Handler"
}

// Endpoint is one HTTP endpoint occurrence found in the corpus — a route
// declaration or an outbound call URL.
//
// Start/End bracket the URL literal as written, which is the evidence a reader
// (or a persisted graph edge) is pointed at. Symbol names the definition the
// occurrence lives in, so an edge can be attached to the enclosing controller
// action or calling method rather than to a bare byte offset.
type Endpoint struct {
	Role  Role
	Shard int
	Blob  uint64
	Repo  string
	Path  string

	Start, End int
	Raw        string
	Method     Method
	Template   Template

	// Framework names the recognizer that produced the endpoint, e.g. "aspnet",
	// "nethttp", "express", "flask", "httpclient".
	Framework string

	// Symbol is the enclosing definition's name, and SymbolStart its name
	// offset, or -1 when the occurrence has no enclosing definition.
	Symbol      string
	SymbolStart int
}

// Endpoints is the extracted both-sides view of the corpus.
type Endpoints struct {
	Handlers []Endpoint
	Calls    []Endpoint
}

// markers are the literal framework tokens that seed the trigram fan-out.
//
// Trigram-first, exactly like internal/classify: each marker becomes a sound
// candidate-blob query and the language regexps below are the precision gate.
// Every marker must be a NECESSARY substring of the pattern it stands for, or
// the arm silently loses recall — which is why the receiver-qualified verbs are
// listed as bare ".get(" rather than "app.get(".
var markers = []string{
	// ASP.NET attributes and minimal API.
	"[Route", "[HttpGet", "[HttpPost", "[HttpPut", "[HttpDelete", "[HttpPatch",
	"MapGet", "MapPost", "MapPut", "MapDelete", "MapPatch",
	// Go net/http and the common third-party routers.
	"HandleFunc", ".Handle(",
	".GET(", ".POST(", ".PUT(", ".DELETE(", ".PATCH(",
	".Get(", ".Post(", ".Put(", ".Delete(", ".Patch(",
	// Express / Flask / FastAPI verbs, and the JS + Python client verbs.
	".get(", ".post(", ".put(", ".delete(", ".patch(", ".use(", ".route(",
	".all(", ".request(",
	// HttpClient.
	"GetAsync", "PostAsync", "PutAsync", "DeleteAsync", "PatchAsync", "SendAsync",
	"GetStringAsync", "GetFromJsonAsync", "PostAsJsonAsync", "PutAsJsonAsync",
	"GetByteArrayAsync", "GetStreamAsync", "HttpRequestMessage",
	// Go client.
	"http.Get(", "http.Post(", "http.Head(", "http.PostForm(", "http.NewRequest",
	// fetch / axios.
	"fetch(", "axios",
}

var (
	// ASP.NET routing attributes. The template argument is read with argList
	// rather than a regexp group so a "]" inside "[controller]" cannot end it.
	csAttrRE = regexp.MustCompile(`\[\s*(Route|HttpGet|HttpPost|HttpPut|HttpDelete|HttpPatch|HttpHead|HttpOptions)(?:Attribute)?\b\s*`)
	// ASP.NET minimal API: app.MapGet("/api/orders", handler).
	csMapRE = regexp.MustCompile(`\bMap(Get|Post|Put|Delete|Patch)\s*\(`)
	// HttpClient and its extension methods, with optional generic arguments.
	csClientRE  = regexp.MustCompile(`\.\s*(Get|Post|Put|Patch|Delete|Send)(?:String|ByteArray|Stream|FromJson|AsJson)?Async\s*(?:<[^<>()]*>\s*)?\(`)
	csRequestRE = regexp.MustCompile(`\bnew\s+HttpRequestMessage\s*\(`)
	csMethodRE  = regexp.MustCompile(`\bHttpMethod\s*\.\s*(Get|Post|Put|Delete|Patch|Head|Options)\b`)
	// Everything that may stand between an attribute and the declaration it
	// decorates: whitespace, further attribute lists (whose own arguments may
	// quote a "]"), and modifiers.
	csDeclRE = regexp.MustCompile(`^(?:\s|\[(?:[^\]"]|"(?:\\.|[^"\\])*")*\]|\b(?:public|private|protected|internal|static|async|abstract|sealed|virtual|override|partial|readonly|new|extern|unsafe|required)\b)*`)
	csTypeRE = regexp.MustCompile(`^\s*(?:class|record|struct|interface)\b`)

	// Go net/http registration, plus the verb methods of gin/chi/echo.
	goHandleRE = regexp.MustCompile(`\b(?:[A-Za-z_][A-Za-z0-9_]*\s*\.\s*)?(HandleFunc|Handle)\s*\(`)
	goVerbRE   = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\s*\.\s*(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS|Get|Post|Put|Delete|Patch|Head|Options)\s*\(`)
	goClientRE = regexp.MustCompile(`\bhttp\s*\.\s*(Get|Post|PostForm|Head|NewRequestWithContext|NewRequest)\s*\(`)
	goMethodRE = regexp.MustCompile(`\bhttp\s*\.\s*Method(Get|Post|Put|Delete|Patch|Head|Options)\b`)

	// Express-style registration and the JS/TS clients.
	jsVerbRE   = regexp.MustCompile(`([A-Za-z_$][A-Za-z0-9_$]*)\s*\.\s*(get|post|put|delete|patch|head|options|all|use)\s*\(`)
	jsFetchRE  = regexp.MustCompile(`\bfetch\s*\(`)
	jsAxiosRE  = regexp.MustCompile(`\baxios\s*\(`)
	jsMethodRE = regexp.MustCompile(`\bmethod\s*:\s*['"` + "`" + `](GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS|get|post|put|delete|patch|head|options)`)

	// Flask/FastAPI decorators and the Python clients.
	pyRouteRE     = regexp.MustCompile(`@\s*[A-Za-z_][A-Za-z0-9_.]*\s*\.\s*(route|get|post|put|delete|patch|head|options)\s*\(`)
	pyClientRE    = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*\.\s*(get|post|put|delete|patch|head|options|request)\s*\(`)
	pyMethodsRE   = regexp.MustCompile(`\bmethods\s*=\s*[\[(]([^\])]*)[\])]`)
	pyMethodLitRE = regexp.MustCompile(`['"]([A-Za-z]+)['"]`)
)

// clientReceivers are the receiver names that mark a `x.get(...)` call as an
// outbound request rather than a route registration. The two idioms are
// syntactically identical in JS and Python — `app.get('/users', handler)` and
// `axios.get('/users', config)` differ only in what the receiver IS — so the
// receiver name is the only local signal available. Getting this wrong in the
// permissive direction would invent a HANDLER that real callers then match
// against, so the split is applied to both sides: a client receiver never
// registers a route, and a non-client receiver never issues one.
var clientReceivers = map[string]bool{
	"axios": true, "http": true, "https": true, "httpclient": true,
	"client": true, "apiclient": true, "api": true, "request": true,
	"superagent": true, "got": true, "ky": true, "fetch": true,
	"agent": true, "session": true, "sess": true, "requests": true,
	"httpx": true, "aiohttp": true, "restclient": true, "$http": true,
}

// extractBlob appends every endpoint found in one blob. It is the per-language
// dispatch; each recognizer below confirms its own framework syntax over the
// blob the trigram fan-out proposed.
func extractBlob(shard int, ix *index.Index, sx *symbol.Index, id uint64, out *Endpoints) bool {
	b := ix.Blob(id)
	if b == nil {
		return false
	}
	lang := blobLanguage(b)
	if lang == langUnknown {
		return false
	}
	lx := lex(b.Content, lang)
	e := &extractor{shard: shard, blob: b, sx: sx, lang: lang, lx: &lx, out: out}

	switch lang {
	case langCSharp:
		e.csharpHandlers()
		e.csharpClients()
	case langGo:
		e.goHandlers()
		e.goClients()
	case langJS:
		e.jsHandlers()
		e.jsClients()
	case langPython:
		e.pythonHandlers()
		e.pythonClients()
	}
	return true
}

type extractor struct {
	shard int
	blob  *index.Blob
	sx    *symbol.Index
	lang  language
	lx    *lexed
	out   *Endpoints
}

// emit records one endpoint, attributing it to the definition that ENCLOSES the
// evidence.
func (e *extractor) emit(role Role, lit strLit, tmpl Template, method Method, framework string) {
	e.emitOwned(role, lit, tmpl, method, framework, nil)
}

// emitOwned records an endpoint whose owning definition is known explicitly.
//
// An annotation-style declaration — an ASP.NET attribute, a Flask decorator —
// sits OUTSIDE the member it decorates, so the enclosing definition at that
// offset is the containing class or module rather than the action itself.
// Passing the decorated symbol keeps the edge attached to the handler a reader
// would name.
func (e *extractor) emitOwned(role Role, lit strLit, tmpl Template, method Method, framework string, owner *symbol.Symbol) {
	if tmpl.Empty() {
		return
	}
	ep := Endpoint{
		Role:        role,
		Shard:       e.shard,
		Blob:        e.blob.ID,
		Start:       lit.Start,
		End:         lit.End,
		Raw:         lit.Inner,
		Method:      method,
		Template:    tmpl,
		Framework:   framework,
		SymbolStart: -1,
	}
	if len(e.blob.Files) > 0 {
		ep.Repo = e.blob.Files[0].Repo
		ep.Path = e.blob.Files[0].RelPath
	}
	switch {
	case owner != nil:
		ep.Symbol, ep.SymbolStart = owner.Name, owner.NameStart
	case e.sx != nil:
		if s, ok := e.sx.Enclosing(e.blob.ID, lit.Start); ok {
			ep.Symbol, ep.SymbolStart = s.Name, s.NameStart
		}
	}
	if role == Call {
		e.out.Calls = append(e.out.Calls, ep)
	} else {
		e.out.Handlers = append(e.out.Handlers, ep)
	}
}

// urlArgument picks the request path out of an argument list.
//
// Literals are considered at EVERY nesting depth, deliberately: the URL is
// routinely wrapped — fmt.Sprintf("/api/orders/%d", id), new Uri($"..."),
// string.Format(...) — and the wrapper is not what a reader would call the
// argument.
//
// requireSlash keeps a client call from mistaking an unrelated literal for a
// URL; a route template is read without it, since "[HttpGet(\"submit\")]" names
// a real single-segment route. verbAware consumes a leading literal that is
// exactly an HTTP verb as the method, which is what reads
// http.NewRequest("GET", url, nil) and requests.request("POST", url); it is off
// for route templates, where "[Route(\"get\")]" is a path, not a verb.
func (e *extractor) urlArgument(args span, requireSlash, verbAware bool) (strLit, Method, bool) {
	method := MethodAny
	for _, lit := range e.lx.literalsIn(args.Start, args.End) {
		if lit.Inner == "" {
			continue
		}
		if verbAware && !strings.Contains(lit.Inner, "/") {
			if m := ParseMethod(lit.Inner); m != MethodAny {
				if method == MethodAny {
					method = m
				}
				continue
			}
		}
		if requireSlash && !strings.Contains(lit.Inner, "/") {
			continue
		}
		return lit, method, true
	}
	return strLit{}, method, false
}

// argText returns the raw source of an argument list, for the small number of
// checks that read around the literals (a `method:` option, Flask's `methods=`).
func (e *extractor) argText(args span) string {
	if args.Start < 0 || args.End > len(e.blob.Content) || args.End < args.Start {
		return ""
	}
	return string(e.blob.Content[args.Start:args.End])
}

// callArgs finds the argument list opened at or after the end of a matched call
// token, skipping the whitespace a formatter may have left.
func (e *extractor) callArgs(matchEnd int) (span, bool) {
	i := matchEnd - 1
	if i < 0 || i >= len(e.blob.Content) || e.blob.Content[i] != '(' {
		i = matchEnd
		for i < len(e.blob.Content) && isSpaceByte(e.blob.Content[i]) {
			i++
		}
		if i >= len(e.blob.Content) || e.blob.Content[i] != '(' {
			return span{}, false
		}
	}
	return argList(e.blob.Content, i, e.lang, e.lx)
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// ---------------------------------------------------------------- C# handlers

// csharpHandlers reads ASP.NET routing attributes and minimal-API mappings.
//
// A controller's routes are SPLIT across two declarations — the class carries
// [Route("api/[controller]")] and the action carries [HttpGet("{id}")] — so the
// class-level prefixes are collected first and then joined onto each action.
// Without that join every C# route would be a fragment and would match nothing.
func (e *extractor) csharpHandlers() {
	type methodAttr struct {
		verb     string
		template string
		lit      strLit
		at       int
	}

	prefixes := map[int]string{} // class NameStart -> route prefix
	var actions []methodAttr

	for _, m := range csAttrRE.FindAllSubmatchIndex(e.blob.Content, -1) {
		at := m[0]
		if e.lx.masked(at) {
			continue
		}
		// The declaration begins after the whole attribute LIST, not after the
		// matched name: "[Route(\"api/[controller]\")]" ends several tokens
		// later, and "[HttpGet, Produces]" ends later still.
		end := attrEnd(e.blob.Content, at, e.lx)
		if end < 0 {
			continue
		}
		name := string(e.blob.Content[m[2]:m[3]])

		template := ""
		// A bare [HttpGet] still declares a route — the controller's own prefix
		// — so with no template the attribute itself is the evidence.
		lit := strLit{Start: at, End: end}
		if m[1] < len(e.blob.Content) && e.blob.Content[m[1]] == '(' {
			args, ok := argList(e.blob.Content, m[1], e.lang, e.lx)
			if !ok {
				continue
			}
			if found, _, ok := e.urlArgument(args, false, false); ok {
				template, lit = found.Inner, found
			}
		}

		if e.csharpDeclaresType(end) {
			if s, ok := e.nextSymbol(end); ok && name == "Route" {
				if _, seen := prefixes[s.NameStart]; !seen {
					prefixes[s.NameStart] = template
				}
			}
			continue
		}
		actions = append(actions, methodAttr{verb: name, template: template, lit: lit, at: at})
	}

	for _, a := range actions {
		controller, prefix := "", ""
		if s, ok := e.sx.Enclosing(e.blob.ID, a.at); ok {
			controller = s.Name
			prefix = prefixes[s.NameStart]
		}
		action := ""
		var owner *symbol.Symbol
		if s, ok := e.nextSymbol(a.at); ok {
			action = s.Name
			owner = &s
		}
		raw := joinRoute(prefix, a.template)
		raw = substituteTokens(raw, controller, action)
		method := MethodAny
		if strings.HasPrefix(a.verb, "Http") {
			method = ParseMethod(strings.TrimPrefix(a.verb, "Http"))
		}
		e.emitOwned(Handler, a.lit, ParseTemplate(raw), method, "aspnet", owner)
	}

	for _, m := range csMapRE.FindAllSubmatchIndex(e.blob.Content, -1) {
		if e.lx.masked(m[0]) {
			continue
		}
		args, ok := e.callArgs(m[1])
		if !ok {
			continue
		}
		lit, _, ok := e.urlArgument(args, false, false)
		if !ok {
			continue
		}
		verb := ParseMethod(string(e.blob.Content[m[2]:m[3]]))
		e.emitOwned(Handler, lit, ParseTemplate(lit.Inner), verb, "aspnet-minimal", e.namedHandler(args))
	}
}

// csharpDeclaresType reports whether the declaration starting at off (after any
// further attributes and modifiers) is a type rather than a method — the
// difference between a controller-level route prefix and an action's route.
func (e *extractor) csharpDeclaresType(off int) bool {
	if off < 0 || off >= len(e.blob.Content) {
		return false
	}
	rest := e.blob.Content[off:]
	if len(rest) > maxArgListBytes {
		rest = rest[:maxArgListBytes]
	}
	skip := csDeclRE.Find(rest)
	return csTypeRE.Match(rest[len(skip):])
}

// identChainRE matches a bare identifier or a dotted selector — the shapes a
// registration's handler argument takes when it NAMES a function rather than
// inlining a closure.
var identChainRE = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*(?:\s*\.\s*[A-Za-z_$][A-Za-z0-9_$]*)*$`)

// namedHandler resolves a route registration's handler argument to the
// definition it names.
//
// This is what points an HTTP edge at the function that SERVES the route
// instead of at the one that registered it: in `mux.HandleFunc("/api/orders",
// getOrder)` the enclosing definition is the route table, and getOrder is the
// handler a reader is looking for. An inline closure, a middleware chain, or a
// name defined in another blob all return nil and leave the endpoint attributed
// to its enclosing definition.
func (e *extractor) namedHandler(args span) *symbol.Symbol {
	if e.sx == nil {
		return nil
	}
	arg, ok := secondArg(e.blob.Content, args, e.lx)
	if !ok || arg.End <= arg.Start {
		return nil
	}
	text := string(e.blob.Content[arg.Start:arg.End])
	if !identChainRE.MatchString(text) {
		return nil
	}
	name := text
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = strings.TrimSpace(name[i+1:])
	}
	for _, s := range e.sx.Symbols(e.blob.ID) {
		if s.Name == name {
			return &s
		}
	}
	return nil
}

// nextSymbol returns the first definition in this blob whose name begins at or
// after off — the declaration an attribute decorates.
func (e *extractor) nextSymbol(off int) (symbol.Symbol, bool) {
	if e.sx == nil {
		return symbol.Symbol{}, false
	}
	var best symbol.Symbol
	found := false
	for _, s := range e.sx.Symbols(e.blob.ID) {
		if s.NameStart < off {
			continue
		}
		if !found || s.NameStart < best.NameStart {
			best, found = s, true
		}
	}
	return best, found
}

// joinRoute combines a controller-level prefix with an action-level template.
// A template rooted with "/" or ASP.NET's "~/" replaces the prefix outright.
func joinRoute(prefix, template string) string {
	switch {
	case strings.HasPrefix(template, "~/"):
		return template[1:]
	case strings.HasPrefix(template, "/"):
		return template
	case prefix == "":
		return template
	case template == "":
		return prefix
	}
	return strings.TrimSuffix(prefix, "/") + "/" + template
}

var tokenRE = regexp.MustCompile(`(?i)\[(controller|action)\]`)

// substituteTokens resolves ASP.NET's [controller] and [action] tokens against
// the declarations they were read from. An unresolved token stays in place and
// parses as a placeholder rather than as literal text.
func substituteTokens(route, controller, action string) string {
	return tokenRE.ReplaceAllStringFunc(route, func(tok string) string {
		if strings.EqualFold(tok, "[controller]") {
			if controller == "" {
				return tok
			}
			return strings.TrimSuffix(controller, "Controller")
		}
		if action == "" {
			return tok
		}
		return action
	})
}

// ----------------------------------------------------------------- C# clients

func (e *extractor) csharpClients() {
	for _, m := range csClientRE.FindAllSubmatchIndex(e.blob.Content, -1) {
		if e.lx.masked(m[0]) {
			continue
		}
		args, ok := e.callArgs(m[1])
		if !ok {
			continue
		}
		lit, literalMethod, ok := e.urlArgument(args, true, false)
		if !ok {
			continue
		}
		method := ParseMethod(string(e.blob.Content[m[2]:m[3]]))
		if method == MethodAny {
			method = literalMethod // SendAsync carries its verb elsewhere
		}
		e.emit(Call, lit, ParseTemplate(lit.Inner), method, "httpclient")
	}

	for _, m := range csRequestRE.FindAllIndex(e.blob.Content, -1) {
		if e.lx.masked(m[0]) {
			continue
		}
		args, ok := e.callArgs(m[1])
		if !ok {
			continue
		}
		lit, method, ok := e.urlArgument(args, true, true)
		if !ok {
			continue
		}
		if method == MethodAny {
			if verb := csMethodRE.FindStringSubmatch(e.argText(args)); verb != nil {
				method = ParseMethod(verb[1])
			}
		}
		e.emit(Call, lit, ParseTemplate(lit.Inner), method, "httpclient")
	}
}

// ---------------------------------------------------------------- Go handlers

func (e *extractor) goHandlers() {
	for _, m := range goHandleRE.FindAllSubmatchIndex(e.blob.Content, -1) {
		if e.lx.masked(m[0]) {
			continue
		}
		args, ok := e.callArgs(m[1])
		if !ok || topLevelArgs(e.blob.Content, args, e.lx) < 2 {
			continue
		}
		lit, _, ok := e.urlArgument(args, false, false)
		if !ok {
			continue
		}
		method, path := goPattern(lit.Inner)
		tmpl := ParseTemplate(path)
		// net/http gives a trailing slash subtree semantics: the pattern also
		// serves every deeper path.
		tmpl.Prefix = tmpl.TrailingSlash
		e.emitOwned(Handler, lit, tmpl, method, "nethttp", e.namedHandler(args))
	}

	for _, m := range goVerbRE.FindAllSubmatchIndex(e.blob.Content, -1) {
		if e.lx.masked(m[0]) {
			continue
		}
		args, ok := e.callArgs(m[1])
		if !ok || topLevelArgs(e.blob.Content, args, e.lx) < 2 {
			continue
		}
		lit, _, ok := e.urlArgument(args, true, false)
		// A router registration always names a rooted path. Requiring it is what
		// keeps cache.Get("x", &v) and similar two-argument getters out.
		if !ok || !strings.HasPrefix(lit.Inner, "/") {
			continue
		}
		e.emitOwned(Handler, lit, ParseTemplate(lit.Inner), ParseMethod(string(e.blob.Content[m[2]:m[3]])), "go-router", e.namedHandler(args))
	}
}

// goPattern splits a Go 1.22 ServeMux pattern — "[METHOD ][host]/path" — into
// its verb and path.
func goPattern(raw string) (Method, string) {
	path := strings.TrimSpace(raw)
	method := MethodAny
	if i := strings.IndexByte(path, ' '); i > 0 {
		if m := ParseMethod(path[:i]); m != MethodAny {
			method = m
			path = strings.TrimSpace(path[i+1:])
		}
	}
	// A pattern may be host-qualified; the host is not part of the route.
	if !strings.HasPrefix(path, "/") {
		if i := strings.IndexByte(path, '/'); i >= 0 {
			path = path[i:]
		}
	}
	return method, path
}

// ----------------------------------------------------------------- Go clients

func (e *extractor) goClients() {
	for _, m := range goClientRE.FindAllSubmatchIndex(e.blob.Content, -1) {
		if e.lx.masked(m[0]) {
			continue
		}
		args, ok := e.callArgs(m[1])
		if !ok {
			continue
		}
		lit, literalMethod, ok := e.urlArgument(args, true, true)
		if !ok {
			continue
		}
		method := literalMethod
		switch fn := string(e.blob.Content[m[2]:m[3]]); fn {
		case "Get", "Head":
			method = ParseMethod(fn)
		case "Post", "PostForm":
			method = MethodPost
		default: // NewRequest / NewRequestWithContext carry the verb in an argument
			if method == MethodAny {
				if verb := goMethodRE.FindStringSubmatch(e.argText(args)); verb != nil {
					method = ParseMethod(verb[1])
				}
			}
		}
		e.emit(Call, lit, ParseTemplate(lit.Inner), method, "nethttp-client")
	}
}

// ------------------------------------------------------------- JS/TS handlers

func (e *extractor) jsHandlers() {
	for _, m := range jsVerbRE.FindAllSubmatchIndex(e.blob.Content, -1) {
		if e.lx.masked(m[0]) {
			continue
		}
		receiver := strings.ToLower(string(e.blob.Content[m[2]:m[3]]))
		if clientReceivers[receiver] {
			continue // axios.get(...) is a request, not a registration
		}
		args, ok := e.callArgs(m[1])
		if !ok || topLevelArgs(e.blob.Content, args, e.lx) < 2 {
			continue
		}
		// A registration's second argument is a handler (a function or an
		// identifier); a request's is an options OBJECT. That structural
		// difference separates the two idioms without trusting the name alone.
		if next := secondArgByte(e.blob.Content, args, e.lx); next == '{' {
			continue
		}
		lit, _, ok := e.urlArgument(args, true, false)
		if !ok || !strings.HasPrefix(lit.Inner, "/") {
			continue
		}
		verb := string(e.blob.Content[m[4]:m[5]])
		tmpl := ParseTemplate(lit.Inner)
		method := ParseMethod(verb)
		if verb == "use" {
			// app.use('/api', router) mounts a subtree, not a leaf route.
			tmpl.Prefix = true
		}
		e.emitOwned(Handler, lit, tmpl, method, "express", e.namedHandler(args))
	}
}

// -------------------------------------------------------------- JS/TS clients

func (e *extractor) jsClients() {
	for _, m := range jsFetchRE.FindAllIndex(e.blob.Content, -1) {
		if e.lx.masked(m[0]) {
			continue
		}
		args, ok := e.callArgs(m[1])
		if !ok {
			continue
		}
		lit, _, ok := e.urlArgument(args, true, false)
		if !ok {
			continue
		}
		e.emit(Call, lit, ParseTemplate(lit.Inner), e.fetchMethod(args), "fetch")
	}

	for _, m := range jsAxiosRE.FindAllIndex(e.blob.Content, -1) {
		if e.lx.masked(m[0]) {
			continue
		}
		args, ok := e.callArgs(m[1])
		if !ok {
			continue
		}
		lit, _, ok := e.urlArgument(args, true, false)
		if !ok {
			continue
		}
		method := MethodAny
		if verb := jsMethodRE.FindStringSubmatch(e.argText(args)); verb != nil {
			method = ParseMethod(verb[1])
		}
		e.emit(Call, lit, ParseTemplate(lit.Inner), method, "axios")
	}

	for _, m := range jsVerbRE.FindAllSubmatchIndex(e.blob.Content, -1) {
		if e.lx.masked(m[0]) {
			continue
		}
		if !clientReceivers[strings.ToLower(string(e.blob.Content[m[2]:m[3]]))] {
			continue
		}
		verb := string(e.blob.Content[m[4]:m[5]])
		if verb == "use" || verb == "all" {
			continue
		}
		args, ok := e.callArgs(m[1])
		if !ok {
			continue
		}
		lit, _, ok := e.urlArgument(args, true, false)
		if !ok {
			continue
		}
		e.emit(Call, lit, ParseTemplate(lit.Inner), ParseMethod(verb), "axios")
	}
}

// fetchMethod reads fetch's verb. A one-argument fetch is GET by definition; an
// options object with an unreadable method could be anything, and MethodAny
// keeps it compatible with every handler rather than guessing wrong.
func (e *extractor) fetchMethod(args span) Method {
	if topLevelArgs(e.blob.Content, args, e.lx) < 2 {
		return MethodGet
	}
	if verb := jsMethodRE.FindStringSubmatch(e.argText(args)); verb != nil {
		return ParseMethod(verb[1])
	}
	return MethodAny
}

// ------------------------------------------------------------ Python handlers

func (e *extractor) pythonHandlers() {
	for _, m := range pyRouteRE.FindAllSubmatchIndex(e.blob.Content, -1) {
		if e.lx.masked(m[0]) {
			continue
		}
		args, ok := e.callArgs(m[1])
		if !ok {
			continue
		}
		lit, _, ok := e.urlArgument(args, false, false)
		if !ok {
			continue
		}
		verb := string(e.blob.Content[m[2]:m[3]])
		tmpl := ParseTemplate(lit.Inner)
		// The decorator sits above the view function it declares.
		var owner *symbol.Symbol
		if s, ok := e.nextSymbol(m[0]); ok {
			owner = &s
		}
		if verb != "route" {
			e.emitOwned(Handler, lit, tmpl, ParseMethod(verb), "flask", owner)
			continue
		}
		// Flask's @app.route defaults to GET and lists anything else in
		// methods=[...]; one endpoint per declared verb.
		methods := pyMethodsRE.FindStringSubmatch(e.argText(args))
		if methods == nil {
			e.emitOwned(Handler, lit, tmpl, MethodAny, "flask", owner)
			continue
		}
		for _, verb := range pyMethodLitRE.FindAllStringSubmatch(methods[1], -1) {
			e.emitOwned(Handler, lit, tmpl, ParseMethod(verb[1]), "flask", owner)
		}
	}
}

// ------------------------------------------------------------- Python clients

func (e *extractor) pythonClients() {
	for _, m := range pyClientRE.FindAllSubmatchIndex(e.blob.Content, -1) {
		if e.lx.masked(m[0]) {
			continue
		}
		if !clientReceivers[strings.ToLower(string(e.blob.Content[m[2]:m[3]]))] {
			continue
		}
		if precededByAt(e.blob.Content, m[0]) {
			continue // a decorator is a route declaration, not a request
		}
		args, ok := e.callArgs(m[1])
		if !ok {
			continue
		}
		lit, literalMethod, ok := e.urlArgument(args, true, true)
		if !ok {
			continue
		}
		method := ParseMethod(string(e.blob.Content[m[4]:m[5]]))
		if method == MethodAny {
			method = literalMethod // requests.request("POST", url)
		}
		e.emit(Call, lit, ParseTemplate(lit.Inner), method, "requests")
	}
}

func precededByAt(content []byte, off int) bool {
	for i := off - 1; i >= 0; i-- {
		switch content[i] {
		case ' ', '\t':
		case '@':
			return true
		default:
			return false
		}
	}
	return false
}
