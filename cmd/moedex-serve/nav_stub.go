//go:build !lsp

// Default daemon build: LSP-precise navigation (ADR 0017) is an optional arm
// behind -tags lsp. navTools registers no navigation tools, so the daemon serves
// search_context exactly as before with zero navigation dependencies linked.
// Build with `-tags lsp` (alongside `onnx` for the dense daemon) to enable the
// find_definition / find_references / find_implementations MCP tools.

package main

import "moedex/internal/mcp"

func navTools() ([]mcp.ToolHandler, func() error) {
	return nil, func() error { return nil }
}
