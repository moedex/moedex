package mcp

// ToolSpecification is Moedex's complete, process-stable contract for one MCP
// tool. Keeping the handler beside both schemas prevents the catalog and the
// implementation from drifting independently.
type ToolSpecification struct {
	Name         string
	Description  string
	InputSchema  map[string]interface{}
	OutputSchema map[string]interface{}
}

// NewToolSpecification completes a tool contract with the standard Moedex
// output schema for that tool. All tools are read-only, idempotent, and operate
// only on the locally configured corpus/workspace.
func NewToolSpecification(name, description string, input map[string]interface{}) ToolSpecification {
	return ToolSpecification{
		Name:         name,
		Description:  description,
		InputSchema:  input,
		OutputSchema: OutputSchema(name),
	}
}

func objectSchema(required []string, properties map[string]interface{}) map[string]interface{} {
	s := map[string]interface{}{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func arrayOf(items map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"type": "array", "items": items}
}

func stringSchema() map[string]interface{} { return map[string]interface{}{"type": "string"} }
func intSchema() map[string]interface{}    { return map[string]interface{}{"type": "integer"} }
func numberSchema() map[string]interface{} { return map[string]interface{}{"type": "number"} }
func boolSchema() map[string]interface{}   { return map[string]interface{}{"type": "boolean"} }

func standardErrorSchema() map[string]interface{} {
	return objectSchema([]string{"error"}, map[string]interface{}{"error": errorPropertySchema()})
}

func errorPropertySchema() map[string]interface{} {
	return objectSchema([]string{"code", "message"}, map[string]interface{}{
		"code": stringSchema(), "message": stringSchema(),
	})
}

func locationSchema() map[string]interface{} {
	position := objectSchema([]string{"line", "column"}, map[string]interface{}{
		"line": intSchema(), "column": intSchema(),
	})
	return objectSchema([]string{"file", "start", "end"}, map[string]interface{}{
		"file": stringSchema(), "blob_sha": stringSchema(), "start": position, "end": position,
	})
}

func symbolSchema() map[string]interface{} {
	return objectSchema([]string{"name", "kind", "location"}, map[string]interface{}{
		"name": stringSchema(), "kind": stringSchema(), "location": locationSchema(),
	})
}

func confidenceSchema() map[string]interface{} {
	return objectSchema([]string{"tier", "score"}, map[string]interface{}{
		"tier":  map[string]interface{}{"type": "string", "enum": []string{"Candidate", "Pattern", "Verified", "Proven"}},
		"score": numberSchema(),
	})
}

func graphLocationSchema() map[string]interface{} {
	return objectSchema(nil, map[string]interface{}{
		"repo": stringSchema(), "path": stringSchema(), "abs_path": stringSchema(), "line": intSchema(), "blob_sha": stringSchema(),
	})
}

func graphNodeSchema() map[string]interface{} {
	return objectSchema([]string{"id", "blob_sha", "symbol_offset", "confidence", "hops", "locations"}, map[string]interface{}{
		"id": stringSchema(), "symbol": stringSchema(), "kind": stringSchema(), "blob_sha": stringSchema(),
		"symbol_offset": intSchema(), "confidence": confidenceSchema(), "hops": intSchema(),
		"locations": arrayOf(graphLocationSchema()),
	})
}

func evidenceSchema() map[string]interface{} {
	return objectSchema([]string{"blob_sha", "byte_offset", "byte_length"}, map[string]interface{}{
		"blob_sha": stringSchema(), "byte_offset": intSchema(), "byte_length": intSchema(),
	})
}

func graphEdgeSchema() map[string]interface{} {
	return objectSchema([]string{"source", "target", "type", "confidence", "evidence"}, map[string]interface{}{
		"source": stringSchema(), "target": stringSchema(), "type": stringSchema(),
		"confidence": confidenceSchema(), "evidence": evidenceSchema(), "similarity": numberSchema(),
	})
}

func graphResultSchema() map[string]interface{} {
	return objectSchema([]string{"tool", "query", "depth", "nodes", "edges"}, map[string]interface{}{
		"tool": stringSchema(), "query": stringSchema(), "depth": intSchema(),
		"nodes": arrayOf(graphNodeSchema()), "edges": arrayOf(graphEdgeSchema()),
	})
}

func contextResultSchema() map[string]interface{} {
	summary := objectSchema([]string{"blocks", "token_estimate", "truncated", "clipped"}, map[string]interface{}{
		"blocks": intSchema(), "token_estimate": intSchema(), "truncated": boolSchema(), "clipped": boolSchema(),
	})
	block := objectSchema([]string{"blob", "blob_sha", "repo", "rel_path", "abs_path", "start_line", "end_line", "score", "lexical", "dense", "text", "clipped"}, map[string]interface{}{
		"blob": intSchema(), "blob_sha": stringSchema(), "repo": stringSchema(), "path_with_namespace": stringSchema(),
		"rel_path": stringSchema(), "abs_path": stringSchema(), "start_line": intSchema(), "end_line": intSchema(),
		"score": numberSchema(), "lexical": numberSchema(), "dense": numberSchema(), "text": stringSchema(), "clipped": boolSchema(),
		// Neighbor payloads are stable objects but intentionally remain extensible
		// as graph relationship lanes evolve.
		"neighbors": map[string]interface{}{"type": "object"},
	})
	return objectSchema([]string{"summary", "blocks"}, map[string]interface{}{
		"summary": summary, "blocks": arrayOf(block),
	})
}

func navigationResultSchema() map[string]interface{} {
	return objectSchema([]string{"status", "locations"}, map[string]interface{}{
		"status":    map[string]interface{}{"type": "string", "enum": []string{"resolved", "ready_empty", "unsupported", "unavailable"}},
		"locations": arrayOf(locationSchema()), "message": stringSchema(),
	})
}

func symbolsResultSchema() map[string]interface{} {
	return objectSchema([]string{"status", "symbols"}, map[string]interface{}{
		"status":  map[string]interface{}{"type": "string", "enum": []string{"resolved", "ready_empty", "unavailable"}},
		"symbols": arrayOf(symbolSchema()), "message": stringSchema(),
	})
}

func clusterResultSchema() map[string]interface{} {
	// Host tool-schema adapters commonly reject root-level composition keywords.
	// Advertise the property superset of the summary, detail, and unavailable
	// variants; status/available remain the wire discriminators, and handlers
	// retain strict runtime validation.
	properties := map[string]interface{}{
		"available": boolSchema(), "status": stringSchema(), "graph_generation": intSchema(),
		"observed_nodes": intSchema(), "observed_edges": intSchema(), "eligible_nodes": intSchema(),
		"eligible_edges": intSchema(), "cap": intSchema(), "guidance": stringSchema(),
		"generation": intSchema(), "offset": intSchema(), "limit": intSchema(), "total": intSchema(),
		"total_members": intSchema(),
		"clusters": arrayOf(objectSchema([]string{"cluster_id", "label", "member_count"}, map[string]interface{}{
			"cluster_id": intSchema(), "label": stringSchema(), "member_count": intSchema(),
		})),
		// Cluster members are a persisted graph payload that remains extensible.
		"cluster": map[string]interface{}{"type": "object"},
	}
	return objectSchema(nil, properties)
}

func discoveryOutputSchema(name string) map[string]interface{} {
	switch name {
	case "list_repos":
		return objectSchema([]string{"repos", "total"}, map[string]interface{}{
			"repos": arrayOf(objectSchema([]string{"name", "files"}, map[string]interface{}{"name": stringSchema(), "files": intSchema()})),
			"total": intSchema(),
		})
	case "graph_schema":
		return objectSchema([]string{"generation", "corpus_fingerprint", "build_id", "total_nodes", "total_edges", "node_kinds", "edge_types"}, map[string]interface{}{
			"generation": intSchema(), "corpus_fingerprint": stringSchema(), "build_id": stringSchema(),
			"total_nodes": intSchema(), "total_edges": intSchema(),
			"node_kinds": map[string]interface{}{"type": "object", "additionalProperties": intSchema()},
			"edge_types": map[string]interface{}{"type": "object", "additionalProperties": intSchema()},
		})
	case "read_source":
		return objectSchema([]string{"repo", "path", "blob_sha", "lines", "start_line", "end_line", "truncated", "content"}, map[string]interface{}{
			"repo": stringSchema(), "path": stringSchema(), "blob_sha": stringSchema(), "lines": intSchema(),
			"start_line": intSchema(), "end_line": intSchema(), "truncated": boolSchema(), "content": stringSchema(),
		})
	case "list_symbols":
		entry := objectSchema([]string{"name", "kind", "blob_sha"}, map[string]interface{}{
			"name": stringSchema(), "kind": stringSchema(), "repo": stringSchema(), "path": stringSchema(), "line": intSchema(), "blob_sha": stringSchema(),
		})
		return objectSchema([]string{"symbols", "total", "truncated"}, map[string]interface{}{
			"symbols": arrayOf(entry), "total": intSchema(), "truncated": boolSchema(),
		})
	case "file_tree":
		return objectSchema([]string{"repo", "files", "total", "truncated"}, map[string]interface{}{
			"repo": stringSchema(), "prefix": stringSchema(), "files": arrayOf(stringSchema()), "total": intSchema(), "truncated": boolSchema(),
		})
	}
	return map[string]interface{}{"type": "object"}
}

// OutputSchema returns the advertised structuredContent schema for a named
// tool. The root is deliberately a single closed object: several MCP hosts
// reject an entire catalog when a tool schema has root-level anyOf/oneOf. The
// normal success properties and standard error property are therefore optional
// at the schema root; handlers and contract fixtures enforce the concrete wire
// variants.
func OutputSchema(name string) map[string]interface{} {
	var normal map[string]interface{}
	switch name {
	case "search_context":
		normal = contextResultSchema()
	case "trace_calls", "trace_consumers", "trace_hierarchy", "trace_queries", "trace_renders", "impact_analysis", "graph_neighbors":
		normal = graphResultSchema()
	case "list_clusters":
		normal = clusterResultSchema()
	case "find_definition", "find_references", "find_implementations":
		normal = navigationResultSchema()
	case "find_symbol", "symbols_overview":
		normal = symbolsResultSchema()
	default:
		normal = discoveryOutputSchema(name)
	}
	properties := make(map[string]interface{})
	if normalProperties, ok := normal["properties"].(map[string]interface{}); ok {
		for key, value := range normalProperties {
			properties[key] = value
		}
	}
	properties["error"] = errorPropertySchema()
	return objectSchema(nil, properties)
}
