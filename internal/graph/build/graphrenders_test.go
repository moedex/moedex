package graphbuild

import (
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
)

func TestExtractAngularSelectors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name: "simple element selector",
			content: `@Component({
  selector: 'app-header',
  templateUrl: './header.component.html',
})
export class HeaderComponent { }`,
			want: []string{"app-header"},
		},
		{
			name: "double-quoted selector",
			content: `@Component({
  selector: "app-footer",
  templateUrl: "./footer.component.html",
})
export class FooterComponent { }`,
			want: []string{"app-footer"},
		},
		{
			name: "attribute selector",
			content: `@Component({
  selector: '[stlr-button]',
  templateUrl: './button.component.html',
})
export class ButtonComponent { }`,
			want: []string{"stlr-button"},
		},
		{
			name: "compound selector with attribute on element",
			content: `@Component({
  selector: 'button[stlrButton], a[stlrButton]',
  templateUrl: './button.component.html',
})
export class StlrButtonComponent { }`,
			want: []string{"stlrButton", "stlrButton"},
		},
		{
			name:    "no selector",
			content: `export class PlainClass { }`,
			want:    nil,
		},
		{
			name: "component without selector field",
			content: `@Component({
  templateUrl: './dialog.component.html',
})
export class DialogComponent { }`,
			want: nil,
		},
		{
			name: "non-custom element selector",
			content: `@Component({
  selector: 'div',
  template: '<p>hello</p>',
})
export class DivComponent { }`,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAngularSelectors([]byte(tt.content))
			if len(got) != len(tt.want) {
				t.Fatalf("got %d selectors, want %d: %v", len(got), len(tt.want), got)
			}
			for i, want := range tt.want {
				if got[i] != want {
					t.Errorf("[%d] got %q, want %q", i, got[i], want)
				}
			}
		})
	}
}

func TestParseAngularSelector(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		{"app-header", []string{"app-header"}},
		{"stlr-dropdown", []string{"stlr-dropdown"}},
		{"button[stlrButton], a[stlrButton]", []string{"stlrButton", "stlrButton"}},
		{"[appTooltip]", []string{"appTooltip"}},
		{"app-card, [app-card]", []string{"app-card", "app-card"}},
		{"div", nil},           // Not a custom element
		{"ng-content", nil},    // Angular built-in
		{"router-outlet", nil}, // Angular built-in
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got := parseAngularSelector(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d, want %d: %v", len(got), len(tt.want), got)
			}
			for i, want := range tt.want {
				if got[i] != want {
					t.Errorf("[%d] got %q, want %q", i, got[i], want)
				}
			}
		})
	}
}

func TestExtractTemplateTags(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "single custom element",
			content: `<app-header></app-header>`,
			want:    []string{"app-header"},
		},
		{
			name:    "multiple custom elements",
			content: `<div><app-header></app-header><stlr-icon>close</stlr-icon></div>`,
			want:    []string{"app-header", "stlr-icon"},
		},
		{
			name:    "self-closing",
			content: `<app-loader />`,
			want:    []string{"app-loader"},
		},
		{
			name:    "native elements skipped",
			content: `<div><span>hello</span><p>text</p></div>`,
			want:    nil,
		},
		{
			name:    "angular built-ins skipped",
			content: `<ng-content></ng-content><ng-container><router-outlet></router-outlet></ng-container>`,
			want:    nil,
		},
		{
			name:    "closing tags not double-counted",
			content: `<app-header>content</app-header>`,
			want:    []string{"app-header"},
		},
		{
			name:    "comments skipped",
			content: `<!-- <app-old-header></app-old-header> --><app-header></app-header>`,
			want:    []string{"app-header"},
		},
		{
			name:    "mixed content",
			content: `<div class="wrapper"><app-nav></app-nav><main><app-content [data]="items"></app-content></main><app-footer></app-footer></div>`,
			want:    []string{"app-nav", "app-content", "app-footer"},
		},
		{
			name:    "empty template",
			content: ``,
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTemplateTags([]byte(tt.content))
			if len(got) != len(tt.want) {
				t.Fatalf("got %d tags, want %d: %v", len(got), len(tt.want), tagNames(got))
			}
			for i, want := range tt.want {
				if got[i].name != want {
					t.Errorf("[%d] got %q, want %q", i, got[i].name, want)
				}
			}
		})
	}
}

func TestSelectorToClassName(t *testing.T) {
	tests := []struct {
		selector string
		want     string
	}{
		{"app-header", "AppHeaderComponent"},
		{"stlr-dropdown", "StlrDropdownComponent"},
		{"my-complex-widget", "MyComplexWidgetComponent"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.selector, func(t *testing.T) {
			got := selectorToClassName(tt.selector)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractInlineTemplateTags(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "inline template with custom element",
			content: "@Component({\n  selector: 'app-wrapper',\n  template: `<app-child></app-child>`\n})\nexport class WrapperComponent { }",
			want:    []string{"app-child"},
		},
		{
			name:    "templateUrl present suppresses inline scan",
			content: "@Component({\n  selector: 'app-wrapper',\n  templateUrl: './wrapper.component.html',\n  template: `<app-child></app-child>`\n})\nexport class WrapperComponent { }",
			want:    nil,
		},
		{
			name:    "no component decorator",
			content: `export class PlainService { }`,
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractInlineTemplateTags([]byte(tt.content))
			if len(got) != len(tt.want) {
				t.Fatalf("got %d tags, want %d: %v", len(got), len(tt.want), tagNames(got))
			}
			for i, want := range tt.want {
				if got[i].name != want {
					t.Errorf("[%d] got %q, want %q", i, got[i].name, want)
				}
			}
		})
	}
}

func tagNames(tags []templateTag) []string {
	names := make([]string, len(tags))
	for i, t := range tags {
		names[i] = t.name
	}
	return names
}

// All edges below are extracted from source, never injected into the builder.
func renderFixtureComponent(repo, path, selector, class string) graphFile {
	return graphFile{repo: repo, path: path, content: "@Component({ selector: '" + selector + "' })\nexport class " + class + " { }\n"}
}

func buildRenderFixture(t *testing.T, files []graphFile) []graphRecord {
	t.Helper()
	dir := t.TempDir()
	writeGraphShards(t, dir, files, 1)
	path, _, err := BuildGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []graphRecord
	for _, record := range readGraph(t, path) {
		if record.edge.Type == diskgraph.EdgeRenders {
			out = append(out, record)
		}
	}
	_, stats, err := RefreshGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Unchanged {
		t.Fatalf("unchanged render graph rebuilt: %+v", stats)
	}
	return out
}

func TestRenderOwnersAndSelectorsRespectRepositoryContext(t *testing.T) {
	files := []graphFile{
		renderFixtureComponent("one", "src/parent.component.ts", "app-parent", "OneParent"),
		renderFixtureComponent("one", "src/child.component.ts", "app-child", "OneChild"),
		{repo: "one", path: "src/parent.component.html", content: "<app-child>one</app-child>"},
		renderFixtureComponent("two", "src/parent.component.ts", "app-parent", "TwoParent"),
		renderFixtureComponent("two", "src/child.component.ts", "app-child", "TwoChild"),
		{repo: "two", path: "src/parent.component.html", content: "<app-child>two</app-child>"},
	}
	sha := func(i int) string { return diskstore.GitBlobSHA1([]byte(files[i].content)) }
	edges := buildRenderFixture(t, files)
	if len(edges) != 4 {
		t.Fatalf("got %d render edges, want both local and diagnostic foreign targets: %+v", len(edges), edges)
	}
	for _, edge := range edges {
		owner, local, template := 0, 1, 2
		if edge.key.BlobSHA == sha(3) {
			owner, local, template = 3, 4, 5
		}
		if edge.key.BlobSHA != sha(owner) || edge.edge.Evidence.BlobSHA != sha(template) {
			t.Errorf("template attributed to wrong repository owner: %+v", edge)
		}
		want := graph.Candidate
		if edge.edge.TargetBlob == sha(local) {
			want = graph.Pattern
		}
		if edge.edge.Confidence != want {
			t.Errorf("render tier %s, want %s: %+v", edge.edge.Confidence, want, edge)
		}
		if edge.edge.Evidence.ByteOffset != 1 || edge.edge.Evidence.ByteLength != uint64(len("app-child")) {
			t.Errorf("wrong tag evidence: %+v", edge)
		}
	}
	// Different shard order must not choose a different selector winner.
	reversed := append([]graphFile{}, files...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	requireSameGraph(t, "render shard order", withoutGenerations(edges), withoutGenerations(buildRenderFixture(t, reversed)))
}

func TestRenderAmbiguityRemainsCandidate(t *testing.T) {
	parent := renderFixtureComponent("one", "parent.component.ts", "app-parent", "Parent")
	child := renderFixtureComponent("one", "child.component.ts", "app-child", "Child")
	template := graphFile{repo: "one", path: "parent.component.html", content: "<app-child></app-child>"}
	for _, tc := range []struct {
		name  string
		files []graphFile
		want  int
	}{
		{"duplicate local selector", []graphFile{parent, child, template, renderFixtureComponent("one", "other.component.ts", "app-child", "OtherChild")}, 2},
		{"shared template", []graphFile{parent, child, template, {repo: "two", path: template.path, content: template.content}}, 1},
		{"shared owner", []graphFile{parent, child, template, {repo: "two", path: parent.path, content: parent.content}}, 1},
		{"shared target", []graphFile{parent, child, template, {repo: "two", path: child.path, content: child.content}}, 1},
		{"multiple owner types", []graphFile{{repo: parent.repo, path: parent.path, content: "export class OtherOwner { }\n" + parent.content}, child, template}, 2},
		{"multiple target types", []graphFile{parent, {repo: child.repo, path: child.path, content: "export class OtherChild { }\n" + child.content}, template}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edges := buildRenderFixture(t, tc.files)
			if len(edges) != tc.want {
				t.Fatalf("got %d edges, want %d: %+v", len(edges), tc.want, edges)
			}
			for _, edge := range edges {
				if edge.edge.Confidence != graph.Candidate {
					t.Errorf("ambiguous render promoted: %+v", edge)
				}
			}
		})
	}
}

func TestRenderFallbackRetainsAllCandidates(t *testing.T) {
	files := []graphFile{
		renderFixtureComponent("one", "parent.component.ts", "app-parent", "Parent"),
		{repo: "one", path: "parent.component.html", content: "<app-ghost></app-ghost>"},
		{repo: "one", path: "first.ts", content: "export class AppGhostComponent { }\n"},
		{repo: "two", path: "second.ts", content: "// unrelated definition\nexport class AppGhostComponent { }\n"},
		{repo: "one", path: "third.cs", content: "public class AppGhostComponent { }\n"},
	}
	edges := buildRenderFixture(t, files)
	if len(edges) != 2 {
		t.Fatalf("fallback must retain both TS types: %+v", edges)
	}
	for _, edge := range edges {
		if edge.edge.Confidence != graph.Candidate {
			t.Errorf("fallback promoted: %+v", edge)
		}
	}
}

func TestRenderNeverBorrowsOwnerFromAnotherRepository(t *testing.T) {
	files := []graphFile{
		renderFixtureComponent("two", "parent.component.ts", "app-parent", "Parent"),
		renderFixtureComponent("one", "child.component.ts", "app-child", "Child"),
		{repo: "one", path: "parent.component.html", content: "<app-child></app-child>"},
	}
	if edges := buildRenderFixture(t, files); len(edges) != 0 {
		t.Fatalf("foreign owner borrowed: %+v", edges)
	}
}
