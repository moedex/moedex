# 0065: Bounded compact semantic artifact envelopes

- Status: accepted, 2026-10-02

## Context

Worker21 can extract Quartz completely, but the legacy base64 JSON artifact
exceeded the 64 MiB file budget. Most of that payload is repetitive compiler
records. Increasing the file budget alone would inflate stored evidence and
would not distinguish transport, storage and expansion costs.

## Decision

Keep the 64 MiB encoded-file limit. Write payloads larger than 64 KiB in a
version-2 JSON envelope containing deterministic gzip bytes, an explicit expanded
byte count, encoding name and SHA-256 of the exact expanded JSON. Smaller writes
retain the legacy envelope. Bound expanded JSON to 256 MiB and allow read callers
to lower that limit. Reject false lengths, corrupt checksums, digest mismatches,
unknown formats/encodings, truncated streams and trailing compressed data,
including concatenated gzip members. Verify semantic identities and references
after decoding. Preserve atomic exclusive publication.

Envelope version is separate from semantic payload schema version, which remains
1. Legacy artifacts stay readable. Source/input and worker-transport budgets are
unchanged. Composition retains its 64 MiB canonical legacy-envelope aggregate
budget and checks expanded input accounting before loading another artifact.

## Consequences

Old binaries cannot read version-2 envelopes. Expanded JSON and Go objects are
materialized; these bounds are not process heap ceilings. Compression adds CPU
work. Large single captures can publish, but composition is deliberately still
conservative. Warm serving continues to use the existing compact semantic index.

## Evidence

[Acceptance report](../../research/semantic-intelligence/COMPACT-ARTIFACT-ACCEPTANCE.md):
Quartz capture, inspect, publication and native MCP succeed; deterministic,
legacy, corruption, expansion and composition controls pass. The same-payload
legacy envelope is 225,618,473 bytes; the compact file is 56,658,037 bytes.

## Related

[0064: Project-built analyzer preparation](0064-project-built-analyzer-preparation.md).
