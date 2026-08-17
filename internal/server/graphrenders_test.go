package server

import (
	"testing"
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
			name: "no selector",
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
			name: "inline template with custom element",
			content: "@Component({\n  selector: 'app-wrapper',\n  template: `<app-child></app-child>`\n})\nexport class WrapperComponent { }",
			want: []string{"app-child"},
		},
		{
			name: "templateUrl present suppresses inline scan",
			content: "@Component({\n  selector: 'app-wrapper',\n  templateUrl: './wrapper.component.html',\n  template: `<app-child></app-child>`\n})\nexport class WrapperComponent { }",
			want: nil,
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
