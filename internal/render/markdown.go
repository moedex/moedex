// Package render owns terminal rendering shared by shell surfaces.
package render

import "charm.land/glamour/v2"

// Markdown renders CommonMark/GFM for a dark terminal and degrades to the
// original source if the renderer cannot be initialized or convert it.
func Markdown(source string, width int) string {
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(max(20, width)),
	)
	if err != nil {
		return source
	}
	output, err := renderer.Render(source)
	if err != nil {
		return source
	}
	return output
}
