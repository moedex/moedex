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
		run        func(context.Context, *navigate.Pool, navigate.Pos, navArgs) (navigate.LocationQueryResult, error)
	}{
		{
			"find_definition",
			"Resolve the symbol at a file position to its declaration site(s) via a real language server — type-resolved and cross-file, unlike a text search. Returns location(s) as file:line:col. Languages: go, csharp, typescript/js, css/scss, cfml (definition only), python; others route when their server is installed.",
			func(ctx context.Context, p *navigate.Pool, at navigate.Pos, _ navArgs) (navigate.LocationQueryResult, error) {
				return p.DefinitionDetailed(ctx, at)
			},
		},
		{
			"find_references",
			"Find every reference to the symbol at a file position across the workspace (type-resolved). Returns locations as file:line:col and a structured status. Servers without a references provider (e.g. cfml, sql) report unsupported rather than an ambiguous empty result.",
			func(ctx context.Context, p *navigate.Pool, at navigate.Pos, a navArgs) (navigate.LocationQueryResult, error) {
				return p.ReferencesDetailed(ctx, at, a.includeDecl())
			},
		},
		{
			"find_implementations",
			"Find concrete implementations of the interface or interface method at a file position (type-resolved). Returns locations as file:line:col.",
			func(ctx context.Context, p *navigate.Pool, at navigate.Pos, _ navArgs) (navigate.LocationQueryResult, error) {
				return p.ImplementationsDetailed(ctx, at)
			},
		},
	}
	tools := make([]mcp.ToolHandler, 0, len(defs)+2)
	for _, d := range defs {
		tools = append(tools, &navTool{name: d.name, desc: d.desc, pool: pool, run: d.run})
	}
	tools = append(tools, &findSymbolTool{pool: pool}, &symbolsOverviewTool{pool: pool})
	return tools, pool.Close
}

// navTool adapts one navigation verb to the mcp.ToolHandler interface.
type navTool struct {
	name string
	desc string
	pool *navigate.Pool
	run  func(context.Context, *navigate.Pool, navigate.Pos, navArgs) (navigate.LocationQueryResult, error)
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
	result, err := t.run(ctx, t.pool, navigate.Pos{File: a.File, Line: a.Line, Col: col}, a)
	return navLocationResult(result, err), nil
}

// navLocationResult renders the status-preserving position-query result. The
// text fallback stays location-only for resolved calls, preserving consumers
// of the original line format, while empty/unsupported/unavailable outcomes are
// now distinct in both text and structuredContent. In particular, ready_empty
// never claims the symbol is external; LSP null is not sufficient evidence for
// that conclusion.
func navLocationResult(result navigate.LocationQueryResult, err error) map[string]interface{} {
	status := result.Status
	if err != nil || status == "" {
		status = navigate.LocationQueryUnavailable
	}
	structured := map[string]interface{}{
		"status":    string(status),
		"locations": structuredLocations(result.Locations),
	}

	if err != nil {
		structured["message"] = err.Error()
		return mcp.StructuredResult("unavailable: "+err.Error(), structured, true)
	}

	switch status {
	case navigate.LocationQueryResolved:
		if len(result.Locations) == 0 {
			structured["status"] = string(navigate.LocationQueryReadyEmpty)
			return mcp.StructuredResult("ready_empty: language server returned no locations", structured, false)
		}
		var b strings.Builder
		for _, l := range result.Locations {
			fmt.Fprintf(&b, "%s:%d:%d\n", l.File, l.Start.Line, l.Start.Col)
		}
		return mcp.StructuredResult(strings.TrimRight(b.String(), "\n"), structured, false)
	case navigate.LocationQueryReadyEmpty:
		return mcp.StructuredResult("ready_empty: language server returned no locations", structured, false)
	case navigate.LocationQueryUnsupported:
		return mcp.StructuredResult("unsupported: language server does not implement this navigation method", structured, false)
	default:
		return mcp.StructuredResult("unavailable: navigation request did not produce an authoritative result", structured, true)
	}
}

func structuredLocations(locs []navigate.Location) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(locs))
	for _, loc := range locs {
		out = append(out, map[string]interface{}{
			"file": loc.File,
			"start": map[string]interface{}{
				"line":   loc.Start.Line,
				"column": loc.Start.Col,
			},
			"end": map[string]interface{}{
				"line":   loc.End.Line,
				"column": loc.End.Col,
			},
		})
	}
	return out
}

// symbolsResult renders a Symbol slice the ADR 0018 pinned way: one
// "name\tkind\tfile:line:col" line per Symbol (Symbol.String), or "no results"
// text for an empty slice — shared by both name-based tools below.
func symbolsResult(syms []navigate.Symbol) map[string]interface{} {
	if len(syms) == 0 {
		return mcp.TextResult("no results", false)
	}
	var b strings.Builder
	for _, s := range syms {
		b.WriteString(s.String())
		b.WriteByte('\n')
	}
	return mcp.TextResult(strings.TrimRight(b.String(), "\n"), false)
}

// findSymbolTool is the ADR 0018 Tool 1 — LSP workspace/symbol — a name-based,
// workspace-scoped lookup. Unlike the position tools it cannot route by a
// file's extension, so it routes by the caller-supplied root (and optional
// lang; see Pool.WorkspaceSymbol for the polyglot-merge behavior when lang is
// omitted).
type findSymbolTool struct {
	pool *navigate.Pool
}

func (t *findSymbolTool) Name() string { return "find_symbol" }

func (t *findSymbolTool) Descriptor() map[string]interface{} {
	return map[string]interface{}{
		"name":        "find_symbol",
		"description": "Find symbols by name across a workspace via a real language server (LSP workspace/symbol) — a fuzzy/substring match, not an exact resolver. Returns Symbol[] as name\\tkind\\tfile:line:col, one per line. Route by root; omit lang to merge every language server ALREADY live for that root (a cold root with no live server yet returns no results — warm it first with find_definition/symbols_overview, or pass lang to spawn it directly).",
		"inputSchema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "Symbol name to search for (fuzzy/substring, server-dependent)."},
				"root":  map[string]interface{}{"type": "string", "description": "Absolute path to the workspace root (must exist on the daemon host)."},
				"lang":  map[string]interface{}{"type": "string", "description": "Optional canonical language (e.g. \"go\", \"typescript\") to select one server; omitted merges every language already live for root."},
			},
			"required": []string{"query", "root"},
		},
	}
}

type findSymbolArgs struct {
	Query string `json:"query"`
	Root  string `json:"root"`
	Lang  string `json:"lang"`
}

func (t *findSymbolTool) Call(ctx context.Context, raw json.RawMessage) (map[string]interface{}, error) {
	var a findSymbolArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return mcp.TextResult("invalid arguments: "+err.Error(), true), nil
	}
	if strings.TrimSpace(a.Query) == "" || strings.TrimSpace(a.Root) == "" {
		return mcp.TextResult("query and root are required", true), nil
	}
	syms, err := t.pool.WorkspaceSymbol(ctx, a.Root, a.Lang, a.Query)
	if err != nil {
		return mcp.TextResult(fmt.Sprintf("find_symbol failed: %v", err), true), nil
	}
	return symbolsResult(syms), nil
}

// symbolsOverviewTool is the ADR 0018 Tool 2 — LSP textDocument/documentSymbol
// — a file-scoped enumeration. It routes exactly like the position tools (by
// the file's extension + enclosing root).
type symbolsOverviewTool struct {
	pool *navigate.Pool
}

func (t *symbolsOverviewTool) Name() string { return "symbols_overview" }

func (t *symbolsOverviewTool) Descriptor() map[string]interface{} {
	return map[string]interface{}{
		"name":        "symbols_overview",
		"description": "List every top-level and nested declaration in a file via a real language server (LSP textDocument/documentSymbol), flattened. Returns Symbol[] as name\\tkind\\tfile:line:col, one per line.",
		"inputSchema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file": map[string]interface{}{"type": "string", "description": "Absolute path to the source file (must exist on the daemon host)."},
			},
			"required": []string{"file"},
		},
	}
}

type symbolsOverviewArgs struct {
	File string `json:"file"`
}

func (t *symbolsOverviewTool) Call(ctx context.Context, raw json.RawMessage) (map[string]interface{}, error) {
	var a symbolsOverviewArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return mcp.TextResult("invalid arguments: "+err.Error(), true), nil
	}
	if strings.TrimSpace(a.File) == "" {
		return mcp.TextResult("file (absolute path) is required", true), nil
	}
	syms, err := t.pool.DocumentSymbol(ctx, a.File)
	if err != nil {
		return mcp.TextResult(fmt.Sprintf("symbols_overview failed: %v", err), true), nil
	}
	return symbolsResult(syms), nil
}
