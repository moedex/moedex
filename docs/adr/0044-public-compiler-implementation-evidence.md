# ADR 0044: Public compiler implementation evidence

**Status:** Accepted

**Date:** 2026-10-01

## Context

Worker6 and semantic index v4 already retain exact interface-method implementation
facts. The Webhooks holdout found three correct native relationships that a caller
could not retrieve through `compiler_binding_at` or `compiler_definitions`.
`compiler_contract_paths` exposes this proof only within a candidate path rooted
at a direct publisher. A general implementation declaration need not publish.

## Decision

Expose existing implementation facts as optional fields on `CompilerBinding`.
`implementation_facts` contains the recorded kind, rule, compile-time scope,
interface symbol ID, and implementing-type symbol ID. A deduplicated
`implementation_symbols` table provides their full qualified identities.
The binding's existing symbol and source/context fields identify the implementation
method and its declaration.

Validate relationships and referenced identities before returning public evidence.
Malformed or incomplete reader results fail closed. Do not synthesize facts at
invocations or infer runtime dispatch. Bindings without implementation facts omit
both fields; older index reads remain supported.

Use the existing index materialization accounting, which already charges the
bounded fact payload and referenced symbols. Deduplicate shared types rather than
expanding full interface/type identities repeatedly in every fact. No new worker,
index format, capture, or MCP tool is required.

## Consequences

An agent can inspect a known method declaration or definition and retrieve its
recorded interface correspondence independently of a messaging path. This does not
provide reverse discovery from an interface method to unknown implementations:
the existing implementation posting is keyed by implementing method, so that
capability requires separate bounded query/index work.

The supported implementation subset remains unchanged. Generic, inherited,
default/static-interface cases are not made supported by adding response fields.
Public representation also does not establish runtime DI selection or deployment
closure. Existing wrapper-path evidence remains a static candidate relationship.

## Evidence

The first [Webhooks holdout](../../research/semantic-intelligence/WEBHOOKS-HOLDOUT-ACCEPTANCE.md)
preserves nine native successes, six publicly exposed assertions, and three
declaration-only results. A separate public projection replay uses the same
artifact and frozen labels; it is a successor regression gate, not another first
holdout. Focused malformed-reader, deduplication, legacy, and budget tests accompany
the public conversion change.

## Related

- [ADR 0043](0043-static-wrapper-candidate-paths.md): static wrapper candidate paths.
