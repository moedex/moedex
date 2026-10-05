# Bounded catalog and response views

2026-10-02. Scripted usability regression; no new agent-quality score.

The new journey `browse.py` offers a tool-name index, complete individual tool
descriptors and JSON Pointer/offset pages over recorded responses. It delegates
new calls to the unchanged frozen accounting client. Default output is capped at
8,192 bytes, including its envelope. Pages carry raw SHA-256, raw byte count,
selection scope, explicit partial status and a continuation offset. Original
schemas, source, provenance and response envelopes remain retained and accessible.

Validation:

- All 17 journey unit tests pass, including Unicode/escaping reconstruction,
  pointer validation, complete schemas and full crossing-response accounting.
- All 74 responses from the prior evaluation (605,020 raw bytes) reconstruct
  exactly through bounded pages. All 22 catalog descriptors reconstruct exactly.
- The 262,642-byte catalog has an 881-byte names-only view. This is navigation,
  not equivalent evidence compression: descriptions and schemas require opening
  their complete descriptors.
- A live CPU-only MCP call returns 13,265 bytes of compiler-symbol evidence.
  The browser pages it without display overflow, reconstructs the full result,
  and resolves a selected status field. Accounting remains one call and 13,265
  response bytes after local rereads. The temporary server was stopped.

No Go production code changed; Python unit tests, complete archived-response
replay and live wrapper integration cover this increment. Existing server checks
remain recorded in the compact-discovery acceptance report. The old client,
archives and independent scores are unchanged. We do not infer an agent win,
reduced transport cost, known model token consumption, or full inspection from
successful paging. Future trial instructions must explicitly require opening
relevant schemas, interpreting partial views and inspecting cited evidence.

See [browser instructions](agent-journeys/README.md#bounded-browser-for-future-runs)
and validation archive (archival evidence maintained separately).
