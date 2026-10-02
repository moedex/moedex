# ADR 0061: Number source lines in MCP text presentation

Status: accepted, 2026-10-01.

## Context

The completed worker18 development evaluation found correct registration facts
with off-by-one citations and unnecessarily broad configuration citations.
`search_context` and `read_source` already return line ranges, but their readable
source excerpts require agents to count lines, including blank lines, manually.
Structured text is also used to verify hashes and derive raw compiler offsets.

## Decision

Number each displayed source line as `62 | source text` in the default
`search_context` text fallback and in `read_source` text content. Source reads
also show the resolved repository, path and returned range. Keep structured
`blocks[].text` and `content` unchanged, with their existing coordinates and
provenance. `search_context(format="structured")` keeps its compact summary
fallback and unnumbered structured blocks.

Render blank lines explicitly. Handle a terminal newline according to the
advertised range: do not invent an extra line for a search block, but preserve an
explicitly requested trailing empty line in a source read. CRLF is normalized
only in presentation; structured bytes remain intact. Do not infer numbering
when a custom provider supplies invalid or inconsistent coordinates.

Tool descriptions and server instructions direct agents to cite relevant lines,
verify narrow ranges, and use unnumbered structured text for hash/offset work.
The source-context token estimate does not include presentation or protocol
overhead; the tool description now states this explicitly.

## Consequences and verification

Text fallback is presentation, so clients requiring original indexed source must
read structured content. No structured schema, source hash, ranking, compiler
fact or worker version changes. Numbering increases text-response bytes in
proportion to returned lines; it does not solve oversized catalog/response
displays or prove improved independent-agent scores.

Tests cover blank lines before registrations, narrow reads, Unicode, CRLF,
terminal empty lines, clipped source, invalid provider ranges and structured
byte preservation. A separate public HTTP regression on the pinned Outbox
snapshot verifies registration lines 62 and 77 and BOM-sensitive hash input.
The original evaluation and its frozen binary remain unchanged.
