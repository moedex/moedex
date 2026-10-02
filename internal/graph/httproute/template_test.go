package httproute_test

import (
	"testing"

	"moedex/internal/graph/httproute"
	graphverify "moedex/internal/graph/verify"
)

// TestPhase10RequiredMatches pins the two behaviours the phase is specified by:
// a client calling api/orders/{id} reaches the handler declared with
// [HttpGet("api/orders/{id}")], and a client calling /api/users does not reach
// an /api/orders handler.
func TestPhase10RequiredMatches(t *testing.T) {
	orderByID := httproute.ParseTemplate("api/orders/{id}") // [HttpGet("api/orders/{id}")]
	orders := httproute.ParseTemplate("api/orders")

	if got := httproute.MatchTemplates(httproute.ParseTemplate("/api/orders/{id}"), orderByID); got != httproute.Exact {
		t.Errorf("client /api/orders/{id} vs handler api/orders/{id} = %s, want Exact", got)
	}
	if got := httproute.MatchTemplates(httproute.ParseTemplate("/api/users"), orders); got != httproute.NoMatch {
		t.Errorf("client /api/users vs handler api/orders = %s, want NoMatch", got)
	}
	if got := httproute.MatchTemplates(httproute.ParseTemplate("/api/users/7"), orderByID); got != httproute.NoMatch {
		t.Errorf("client /api/users/7 vs handler api/orders/{id} = %s, want NoMatch", got)
	}
}

func TestMatchTemplates(t *testing.T) {
	tests := []struct {
		name    string
		call    string
		handler string
		want    httproute.Quality
	}{
		// Structural identity: nothing had to be guessed to line them up.
		{"identical literals", "/api/orders", "api/orders", httproute.Exact},
		{"identical placeholders", "/api/orders/{id}", "api/orders/{id}", httproute.Exact},
		{"placeholder names differ", "/api/orders/{orderId}", "api/orders/{id}", httproute.Exact},
		{"placeholder syntax differs", "/api/orders/${id}", "api/orders/:id", httproute.Exact},
		{"go verb against aspnet", "/api/orders/%d", "api/orders/{id}", httproute.Exact},
		{"flask against aspnet", "/api/orders/<int:id>", "api/orders/{id}", httproute.Exact},
		{"case folded", "/API/Orders", "api/orders", httproute.Exact},
		{"trailing slash ignored", "/api/orders/", "api/orders", httproute.Exact},
		{"query string ignored", "/api/orders?page=2", "api/orders", httproute.Exact},
		{"absolute url", "https://orders-svc/api/orders", "api/orders", httproute.Exact},

		// Binding was required: real evidence, but inferred.
		{"concrete id binds", "/api/orders/42", "api/orders/{id}", httproute.Parameterized},
		{"literal absorbed by client placeholder", "/api/orders/{id}", "api/orders/summary", httproute.Parameterized},
		{"base address dropped", "/{baseUrl}/api/orders/{id}", "api/orders/{id}", httproute.Parameterized},
		{"two base segments dropped", "/{host}/{stage}/api/orders", "api/orders", httproute.Parameterized},
		{"catch-all absorbs tail", "/api/files/a/b/c", "api/files/{*path}", httproute.Parameterized},
		{"mixed segment binds", "/api/v{n}/orders", "api/v2/orders", httproute.Parameterized},

		// No match at all.
		{"different resource", "/api/users", "api/orders", httproute.NoMatch},
		{"call is shorter", "/api/orders", "api/orders/{id}", httproute.NoMatch},
		{"call is longer", "/api/orders/42/items", "api/orders/{id}", httproute.NoMatch},
		{"literal prefix is not a base address", "/v1/api/orders", "api/orders", httproute.NoMatch},
		{"three base segments exceed the bound", "/{a}/{b}/{c}/api/orders", "api/orders", httproute.NoMatch},
		{"no literal in common", "/{a}/{b}", "orders/{id}", httproute.NoMatch},
		{"root path carries no evidence", "/", "/", httproute.NoMatch},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := httproute.MatchTemplates(httproute.ParseTemplate(tt.call), httproute.ParseTemplate(tt.handler))
			if got != tt.want {
				t.Errorf("MatchTemplates(%q, %q) = %s, want %s", tt.call, tt.handler, got, tt.want)
			}
		})
	}
}

// TestSubtreeHandlerMatchesDeeperPaths covers the prefix semantics Go's
// ServeMux gives a trailing-slash pattern and Express gives app.use.
func TestSubtreeHandlerMatchesDeeperPaths(t *testing.T) {
	subtree := httproute.ParseTemplate("/api/reports/")
	subtree.Prefix = true

	if got := httproute.MatchTemplates(httproute.ParseTemplate("/api/reports/monthly/2026"), subtree); got != httproute.Parameterized {
		t.Errorf("deeper path against subtree route = %s, want Parameterized", got)
	}
	if got := httproute.MatchTemplates(httproute.ParseTemplate("/api/reports"), subtree); got != httproute.Exact {
		t.Errorf("exact path against subtree route = %s, want Exact", got)
	}

	leaf := httproute.ParseTemplate("/api/reports")
	if got := httproute.MatchTemplates(httproute.ParseTemplate("/api/reports/monthly"), leaf); got != httproute.NoMatch {
		t.Errorf("deeper path against leaf route = %s, want NoMatch", got)
	}
}

// TestMethodCompatibilityRejectsOnlyProvenMismatches pins the necessary-condition
// discipline: an unread verb never costs an edge, and a real conflict always
// does.
func TestMethodCompatibilityRejectsOnlyProvenMismatches(t *testing.T) {
	tests := []struct {
		call, handler httproute.Method
		want          bool
	}{
		{httproute.MethodGet, httproute.MethodGet, true},
		{httproute.MethodGet, httproute.MethodPost, false},
		{httproute.MethodDelete, httproute.MethodPut, false},
		{httproute.MethodGet, httproute.MethodAny, true},
		{httproute.MethodAny, httproute.MethodPost, true},
		{httproute.MethodAny, httproute.MethodAny, true},
	}
	for _, tt := range tests {
		if got := tt.call.Compatible(tt.handler); got != tt.want {
			t.Errorf("%s.Compatible(%s) = %v, want %v", tt.call, tt.handler, got, tt.want)
		}
	}
}

func TestParseTemplateSegments(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"api/[controller]", "/api/{}"},
		{"/api/orders/{id:int}", "/api/orders/{}"},
		{"/api/orders/{id?}", "/api/orders/{}"},
		{"/api/files/{*path}", "/api/files/{*}"},
		{"/api/files/<path:name>", "/api/files/{*}"},
		{"/static/*", "/static/{*}"},
		{"/api/v{version}/orders", "/api/v{}/orders"},
		{"http://svc:8080/api/orders#frag", "/api/orders"},
		{"/search/%20spaces", "/search/%20spaces"},
		{"/pct/100%%", "/pct/100%"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := httproute.ParseTemplate(tt.raw).String(); got != tt.want {
				t.Errorf("ParseTemplate(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestParseTemplateSurvivesMalformedInput pins the parser against the URL
// literals a real corpus actually contains: templates assembled at runtime,
// truncated constants, and unbalanced placeholder delimiters. Parsing must
// terminate and produce something matchable rather than panic, since every one
// of these reaches the parser from ordinary source.
func TestParseTemplateSurvivesMalformedInput(t *testing.T) {
	raws := []string{
		"", "/", "//", "///", "<", ">", "{", "}", "[", "]", "$", "%", ":", "*",
		"/<", "/{", "/[", "/${", "/api/<", "/api/{id", "/api/}", "/api/{{id}}",
		"/api/{{", "/api/}}", "/api/List<T>", "/api/{a{b}c", "/api/:", "/api/*",
		"/api/%", "/api/%%", "/api/%s%d", "://", "http://", "http:///api",
		"?", "#", "/api?x=1#f", "{}", "${}", "<>", "[]", "/{*}", "/{**}",
	}
	for _, raw := range raws {
		t.Run(raw, func(t *testing.T) {
			tmpl := httproute.ParseTemplate(raw)
			_ = tmpl.String()
			// Matching a malformed template against itself must also terminate.
			_ = httproute.MatchTemplates(tmpl, tmpl)
			_ = httproute.MatchTemplates(tmpl, httproute.ParseTemplate("/api/orders/{id}"))
			_ = httproute.MatchTemplates(httproute.ParseTemplate("/api/orders/{id}"), tmpl)
		})
	}
}

// TestQualityMapsOntoTheSharedConfidenceLadder pins the numbers phase 10 emits
// against the tiers the rest of the graph layer scores on.
func TestQualityMapsOntoTheSharedConfidenceLadder(t *testing.T) {
	if got := httproute.Exact.Tier(); got != graphverify.Pattern {
		t.Errorf("Exact.Tier() = %s, want Pattern", got)
	}
	if got := httproute.Parameterized.Tier(); got != graphverify.Pattern {
		t.Errorf("Parameterized.Tier() = %s, want Pattern", got)
	}
	if graphverify.Verified.Score() <= graphverify.Pattern.Score() {
		t.Errorf("Verified (%v) must outrank Pattern (%v)", graphverify.Verified.Score(), graphverify.Pattern.Score())
	}
}
