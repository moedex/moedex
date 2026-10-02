# ADR 0062: Graph availability and compact source reads

Status: accepted, 2026-10-01.

## Context

The worker18 development evaluation included a graph traversal request against a
snapshot without a graph sidecar. Source and captured compiler tools were usable,
but repository orientation did not explicitly distinguish graph availability.
Two solvers also reported truncated response displays. Numbered source text is
useful for citations, but duplicating it alongside structured source costs bytes.

## Decision

`list_repos` includes a required boolean `graph_available`, read from the same
acquired immutable snapshot as its repository results. It describes whether a
graph is loaded, not whether a query has matches or every repository is covered.
An empty repository filter result does not change this flag. Source-only tools
report false even when an unloaded graph exists on disk. Successful reloads can
change the flag; failed reloads retain the previous state. Cross-call consumers
must continue comparing snapshot identity, since this flag is not a future lease.

Tool descriptions direct agents to skip graph-dependent operations when false.
The field makes no claim about compiler or LSP capability. Existing tool names
remain discoverable, and graph calls still return explicit `graph_unavailable`
errors when appropriate. This avoids changing the catalog on each reload.

`read_source` accepts optional `format: text|structured`. The default retains
numbered source presentation. Structured mode replaces only the readable
fallback with a short location/truncation summary; structured source and snapshot
metadata are identical. Clients reading structured results can use this mode,
as already supported by `search_context`, to avoid duplicated source text.

The touched range path also rejects reversed positive line ranges and bounds
large start-line inputs before addition, preventing slice panics and integer
overflow. File/range selection and the existing 500-line cap otherwise remain.

## Verification and limits

Tests cover source-only orientation, graph activation on reload, retention after
failed reload, empty filters, compact/default source and provenance equality,
invalid formats and reversed/extreme ranges. Full Go tests, vet and focused race
checks pass. A public HTTP regression verifies a graph-absent snapshot with usable
compiler discovery and measures response bytes for both presentations.

On one pinned Worker source read, compact presentation reduces serialized response
size from 8,346 to 4,292 bytes (48.57%). This is a fixture measurement, not a general
compression guarantee or an independent agent-quality improvement. Catalog size,
compiler response rendering and host display limits are unchanged. Prior frozen
evaluations remain immutable.
