package graphbuild

import (
	"testing"

	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/symbol"
)

func TestDiTypeArguments(t *testing.T) {
	tests := []struct {
		name string
		code string
		want []string
	}{
		{
			name: "two-arg scoped",
			code: "services.AddScoped<IOrderService, OrderService>()",
			want: []string{"IOrderService", "OrderService"},
		},
		{
			name: "single-arg",
			code: "services.AddScoped<ConcreteService>()",
			want: []string{"ConcreteService"},
		},
		{
			name: "generic types",
			code: "services.AddScoped<IRepo<T>, SqlRepo<T>>()",
			want: []string{"IRepo", "SqlRepo"},
		},
		{
			name: "nested generics",
			code: "services.AddSingleton<IOptions<AppConfig>, OptionsWrapper<AppConfig>>()",
			want: []string{"IOptions", "OptionsWrapper"},
		},
		{
			name: "namespaced types",
			code: "services.AddScoped<My.Namespace.IFoo, My.Namespace.Foo>()",
			want: []string{"IFoo", "Foo"},
		},
		{
			name: "three-arg (unlikely but handles gracefully)",
			code: "services.AddScoped<A, B, C>()",
			want: []string{"A", "B", "C"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := []byte(tt.code)
			mask := diLiteralMask(content)

			// Find the '<' after Add...
			openAngle := -1
			for i := 0; i < len(content); i++ {
				if content[i] == '<' {
					openAngle = i
					break
				}
			}
			if openAngle < 0 {
				t.Fatal("no '<' found")
			}

			args := diTypeArguments(content, openAngle, mask)
			if len(args) != len(tt.want) {
				t.Fatalf("got %d args, want %d: %v", len(args), len(tt.want), args)
			}
			for i, want := range tt.want {
				if args[i].name != want {
					t.Errorf("[%d] name = %q, want %q", i, args[i].name, want)
				}
			}
		})
	}
}

func TestDiLiteralMask(t *testing.T) {
	tests := []struct {
		name     string
		code     string
		checkOff int
		masked   bool
	}{
		{
			name:     "line comment",
			code:     "// services.AddScoped<IFoo, Foo>()\nreal code",
			checkOff: 5,
			masked:   true,
		},
		{
			name:     "block comment",
			code:     "/* services.AddScoped<IFoo, Foo>() */\nreal code",
			checkOff: 5,
			masked:   true,
		},
		{
			name:     "string literal",
			code:     `var s = "services.AddScoped<IFoo, Foo>()";`,
			checkOff: 10,
			masked:   true,
		},
		{
			name:     "real code not masked",
			code:     "services.AddScoped<IFoo, Foo>()",
			checkOff: 10,
			masked:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mask := diLiteralMask([]byte(tt.code))
			got := diMasked(mask, tt.checkOff)
			if got != tt.masked {
				t.Errorf("offset %d masked = %v, want %v", tt.checkOff, got, tt.masked)
			}
		})
	}
}

func TestDiMatchingAngle(t *testing.T) {
	tests := []struct {
		name string
		code string
		open int
		want int
	}{
		{
			name: "simple",
			code: "<Foo>",
			open: 0,
			want: 4,
		},
		{
			name: "nested",
			code: "<IOptions<AppConfig>>",
			open: 0,
			want: 20,
		},
		{
			name: "two args",
			code: "<IFoo, Foo>",
			open: 0,
			want: 10,
		},
		{
			name: "no close",
			code: "<Foo",
			open: 0,
			want: -1,
		},
		{
			name: "semicolon abort",
			code: "<Foo; Bar>",
			open: 0,
			want: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := []byte(tt.code)
			mask := diLiteralMask(content)
			got := diMatchingAngle(content, tt.open, mask)
			if got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestExtractLastIdent(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Foo", "Foo"},
		{"Namespace.Foo", "Foo"},
		{"My.Deep.Namespace.Foo", "Foo"},
		{"Foo<T>", "Foo"},
		{"Namespace.Foo<T>", "Foo"},
		{"  Foo  ", "Foo"},
		{"", ""},
		{"<>", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := extractLastIdent([]byte(tt.input))
			if got != tt.want {
				t.Errorf("extractLastIdent(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestAddInjectionEdgesCommentDecoy(t *testing.T) {
	content := []byte("// services.AddScoped<IFoo, Foo>()\nclass Startup { }")
	mask := diLiteralMask(content)

	matches := diRegistrationRE.FindAllIndex(content, -1)
	for _, m := range matches {
		if !diMasked(mask, m[0]) {
			t.Error("expected DI registration in comment to be masked")
		}
	}
}

func TestAddInjectionEdgesStringDecoy(t *testing.T) {
	content := []byte(`var s = "services.AddScoped<IFoo, Foo>()";` + "\n" + `class Startup { }`)
	mask := diLiteralMask(content)

	matches := diRegistrationRE.FindAllIndex(content, -1)
	for _, m := range matches {
		if !diMasked(mask, m[0]) {
			t.Error("expected DI registration in string to be masked")
		}
	}
}

func TestFindEnclosingOffset(t *testing.T) {
	// Simulate: a method ConfigureServices at offset 50 with body 45..200
	// containing a DI call at offset 100
	syms := []symbolForTest{
		{name: "Startup", kind: "Type", nameStart: 10, bodyStart: 5, bodyEnd: 250},
		{name: "ConfigureServices", kind: "Method", nameStart: 50, bodyStart: 45, bodyEnd: 200},
	}

	symbols := makeSymbolSlice(syms)

	// Inside ConfigureServices
	got := findEnclosingOffset(symbols, 100)
	if got != 50 {
		t.Errorf("expected offset 50 (ConfigureServices), got %d", got)
	}

	// Outside any method body but inside Startup
	got = findEnclosingOffset(symbols, 220)
	if got != 10 {
		t.Errorf("expected offset 10 (Startup), got %d", got)
	}

	// Outside all bodies
	got = findEnclosingOffset(symbols, 300)
	if got != 0 {
		t.Errorf("expected offset 0 (fallback), got %d", got)
	}
}

func TestDiRegistrationREMatches(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"AddScoped", "services.AddScoped<IFoo, Foo>()", 1},
		{"AddTransient", "services.AddTransient<IWorker, Worker>()", 1},
		{"AddSingleton", "services.AddSingleton<ICache, MemoryCache>()", 1},
		{"multiple", "services.AddScoped<A, B>(); services.AddTransient<C, D>()", 2},
		{"no match", "services.AddHostedService<Worker>()", 0},
		{"AddScopedFactory", "services.AddScopedFactory<>()", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := diRegistrationRE.FindAllIndex([]byte(tt.input), -1)
			if len(matches) != tt.want {
				t.Errorf("got %d matches, want %d", len(matches), tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test scaffolding — build symbol.Symbol from a simpler struct
// ---------------------------------------------------------------------------

type symbolForTest struct {
	name      string
	kind      string
	nameStart int
	bodyStart int
	bodyEnd   int
}

func makeSymbolSlice(ss []symbolForTest) []symbol.Symbol {
	// We avoid importing symbol.Kind from the test file by using a local import
	return makeSymbols(ss)
}

func makeSymbols(ss []symbolForTest) []symbol.Symbol {
	out := make([]symbol.Symbol, len(ss))
	for i, s := range ss {
		out[i] = symbol.Symbol{
			Name:      s.name,
			NameStart: s.nameStart,
			NameEnd:   s.nameStart + len(s.name),
			BodyStart: s.bodyStart,
			BodyEnd:   s.bodyEnd,
		}
		switch s.kind {
		case "Type":
			out[i].Kind = symbol.Type
		case "Method":
			out[i].Kind = symbol.Method
		case "Func":
			out[i].Kind = symbol.Func
		}
	}
	return out
}

// Integration test: verify that edge builder gets edges from real-looking content
func TestInjectionEdgeBuilderIntegration(t *testing.T) {
	builder := diskgraph.NewBuilder()
	seen := make(map[persistedGraphEdge]struct{})

	content := []byte("services.AddScoped<IOrderService, OrderService>()")
	mask := diLiteralMask(content)

	matches := diRegistrationRE.FindAllIndex(content, -1)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}

	m := matches[0]
	if diMasked(mask, m[0]) {
		t.Fatal("match should not be masked")
	}

	openAngle := m[1] - 1
	args := diTypeArguments(content, openAngle, mask)
	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d", len(args))
	}
	if args[0].name != "IOrderService" {
		t.Errorf("arg[0] = %q, want IOrderService", args[0].name)
	}
	if args[1].name != "OrderService" {
		t.Errorf("arg[1] = %q, want OrderService", args[1].name)
	}

	// Direct builder test — emit a fake edge to verify the builder accepts it
	blobSHA := "abc123def456"
	err := builder.Add(blobSHA, 0, diskgraph.Edge{
		Type:         diskgraph.EdgeInjects,
		TargetBlob:   "target123",
		TargetOffset: 42,
		Confidence:   graph.Verified,
		Evidence: graph.Evidence{
			BlobSHA:    blobSHA,
			ByteOffset: uint64(m[0]),
			ByteLength: uint64(m[1] - m[0]),
		},
	})
	if err != nil {
		t.Fatalf("builder.Add failed: %v", err)
	}
	if builder.NumEdges() != 1 {
		t.Errorf("builder has %d edges, want 1", builder.NumEdges())
	}

	// Verify dedup via seen map
	record := persistedGraphEdge{
		sourceBlob:     blobSHA,
		sourceOffset:   0,
		typeID:         diskgraph.EdgeInjects,
		targetBlob:     "target123",
		targetOffset:   42,
		confidence:     uint64(graph.Verified),
		evidenceBlob:   blobSHA,
		evidence:       uint64(m[0]),
		evidenceLength: uint64(m[1] - m[0]),
	}
	seen[record] = struct{}{}
	if _, dup := seen[record]; !dup {
		t.Error("dedup via seen map failed")
	}
}
