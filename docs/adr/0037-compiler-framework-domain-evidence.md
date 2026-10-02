# ADR 0037: Compiler-backed framework domain evidence

Status: Accepted; implementation and independent real-package source-to-tool gate pass.

## Context

Syntax matches for publish, consumer, registration, and storage names do not
establish their framework bindings or generic target types. The compiler lookup
pipeline already retains qualified API symbols, source spans, build contexts,
and incomplete-binding outcomes. Its normalization to original definitions
intentionally loses constructed generic arguments, which domain adapters need.

## Decision

Worker version 2 attaches optional domain facts to existing resolved reference
occurrences in complete captured contexts. Rule `csharp-framework-v1` recognizes
an explicit allowlist of framework metadata types and assemblies: Microsoft DI
type registrations, MassTransit publishes and consumer interfaces, and EF Core
entity-set properties and constant table mappings. Targets retain qualified type
identities, and the containing binding supplies API, source, owner, and compiler
evidence. No new synthetic source occurrences are required.

The first rule accepts nongeneric named targets; constructed/open generic targets
are omitted because normalizing them would erase distinctions. Factory/instance
DI overloads, private messaging wrappers, runtime table names, and unrelated
lookalike APIs are unsupported. Ordinary compiler bindings remain available when
no domain fact is emitted. Absence of a fact is not evidence of absent behavior.

Facts explicitly describe compile-time observations. They do not prove runtime
activation, dispatch, delivery, database contents, or a configured service path.
Framework assembly identity is compiler evidence backed by captured metadata
digests, not a package-publisher authenticity guarantee.

The audit artifact adds optional identity-bound fields. Legacy bindings without
facts retain their existing IDs. The compact semantic index gains a versioned,
bounded optional fact table while retaining reads of v1 indexes. Existing
`compiler_binding_at` responses expose facts with expanded qualified targets;
callers can use `compiler_definitions` to locate captured target declarations.
No syntax graph edge is promoted or rewritten by this slice.

## Validation

Require independent source anchors against actual pinned framework metadata,
positive and distractor/unsupported cases, actual worker → importer → persisted
index → MCP checks, and explicit incomplete-context rejection. Validate domain
shape, framework API, target cross-references, source scope, size budgets, format
compatibility, and corruption handling. Keep package/test artifacts outside the
repository. Existing compiler worker regression gates must continue to pass.

## Related

- [ADR 0027: semantic artifacts](0027-compiler-worker-and-semantic-artifact.md)
- [ADR 0029: recorded compiler lookup](0029-compiler-lookup-index-and-tools.md)
- [Semantic delivery program](../plans/semantic-intelligence/PROGRAM.md)

- [Domain acceptance](../../research/semantic-intelligence/DOMAIN-ACCEPTANCE.md)
