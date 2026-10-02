# ADR 0050: Compact class evidence without implicit dispatch

Status: accepted. Date: 2026-10-01.

## Problem

Worker9 records class-selected default forwarding, but an agent must separately
fetch the class, target method, template, source call and registration evidence.
Separate calls repeat descriptors and can cross serving generations.

## Decision

Add `compiler_evidence_path` using one immutable compiler session. Start from an
exact type symbol and explicit context IDs. Read class definitions and framework
postings naming that identity, then follow only recorded forwarding locators to
target definitions, template definitions and the exact call occurrence. Retain
source hashes, offsets, contexts, rules, cast requirements and distinct IDs.
Normalize symbol descriptors across the response.

The index traversal shares a 10,000-row budget, 2 MiB materialization budget and
1–100 record limit. Skipped definition rows count against work. Missing witness
contexts are returned as required choices, never silently selected. No recursive
traversal, runtime dispatch edge or dependency-closure compatibility is inferred.

Cap the serialized tool-result envelope at 64 KiB by removing complete records and
unused descriptors together and reporting truncation. Transport framing is not
included in that limit. The response may contain referenced identities whose
source records were truncated; callers must inspect truncation and required scopes.

## Consequences

No artifact or index-format change; existing v5 evidence is reused. Stable catalog
size increases from seven to eight compiler tools. Existing contract-path hop
semantics remain unchanged. The bounded response improves a paired eShop evidence
retrieval subtask from five calls to one, with 34.34% fewer bytes. It does not
measure discovery or independent agent task completion.

See [acceptance](../../research/semantic-intelligence/COMPACT-EVIDENCE-ACCEPTANCE.md)
for fresh-corpus failures, public comparisons, validation and remaining work.
