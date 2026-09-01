// Package tui implements Moe's TTY-only interactive search surface.
package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"

	"moedex/internal/app/searchservice"
	"moedex/internal/contextwin"
	"moedex/internal/mcp"
	"moedex/internal/render"
	"moedex/internal/ui/theme"
)

const debounceDelay = 150 * time.Millisecond

// Searcher is the cancellable search boundary exercised by the TUI.
type Searcher interface {
	Search(context.Context, string) (searchservice.Result, error)
}

// Options selects the published source and search window.
type Options struct {
	IndexDir     string
	ShardDir     string
	InitialQuery string
	TokenBudget  int
	TopK         int
	GraphDepth   int
}

// IsInteractive reports whether both input and output are terminals.
func IsInteractive(in, out *os.File) bool {
	return in != nil && out != nil && term.IsTerminal(in.Fd()) && term.IsTerminal(out.Fd())
}

// Run opens the selected published index and starts the alternate-screen UI.
func Run(ctx context.Context, opts Options) error {
	service, err := searchservice.Open(ctx, searchservice.Options{
		IndexDir: opts.IndexDir, ShardDir: opts.ShardDir, TokenBudget: opts.TokenBudget,
		TopK: opts.TopK, GraphDepth: opts.GraphDepth,
	})
	if err != nil {
		return err
	}
	defer service.Close()
	model := NewModel(ctx, service, opts.InitialQuery)
	_, err = tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout)).Run()
	return err
}

type debounceMsg struct{ sequence uint64 }
type searchResultMsg struct {
	sequence uint64
	result   searchservice.Result
	err      error
}

// Model is the interactive search state machine. Searches are debounced and
// prior requests are cancelled; sequence numbers prevent stale replacement.
type Model struct {
	ctx        context.Context
	searcher   Searcher
	input      textinput.Model
	sequence   uint64
	cancel     context.CancelFunc
	loading    bool
	result     searchservice.Result
	err        error
	selected   int
	graphFocus bool
	width      int
	height     int
	status     string
}

// NewModel creates a focused search model.
func NewModel(ctx context.Context, searcher Searcher, initialQuery string) Model {
	input := textinput.New()
	input.Prompt = "Search › "
	input.SetWidth(72)
	input.SetValue(initialQuery)
	_ = input.Focus()
	m := Model{ctx: ctx, searcher: searcher, input: input, width: 100, height: 32}
	if strings.TrimSpace(initialQuery) != "" {
		m.sequence = 1
	}
	return m
}

func (m Model) Init() tea.Cmd {
	focus := m.input.Focus()
	if strings.TrimSpace(m.input.Value()) == "" {
		return focus
	}
	return tea.Batch(focus, debounce(m.sequence, 0))
}

func debounce(sequence uint64, delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg { return debounceMsg{sequence: sequence} })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(max(20, msg.Width-4))
		return m, nil
	case debounceMsg:
		if msg.sequence != m.sequence || strings.TrimSpace(m.input.Value()) == "" {
			return m, nil
		}
		return m.startSearch(msg.sequence)
	case searchResultMsg:
		if msg.sequence != m.sequence {
			return m, nil
		}
		m.loading = false
		m.err = msg.err
		m.result = msg.result
		m.selected = 0
		if msg.err == nil {
			m.status = fmt.Sprintf("%d blocks · ~%d tokens", len(msg.result.Window.Blocks), msg.result.Window.TokenEstimate)
		}
		return m, nil
	case tea.KeyPressMsg:
		switch msg.Key().Keystroke() {
		case "ctrl+c", "ctrl+q", "esc":
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		case "enter":
			if strings.TrimSpace(m.input.Value()) == "" {
				return m, nil
			}
			m.sequence++
			return m.startSearch(m.sequence)
		case "up":
			if m.selected > 0 {
				m.selected--
			}
			return m, nil
		case "down":
			if m.selected+1 < len(m.result.Window.Blocks) {
				m.selected++
			}
			return m, nil
		case "tab", "shift+tab":
			m.graphFocus = !m.graphFocus
			if m.graphFocus {
				m.status = "graph panel focused · tab returns to context"
			} else {
				m.status = "context panel focused · tab opens graph relationships"
			}
			return m, nil
		case "ctrl+y":
			if m.result.Markdown != "" {
				m.status = "copied result with OSC52"
				return m, tea.SetClipboard(m.result.Markdown)
			}
			return m, nil
		}
	}

	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() == before {
		return m, cmd
	}
	m.sequence++
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.err = nil
	m.status = ""
	if strings.TrimSpace(m.input.Value()) == "" {
		m.loading = false
		m.result = searchservice.Result{}
		return m, cmd
	}
	return m, tea.Batch(cmd, debounce(m.sequence, debounceDelay))
}

func (m Model) startSearch(sequence uint64) (Model, tea.Cmd) {
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	m.loading = true
	m.err = nil
	m.status = "searching…"
	query := m.input.Value()
	return m, func() tea.Msg {
		result, err := m.searcher.Search(ctx, query)
		return searchResultMsg{sequence: sequence, result: result, err: err}
	}
}

func (m Model) View() tea.View {
	header := theme.Header("moedex")
	query := lipgloss.NewStyle().Padding(0, 1).Width(max(20, m.width-2)).Render(m.input.View())
	bodyHeight := max(8, m.height-6)
	listWidth := max(28, m.width/3)
	detailWidth := max(36, m.width-listWidth-3)
	list := m.renderList(listWidth, bodyHeight)
	detail := m.renderDetail(detailWidth, bodyHeight)
	var body string
	if m.width >= 88 {
		body = lipgloss.JoinHorizontal(lipgloss.Top, list, " ", detail)
	} else {
		stackHeight := max(4, bodyHeight/2)
		body = lipgloss.JoinVertical(lipgloss.Left, m.renderList(m.width-2, stackHeight), m.renderDetail(m.width-2, stackHeight))
	}
	status := m.status
	if m.err != nil {
		status = "error: " + m.err.Error()
	}
	if status == "" {
		status = "type to search · ↑/↓ select · tab graph · enter now · ctrl+y copy · esc quit"
	}
	footer := theme.Muted(status)
	view := tea.NewView(lipgloss.JoinVertical(lipgloss.Left, " "+header, query, body, footer))
	view.AltScreen = true
	return view
}

func (m Model) renderList(width, height int) string {
	lines := []string{"Ranked context"}
	switch {
	case strings.TrimSpace(m.input.Value()) == "":
		lines = append(lines, "", "Start typing a symbol, concept, or behavior.")
	case m.loading && len(m.result.Window.Blocks) == 0:
		lines = append(lines, "", "Searching published index…")
	case m.err != nil:
		lines = append(lines, "", "Search failed.")
	case len(m.result.Window.Blocks) == 0:
		lines = append(lines, "", "No ranked context found.")
	default:
		for i, block := range m.result.Window.Blocks {
			path := block.RelPath
			if block.Repo != "" {
				path = filepath.ToSlash(filepath.Join(block.Repo, block.RelPath))
			}
			prefix := "  "
			if i == m.selected {
				prefix = "› "
			}
			line := fmt.Sprintf("%s%d  %s:%d", prefix, i+1, path, block.StartLine)
			lines = append(lines, truncate(line, max(8, width-4)))
		}
	}
	return pane(strings.Join(limitLines(lines, height-2), "\n"), width, height)
}

func (m Model) renderDetail(width, height int) string {
	if len(m.result.Window.Blocks) == 0 {
		message := "Results include token-budgeted code blocks and graph relationships when a graph sidecar is published."
		if m.loading {
			message = "Ranking lexical, symbol, and configured dense signals…"
		}
		return pane(message, width, height)
	}
	selected := min(m.selected, len(m.result.Window.Blocks)-1)
	if m.graphFocus {
		return pane(strings.Join(limitLines(m.graphLines(selected), height-2), "\n"), width, height)
	}
	result := searchservice.Result{
		Query:          m.result.Query,
		Window:         m.result.Window,
		GraphAvailable: m.result.GraphAvailable,
	}
	result.Window.Blocks = []contextwin.ContextBlock{m.result.Window.Blocks[selected]}
	if selected < len(m.result.Neighbors) {
		result.Neighbors = m.result.Neighbors[selected : selected+1]
	}
	markdown := searchservice.FormatMarkdown(result)
	markdown = render.Markdown(markdown, width-4)
	return pane(strings.Join(limitLines(strings.Split(strings.TrimSpace(markdown), "\n"), height-2), "\n"), width, height)
}

func (m Model) graphLines(selected int) []string {
	if !m.result.GraphAvailable {
		return []string{"Graph relationships", "", "Graph sidecar unavailable.", "Ranked context remains available."}
	}
	if selected >= len(m.result.Neighbors) {
		return []string{"Graph relationships", "", "No graph annotations for this block."}
	}
	neighbors := m.result.Neighbors[selected]
	lines := []string{"Graph relationships", ""}
	if len(neighbors.Anchors) == 0 {
		return append(lines, "No anchor symbol resolved for this block.")
	}
	lines = append(lines, "anchors: "+strings.Join(neighbors.Anchors, ", "), "")
	buckets := []struct {
		name string
		list []mcp.Neighbor
	}{
		{"callers", neighbors.Callers}, {"callees", neighbors.Callees},
		{"consumers", neighbors.Consumers}, {"publishers", neighbors.Publishers},
		{"depends on", neighbors.DependsOn}, {"similar to", neighbors.SimilarTo},
	}
	for _, bucket := range buckets {
		if len(bucket.list) == 0 {
			continue
		}
		lines = append(lines, bucket.name+":")
		for _, neighbor := range bucket.list {
			name := neighbor.Symbol
			if name == "" {
				name = neighbor.ID
			}
			lines = append(lines, fmt.Sprintf("  %s  %s · %v · %d hop(s)", name, neighbor.Direction, neighbor.Confidence.Tier, neighbor.Hops))
		}
	}
	if len(lines) == 4 {
		lines = append(lines, "No Pattern-or-stronger relationships found.")
	}
	return lines
}

func pane(content string, width, height int) string {
	return theme.Pane(content, width, height)
}

func limitLines(lines []string, count int) []string {
	if count <= 0 || len(lines) <= count {
		return lines
	}
	return lines[:count]
}

func truncate(s string, width int) string {
	if width <= 1 || len([]rune(s)) <= width {
		return s
	}
	runes := []rune(s)
	return string(runes[:width-1]) + "…"
}
