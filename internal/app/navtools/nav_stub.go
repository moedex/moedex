//go:build !lsp

// Default build: LSP-precise navigation (ADR 0017) is an optional arm behind
// -tags lsp. NavTools registers no navigation tools, so the caller serves
// search_context (and, in the daemon's case, the graph tools) exactly as
// before with zero navigation dependencies linked. Build with `-tags lsp`
// (alongside `onnx` for the dense daemon) to enable the find_definition /
// find_references / find_implementations MCP tools.

package navtools

import "moedex/internal/mcp"

// LSPBuild is false in the default build; see nav_lsp.go's doc comment.
const LSPBuild = false

func NavTools() ([]mcp.ToolHandler, func() error) {
	return nil, func() error { return nil }
}
