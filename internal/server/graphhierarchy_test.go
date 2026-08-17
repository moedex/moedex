package server

import (
	"testing"

	"moedex/internal/graph/diskgraph"
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
