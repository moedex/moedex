# Compact source reads and graph capability discovery

`list_repos` now reports `graph_available` for its acquired snapshot. Agents can
avoid graph traversal when the graph is absent while continuing source discovery;
the flag does not describe compiler or LSP support. `read_source` now accepts
`format="structured"` to return a compact text summary with the same structured
source bytes, line ranges and provenance as its default numbered presentation.

See [ADR0062](../../docs/adr/0062-source-discovery-capabilities.md) and the
[regression manifest](results/compact-discovery-20261001/result.json).

Full Go tests, vet and focused MCP/source-tool race checks pass. Capability tests
cover source-only operation, loading a graph on reload, failed reload retention
and empty filtered repository results. Source tests cover presentation parity,
invalid formats and safe errors for reversed or extremely large line ranges.

Seven public HTTP calls against the pinned Outbox snapshot verify orientation,
both source presentations, a narrow read, expected graph-unavailable behavior,
successful compiler discovery and a reversed-range argument error. The whole
Worker source response shrinks from **8,346 to 4,292 serialized bytes (48.57%)**
when structured mode is selected. Structured content and metadata are identical.
The line-62 compact read is 746 bytes. No source or semantic recapture was needed.

These are scripted development checks. The completed independent evaluation and
its 66/67 correctness, 64.5/67 evidence and 7/12 strict successes remain unchanged.
No new agent, heldout, latency, GPU or competitive result is claimed. The public
catalog retains its full schemas; oversized catalog displays and large compiler
responses still need client-side selection or bounded presentation. Compact mode
does not remove host display limits or change source-token estimates.
