# ADR 0041: Compiler context-registration evidence

Date: 2026-10-01
Status: Accepted

## Context

The external Outbox sample configures EF contexts through `AddDbContext<T>()`.
Those calls resolve successfully but earlier framework rules do not represent
them. Their options callbacks are not DI service factories, and their registration
lifetime is independent of the options lifetime.

## Decision

Worker 4 emits `storage_context_registration` under `csharp-framework-v3` only
for the exact metadata overload taking `IServiceCollection`,
`Action<DbContextOptionsBuilder>`, and two `ServiceLifetime` parameters in
`EntityFrameworkServiceCollectionExtensions` from `Microsoft.EntityFrameworkCore`.
The single concrete named context type supplies both service and implementation
targets. Go artifact/index validation requires these targets to agree.

Read the `contextLifetime` argument from Roslyn's invocation operation, including
the compiler-provided optional default. Require its parameter type to be the
metadata DI `ServiceLifetime` enum and its value to be a known constant. Emit
`singleton`, `scoped`, or `transient` accordingly. A nonconstant or invalid context
lifetime yields no domain fact. Do not infer anything from options lifetime or
callback contents. Other overloads, factory/pooling APIs, unresolved calls,
generic targets, and local lookalikes remain excluded.

This is an observed registration call, not proof that it wins over other
registrations, executes, or activates a service. No database provider, schema,
table, or runtime dependency is inferred. Earlier v1/v2 rules retain their
semantics under worker 4; v3 facts reject older worker provenance. Existing
artifact/index formats remain unchanged.

## Validation

Preserve all previous gold files. A source-authored successor promotes only the
two context-registration sites before capture. Their omitted lifetime arguments
are independently grounded in the [pinned EF 8.0.4 declaration](https://github.com/dotnet/efcore/blob/v8.0.4/src/EFCore/Extensions/EntityFrameworkServiceCollectionExtensions.cs).
Real metadata fixtures distinguish context and options lifetimes, supported
constant values, alternate overloads, dynamic values, and lookalikes. Artifact
and mapped-index regressions cover rule/version mismatch and conflicting targets.
