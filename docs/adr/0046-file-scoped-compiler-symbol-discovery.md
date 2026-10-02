# ADR 0046: File-scoped compiler symbol discovery

Status: accepted, 2026-10-01.

## Problem

Public compiler queries require exact symbol identities. Source discovery previously
required full-file retrieval, context discovery, raw-hash verification (including
possible UTF-8 BOM removal during ingestion), byte-offset calculation, binding
lookup and definition lookup. The scripted Outbox interface workflows took eleven
calls to reach implementation evidence in two compiler contexts.

## Decision

Add `compiler_symbols(repo, path, query?, limit?)`. The caller supplies an exact
source path discovered through public search or tree tools. A case-sensitive
substring over recorded compiler descriptors selects declaration alternatives.
The string query is a filter, never a symbol identity or dispatch assertion.

Seek the existing position directory by repository/path, scan at most 10,000
occurrences, and materialize at most 100 resolved declarations within a conservative
2 MiB budget including context projections. References and nonmatches consume
scan work. Byte/result/work limits yield explicit truncation. An empty truncated
response must not be treated as absence. There is no pagination in this increment;
exact binding lookup remains the fallback for declarations beyond the scan bound.
No corpus-wide symbol scan, new sidecar, index format change or compiler recapture.

Return exact IDs, raw source spans/hashes, snapshot/artifact identity, and separate
context variants. Context choices describe returned matches only. Multiple
variants do not imply one compatible build. Downstream contract/implementation
queries retain their explicit context-selection requirements.

## Consequences

Declaration discovery no longer needs source-text normalization or manual offsets.
The source-offset workflow remains necessary for reference tokens. No recorded
declaration can mean absent capture or no matching declaration; binding context
discovery distinguishes coverage. File scope bounds lookup work but a very large
file can truncate before a late declaration even with a selective query.

Validation includes scope isolation, references excluded, preserved context
alternatives, deterministic output, result/work/materialization bounds, cancellation,
closed mappings, lease release, schema validation, and native public workflows.
Development task walkthroughs and scripted workflow savings are not independent
agent scores or a CodeGraph competitive result.
