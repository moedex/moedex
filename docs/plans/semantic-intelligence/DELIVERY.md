# Semantic intelligence delivery log

## Offline dependency capture and bounded graph selectors

Graph call tracing accepts exact repository and path selectors. Journey clients
share a portable monotonic clock. Compiler capture uses the verified staged
package bundle as a local NuGet feed, allowing floating versions to resolve
without ambient package sources. Synthetic regression tests cover unavailable
package versions and preserve bundle/source verification.

Earlier compiler work adds capture isolation, context-aware semantic indexes,
source-linked observations and bounded MCP retrieval. Public source fixtures and
colocated tests document those implementation contracts. Corpus-specific
measurements and operational evidence are maintained separately from this code.

## Context-selected compiler call tracing

`compiler_trace_calls` traverses resolved named-method invocations under explicit
recorded compiler contexts. Semantic index v6 stores outbound postings; older
indexes remain readable and require an upgrade for tracing. Bounded breadth-first
queries retain source witnesses, static targets and provenance. Colocated tests
and the public SDK fixture cover homonyms, cycles, direction, overloads and
lambda/constructor boundaries. Runtime dispatch and execution remain unproven.
