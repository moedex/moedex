package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"moedex/internal/app/searchservice"
	"moedex/internal/contextwin"
)

type searchFunc func(context.Context, string) (searchservice.Result, error)

func (f searchFunc) Search(ctx context.Context, query string) (searchservice.Result, error) {
	return f(ctx, query)
}

func TestDebouncedSearchAcceptsCurrentResult(t *testing.T) {
	t.Parallel()
	searcher := searchFunc(func(_ context.Context, query string) (searchservice.Result, error) {
		return searchservice.Result{Query: query, Window: contextwin.ContextWindow{Blocks: []contextwin.ContextBlock{{RelPath: "current.go"}}}}, nil
	})
	m := NewModel(context.Background(), searcher, "current")
	updated, cmd := m.Update(debounceMsg{sequence: 1})
	m = updated.(Model)
	if cmd == nil || !m.loading {
		t.Fatal("current debounce did not start a search")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if m.loading || m.result.Query != "current" || len(m.result.Window.Blocks) != 1 {
		t.Fatalf("current result was not accepted: %+v", m)
	}
}

func TestStaleResultCannotReplaceCurrentResult(t *testing.T) {
	t.Parallel()
	m := NewModel(context.Background(), searchFunc(func(context.Context, string) (searchservice.Result, error) {
		return searchservice.Result{}, nil
	}), "")
	m.sequence = 3
	m.result.Query = "new"
	updated, _ := m.Update(searchResultMsg{sequence: 2, result: searchservice.Result{Query: "old"}})
	m = updated.(Model)
	if m.result.Query != "new" {
		t.Fatalf("stale result replaced current result: %q", m.result.Query)
	}
}

func TestClipboardUsesFullMarkdownPayload(t *testing.T) {
	t.Parallel()
	m := NewModel(context.Background(), searchFunc(func(context.Context, string) (searchservice.Result, error) {
		return searchservice.Result{}, nil
	}), "")
	m.result.Markdown = "# exact payload\n"
	updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y', Mod: tea.ModCtrl}))
	m = updated.(Model)
	if cmd == nil || m.status != "copied result with OSC52" {
		t.Fatalf("clipboard command not emitted: status=%q", m.status)
	}
}

func TestTabTogglesGraphPanel(t *testing.T) {
	t.Parallel()
	m := NewModel(context.Background(), searchFunc(func(context.Context, string) (searchservice.Result, error) {
		return searchservice.Result{}, nil
	}), "")
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m = updated.(Model)
	if !m.graphFocus || m.status == "" {
		t.Fatalf("tab did not focus graph panel: %+v", m)
	}
}
