package server

import (
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
	"moedex/internal/symbol"
)

func TestDbSetPropertyExtraction(t *testing.T) {
	content := []byte(`public class AppDbContext : DbContext {
    public DbSet<Order> Orders { get; set; }
    public DbSet<OrderItem> OrderItems { get; set; }
    public DbSet<Customer> Customers { get; set; }
}`)

	matches := dbSetPropertyRE.FindAllSubmatchIndex(content, -1)
	if len(matches) != 3 {
		t.Fatalf("expected 3 DbSet properties, got %d", len(matches))
	}

	expected := []struct{ entity, property string }{
		{"Order", "Orders"},
		{"OrderItem", "OrderItems"},
		{"Customer", "Customers"},
	}
	for i, m := range matches {
		entity := string(content[m[2]:m[3]])
		property := string(content[m[4]:m[5]])
		if entity != expected[i].entity {
			t.Errorf("[%d] entity = %q, want %q", i, entity, expected[i].entity)
		}
		if property != expected[i].property {
			t.Errorf("[%d] property = %q, want %q", i, property, expected[i].property)
		}
	}
}

func TestContextAccessExtraction(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string // expected property names
	}{
		{
			name:    "basic context access",
			content: "var result = context.Orders.Where(o => o.Active);",
			want:    []string{"Orders"},
		},
		{
			name:    "underscore context",
			content: "var result = _context.Orders.ToList();",
			want:    []string{"Orders"},
		},
		{
			name:    "dbContext variable",
			content: "var result = _dbContext.Orders.FirstOrDefault();",
			want:    []string{"Orders"},
		},
		{
			name:    "db variable",
			content: "db.Customers.Add(customer);",
			want:    []string{"Customers"},
		},
		{
			name:    "multiple accesses",
			content: "context.Orders.Where(x => true); context.Customers.ToList();",
			want:    []string{"Orders", "Customers"},
		},
		{
			name:    "no match on plain property",
			content: "var x = foo.Bar.Baz;",
			want:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := contextAccessRE.FindAllSubmatchIndex([]byte(tt.content), -1)
			if len(matches) != len(tt.want) {
				t.Fatalf("got %d matches, want %d", len(matches), len(tt.want))
			}
			for i, m := range matches {
				got := string(tt.content[m[2]:m[3]])
				if got != tt.want[i] {
					t.Errorf("[%d] property = %q, want %q", i, got, tt.want[i])
				}
			}
		})
	}
}

func TestContextSetExtraction(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "basic Set<T>",
			content: "context.Set<Order>()",
			want:    "Order",
		},
		{
			name:    "qualified name",
			content: "_context.Set<Domain.Order>()",
			want:    "Order",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := contextSetRE.FindAllSubmatchIndex([]byte(tt.content), -1)
			if len(matches) != 1 {
				t.Fatalf("got %d matches, want 1", len(matches))
			}
			got := queryLastName(string(tt.content[matches[0][2]:matches[0][3]]))
			if got != tt.want {
				t.Errorf("entity = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestQueryCommentMasking(t *testing.T) {
	content := []byte("// context.Orders.Where(o => true)\ncontext.Customers.Add(c);")
	mask := queryLiteralMask(content)

	// The comment region should be masked.
	commentMatch := contextAccessRE.FindAllSubmatchIndex(content, -1)
	var unmasked []string
	for _, m := range commentMatch {
		if !queryMasked(mask, m[0]) {
			unmasked = append(unmasked, string(content[m[2]:m[3]]))
		}
	}
	if len(unmasked) != 1 {
		t.Fatalf("expected 1 unmasked match, got %d: %v", len(unmasked), unmasked)
	}
	if unmasked[0] != "Customers" {
		t.Errorf("unmasked property = %q, want %q", unmasked[0], "Customers")
	}
}

func TestQueryStringMasking(t *testing.T) {
	content := []byte(`var sql = "context.Orders.Where(x => true)";
context.Customers.Add(c);`)
	mask := queryLiteralMask(content)

	matches := contextAccessRE.FindAllSubmatchIndex(content, -1)
	var unmasked []string
	for _, m := range matches {
		if !queryMasked(mask, m[0]) {
			unmasked = append(unmasked, string(content[m[2]:m[3]]))
		}
	}
	if len(unmasked) != 1 {
		t.Fatalf("expected 1 unmasked match, got %d: %v", len(unmasked), unmasked)
	}
	if unmasked[0] != "Customers" {
		t.Errorf("unmasked property = %q, want %q", unmasked[0], "Customers")
	}
}

func TestQueryEnclosingOffset(t *testing.T) {
	// Simulate symbols: a Type at [0,200) containing a Method at [30,180).
	syms := []symbol.Symbol{
		{Kind: symbol.Type, NameStart: 10, NameEnd: 20, BodyStart: 0, BodyEnd: 200},
		{Kind: symbol.Method, NameStart: 40, NameEnd: 50, BodyStart: 30, BodyEnd: 180},
	}

	// Offset inside the method — should return method's NameStart.
	if got := queryEnclosingOffset(syms, 100); got != 40 {
		t.Errorf("inside method: got %d, want 40", got)
	}

	// Offset inside the type but outside the method — should return type's NameStart.
	if got := queryEnclosingOffset(syms, 190); got != 10 {
		t.Errorf("inside type only: got %d, want 10", got)
	}

	// Offset outside everything — should return 0.
	if got := queryEnclosingOffset(syms, 300); got != 0 {
		t.Errorf("outside all: got %d, want 0", got)
	}
}

func TestQueryLastName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Order", "Order"},
		{"Domain.Order", "Order"},
		{"My.Namespace.Order", "Order"},
	}
	for _, tt := range tests {
		if got := queryLastName(tt.input); got != tt.want {
			t.Errorf("queryLastName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestQueryEdgeIntegration(t *testing.T) {
	// Verify the edge type constant exists and has the expected string.
	if diskgraph.EdgeQueries.String() != "queries" {
		t.Errorf("EdgeQueries.String() = %q, want %q", diskgraph.EdgeQueries.String(), "queries")
	}
}

func TestQueryEdgesResolveSameRepoImportedTypeOnly(t *testing.T) {
	dir := t.TempDir()
	contextContent := []byte(`using App.Entities;
namespace App.Data;
public class AppContext : DbContext {
    public DbSet<Cart> Carts { get; set; }
}`)
	queryContent := []byte(`using App.Entities;
namespace App.Data;
public class Repository {
    public void Load() {
        var carts = context.Carts.ToList();
    }
}`)
	entityContent := []byte("namespace App.Entities;\npublic class Cart {}\n")
	modelContent := []byte("namespace App.Models;\npublic class Cart { public Cart() {} }\n")
	externalContent := []byte("namespace Other.Entities;\npublic class Cart {}\n")
	ix := index.New()
	add := func(repo, rel string, content []byte) string {
		sha := diskstore.GitBlobSHA1(content)
		ix.AddFile(repo, rel, filepath.Join("/corpus", repo, rel), sha, content)
		return sha
	}
	add("app", "Data/AppContext.cs", contextContent)
	querySHA := add("app", "Data/Repository.cs", queryContent)
	entitySHA := add("app", "Entities/Cart.cs", entityContent)
	modelSHA := add("app", "Models/Cart.cs", modelContent)
	externalSHA := add("other", "Entities/Cart.cs", externalContent)
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}
	path, _, err := BuildGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	sourceOffset := uint64(strings.Index(string(queryContent), "Load"))
	var queries []diskgraph.Edge
	for _, edge := range g.Load(querySHA, sourceOffset) {
		if edge.Type == diskgraph.EdgeQueries {
			queries = append(queries, edge)
		}
	}
	if len(queries) != 1 {
		t.Fatalf("query target fan-out = %#v, want one imported entity type", queries)
	}
	if queries[0].TargetBlob != entitySHA || queries[0].Confidence != graph.Verified {
		t.Fatalf("query target = %#v, want App.Entities.Cart Verified", queries[0])
	}
	if queries[0].TargetBlob == modelSHA || queries[0].TargetBlob == externalSHA {
		t.Fatalf("query bound to namespace/repository distractor: %#v", queries[0])
	}
}
