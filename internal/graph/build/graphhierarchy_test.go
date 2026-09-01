package graphbuild

import (
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
	"moedex/internal/symbol"
)

func TestCSExtractInheritance(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []inheritedSuper
	}{
		{
			name:    "class extends one",
			content: "public class Foo : Bar { }",
			want: []inheritedSuper{
				{superName: "Bar", edgeType: diskgraph.EdgeExtends},
			},
		},
		{
			name:    "class implements interface",
			content: "public class Foo : IBar { }",
			want: []inheritedSuper{
				{superName: "IBar", edgeType: diskgraph.EdgeImplements},
			},
		},
		{
			name:    "class with base and interfaces",
			content: "public class Foo : Bar, IBaz, IQux { }",
			want: []inheritedSuper{
				{superName: "Bar", edgeType: diskgraph.EdgeExtends},
				{superName: "IBaz", edgeType: diskgraph.EdgeImplements},
				{superName: "IQux", edgeType: diskgraph.EdgeImplements},
			},
		},
		{
			name:    "generic class with generic base",
			content: "public class Foo<T> : Bar<T>, IDisposable { }",
			want: []inheritedSuper{
				{superName: "Bar", edgeType: diskgraph.EdgeExtends},
				{superName: "IDisposable", edgeType: diskgraph.EdgeImplements},
			},
		},
		{
			name:    "class with where clause",
			content: "public class Foo<T> : Bar<T> where T : class { }",
			want: []inheritedSuper{
				{superName: "Bar", edgeType: diskgraph.EdgeExtends},
			},
		},
		{
			name:    "interface extends interfaces",
			content: "public interface IFoo : IBar, IBaz { }",
			want: []inheritedSuper{
				{superName: "IBar", edgeType: diskgraph.EdgeImplements},
				{superName: "IBaz", edgeType: diskgraph.EdgeImplements},
			},
		},
		{
			name:    "record with primary constructor",
			content: "public record Foo(int X) : Bar;",
			want: []inheritedSuper{
				{superName: "Bar", edgeType: diskgraph.EdgeExtends},
			},
		},
		{
			name:    "no inheritance",
			content: "public class Foo { }",
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := []byte(tt.content)
			syms, _ := symbol.CSharpExtractor{}.Extract(content)
			got := csExtractInheritance(content, syms)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d supers, want %d: %+v", len(got), len(tt.want), got)
			}
			for i, want := range tt.want {
				if got[i].superName != want.superName {
					t.Errorf("[%d] superName = %q, want %q", i, got[i].superName, want.superName)
				}
				if got[i].edgeType != want.edgeType {
					t.Errorf("[%d] edgeType = %v, want %v", i, got[i].edgeType, want.edgeType)
				}
			}
		})
	}
}

func TestTSExtractInheritance(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []inheritedSuper
	}{
		{
			name:    "class extends",
			content: "export class Foo extends Bar { }",
			want: []inheritedSuper{
				{superName: "Bar", edgeType: diskgraph.EdgeExtends},
			},
		},
		{
			name:    "class extends and implements",
			content: "export class Foo extends Bar implements IBaz, IQux { }",
			want: []inheritedSuper{
				{superName: "Bar", edgeType: diskgraph.EdgeExtends},
				{superName: "IBaz", edgeType: diskgraph.EdgeImplements},
				{superName: "IQux", edgeType: diskgraph.EdgeImplements},
			},
		},
		{
			name:    "class implements only",
			content: "class Foo implements IBar { }",
			want: []inheritedSuper{
				{superName: "IBar", edgeType: diskgraph.EdgeImplements},
			},
		},
		{
			name:    "generic extends generic",
			content: "class Foo<T> extends Bar<T> { }",
			want: []inheritedSuper{
				{superName: "Bar", edgeType: diskgraph.EdgeExtends},
			},
		},
		{
			name:    "interface extends",
			content: "interface IFoo extends IBar { }",
			want: []inheritedSuper{
				{superName: "IBar", edgeType: diskgraph.EdgeExtends},
			},
		},
		{
			name:    "no inheritance",
			content: "class Foo { }",
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := []byte(tt.content)
			syms, _ := symbol.TSExtractor{}.Extract(content)
			got := tsExtractInheritance(content, syms)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d supers, want %d: %+v", len(got), len(tt.want), got)
			}
			for i, want := range tt.want {
				if got[i].superName != want.superName {
					t.Errorf("[%d] superName = %q, want %q", i, got[i].superName, want.superName)
				}
				if got[i].edgeType != want.edgeType {
					t.Errorf("[%d] edgeType = %v, want %v", i, got[i].edgeType, want.edgeType)
				}
			}
		})
	}
}

// TestCSHierarchyResolvedKindOverridesIPrefixHeuristic covers F-32:
// csInferEdgeType's I-prefix name heuristic misclassifies a locally-defined
// class whose name happens to match I[A-Z]... (e.g. IDGenerator) as an
// interface, emitting EdgeImplements where EdgeExtends is correct. Once the
// super resolves to a local definition, resolveAndEmitSuper must defer to
// that definition's own declaring keyword instead of the name guess.
func TestCSHierarchyResolvedKindOverridesIPrefixHeuristic(t *testing.T) {
	dir := t.TempDir()

	// IDGenerator is a locally-defined CLASS whose name matches the I[A-Z]...
	// convention csInferEdgeType reads as "this is an interface".
	idgenContent := []byte("public class IDGenerator\n{\n}\n")
	idgenSHA := diskstore.GitBlobSHA1(idgenContent)

	// IBar is a real, locally-defined interface — kept alongside IDGenerator
	// so the fix is proven to still classify a genuine interface correctly via
	// its own keyword, not merely by no longer being wrong about classes.
	ibarContent := []byte("public interface IBar\n{\n}\n")
	ibarSHA := diskstore.GitBlobSHA1(ibarContent)

	defsShard := index.New()
	defsShard.AddFile("repo", "idgen.cs", "/repo/idgen.cs", idgenSHA, idgenContent)
	defsShard.AddFile("repo", "ibar.cs", "/repo/ibar.cs", ibarSHA, ibarContent)
	if err := diskstore.Save(defsShard, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}

	fooContent := []byte("public class Foo : IDGenerator\n{\n}\n")
	fooSHA := diskstore.GitBlobSHA1(fooContent)
	bazContent := []byte("public class Baz : IBar\n{\n}\n")
	bazSHA := diskstore.GitBlobSHA1(bazContent)

	usesShard := index.New()
	usesShard.AddFile("repo", "foo.cs", "/repo/foo.cs", fooSHA, fooContent)
	usesShard.AddFile("repo", "baz.cs", "/repo/baz.cs", bazSHA, bazContent)
	if err := diskstore.Save(usesShard, filepath.Join(dir, "shard-0001.idx")); err != nil {
		t.Fatal(err)
	}

	path, _, err := BuildGraph(dir)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatalf("open graph: %v", err)
	}
	defer g.Close()

	// The graph also carries a general EdgeUsesType reference edge between the
	// same source/target — findEdge narrows to the hierarchy edge (extends or
	// implements) specifically, since that's the one F-32 is about.
	findEdge := func(sourceSHA string, sourceOffset uint64, targetSHA string, targetOffset uint64) *diskgraph.Edge {
		for _, e := range g.Load(sourceSHA, sourceOffset) {
			if e.TargetBlob != targetSHA || e.TargetOffset != targetOffset {
				continue
			}
			if e.Type != diskgraph.EdgeExtends && e.Type != diskgraph.EdgeImplements {
				continue
			}
			return &e
		}
		return nil
	}

	fooOffset := uint64(strings.Index(string(fooContent), "Foo"))
	idgenOffset := uint64(strings.Index(string(idgenContent), "IDGenerator"))
	edge := findEdge(fooSHA, fooOffset, idgenSHA, idgenOffset)
	if edge == nil {
		t.Fatalf("no edge found from Foo to IDGenerator; edges = %#v", g.Load(fooSHA, fooOffset))
	}
	if edge.Type != diskgraph.EdgeExtends {
		t.Errorf("Foo -> IDGenerator edge type = %v, want EdgeExtends (IDGenerator is a locally-defined class)", edge.Type)
	}

	bazOffset := uint64(strings.Index(string(bazContent), "Baz"))
	ibarOffset := uint64(strings.Index(string(ibarContent), "IBar"))
	edge = findEdge(bazSHA, bazOffset, ibarSHA, ibarOffset)
	if edge == nil {
		t.Fatalf("no edge found from Baz to IBar; edges = %#v", g.Load(bazSHA, bazOffset))
	}
	if edge.Type != diskgraph.EdgeImplements {
		t.Errorf("Baz -> IBar edge type = %v, want EdgeImplements (IBar is a locally-defined interface)", edge.Type)
	}
}

func TestContainmentEdges(t *testing.T) {
	content := []byte(`public class Foo {
    public void Bar() { }
    public int Baz() { return 1; }
}`)
	syms, _ := symbol.CSharpExtractor{}.Extract(content)
	if len(syms) < 3 {
		t.Fatalf("expected >= 3 symbols, got %d: %+v", len(syms), syms)
	}

	builder := diskgraph.NewBuilder()
	seen := make(map[persistedGraphEdge]struct{})
	var report HierarchyReport
	err := emitContainmentEdges(builder, seen, "abc123", syms, &report)
	if err != nil {
		t.Fatal(err)
	}
	if report.ContainsEdges == 0 {
		t.Fatal("expected at least one contains_method edge")
	}
	if int(builder.NumEdges()) != report.ContainsEdges {
		t.Fatalf("builder has %d edges, report says %d", builder.NumEdges(), report.ContainsEdges)
	}
}
