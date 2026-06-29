//go:build lsp

// This file is compiled only with `-tags lsp`. It exposes the LSP-precise
// navigation arm (ADR 0017) as MCP tools on the warm daemon — find_definition /
// find_references / find_implementations — backed by a shared navigate.Pool that
// launches one language server per (root, language) and reuses it across all
// agent sessions. This is the ADR 0017 endgame: one spine serves retrieval
// (search_context) AND type-resolved navigation, so Moe fuses a single source.
//
// The daemon must be built with `-tags lsp` (alongside `onnx` for the dense
// arm). Without the tag, nav_stub.go registers no navigation tools and the
// daemon serves search_context exactly as before.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"moedex/internal/mcp"
	"moedex/internal/navigate"
)

// navTools builds the navigation tools backed by one shared, long-lived Pool.
// The returned closer shuts the pool (and every spawned language server) down on
// daemon exit. Idle servers are reaped (IdleTTL) since the daemon is long-lived,
// and the live-server count is bounded (MaxServers) for a deeply polyglot corpus.
func navTools() ([]mcp.ToolHandler, func() error) {
	pool := navigate.NewPool(navigate.Config{
		IdleTTL:    10 * time.Minute,
		MaxServers: 24,
	})
	defs := []struct {
		name, desc string
		run        func(context.Context, *navigate.Pool, navigate.Pos, navArgs) ([]navigate.Location, error)
	}{
		{
			"find_definition",
			"Resolve the symbol at a file position to its declaration site(s) via a real language server — type-resolved and cross-file, unlike a text search. Returns location(s) as file:line:col. Languages: go, csharp, typescript/js, css/scss, cfml (definition only), python; others route when their server is installed.",
			func(ctx context.Context, p *navigate.Pool, at navigate.Pos, _ navArgs) ([]navigate.Location, error) {
				return p.Definition(ctx, at)
			},
		},
		{
			"find_references",
			"Find every reference to the symbol at a file position across the workspace (type-resolved). Returns locations as file:line:col. Empty for servers without a references provider (e.g. cfml, sql).",
			func(ctx context.Context, p *navigate.Pool, at navigate.Pos, a navArgs) ([]navigate.Location, error) {
				return p.References(ctx, at, a.includeDecl())
			},
		},
		{
			"find_implementations",
			"Find concrete implementations of the interface or interface method at a file position (type-resolved). Returns locations as file:line:col.",
			func(ctx context.Context, p *navigate.Pool, at navigate.Pos, _ navArgs) ([]navigate.Location, error) {
				return p.Implementations(ctx, at)
			},
		},
	}
	tools := make([]mcp.ToolHandler, 0, len(defs))
	for _, d := range defs {
		tools = append(tools, &navTool{name: d.name, desc: d.desc, pool: pool, run: d.run})
	}
	return tools, pool.Close
}

// navTool adapts one navigation verb to the mcp.ToolHandler interface.
type navTool struct {
	name string
	desc string
	pool *navigate.Pool
	run  func(context.Context, *navigate.Pool, navigate.Pos, navArgs) ([]navigate.Location, error)
}

func (t *navTool) Name() string { return t.name }

func (t *navTool) Descriptor() map[string]interface{} {
	props := map[string]interface{}{
		"file":   map[string]interface{}{"type": "string", "description": "Absolute path to the source file (must exist on the daemon host)."},
		"line":   map[string]interface{}{"type": "integer", "description": "1-based line number of the symbol."},
		"column": map[string]interface{}{"type": "integer", "description": "1-based byte column within the line (optional; default 1)."},
	}
	if t.name == "find_references" {
		props["include_declaration"] = map[string]interface{}{"type": "boolean", "description": "Include the declaration itself in the results (optional; default true)."}
	}
	return map[string]interface{}{
		"name":        t.name,
		"description": t.desc,
		"inputSchema": map[string]interface{}{
			"type":       "object",
			"properties": props,
			"required":   []string{"file", "line"},
		},
	}
}

// navArgs is the tools/call arguments object shared by all three verbs.
type navArgs struct {
	File               string `json:"file"`
	Line               int    `json:"line"`
	Column             int    `json:"column"`
	IncludeDeclaration *bool  `json:"include_declaration"`
}

func (a navArgs) includeDecl() bool { return a.IncludeDeclaration == nil || *a.IncludeDeclaration }

func (t *navTool) Call(ctx context.Context, raw json.RawMessage) (map[string]interface{}, error) {
	var a navArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return mcp.TextResult("invalid arguments: "+err.Error(), true), nil
	}
	if strings.TrimSpace(a.File) == "" || a.Line < 1 {
		return mcp.TextResult("file (absolute path) and line (1-based) are required", true), nil
	}
	col := a.Column
	if col < 1 {
		col = 1
	}
	locs, err := t.run(ctx, t.pool, navigate.Pos{File: a.File, Line: a.Line, Col: col}, a)
	if err != nil {
		return mcp.TextResult(fmt.Sprintf("%s failed: %v", t.name, err), true), nil
	}
	if len(locs) == 0 {
		return mcp.TextResult("no results", false), nil
	}
	var b strings.Builder
	for _, l := range locs {
		fmt.Fprintf(&b, "%s:%d:%d\n", l.File, l.Start.Line, l.Start.Col)
	}
	return mcp.TextResult(strings.TrimRight(b.String(), "\n"), false), nil
}
