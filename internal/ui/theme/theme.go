// Package theme owns Moe's terminal presentation tokens.
package theme

import "charm.land/lipgloss/v2"

var (
	accent = lipgloss.Color("#8BE9FD")
	border = lipgloss.Color("#6272A4")
)

func Header(value string) string {
	return lipgloss.NewStyle().Bold(true).Foreground(accent).Render(value)
}

func Muted(value string) string {
	return lipgloss.NewStyle().Faint(true).PaddingLeft(1).Render(value)
}

func Pane(content string, width, height int) string {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(0, 1).
		Width(max(10, width-4)).
		Height(max(3, height-2)).
		Render(content)
}
