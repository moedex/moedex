# Compiler-backed framework domain evidence

Recorded October 1, 2026. Locally implemented and verified; not deployed.
See [ADR 0037](../../docs/adr/0037-compiler-framework-domain-evidence.md).

## Delivered behavior

The C# compiler worker now attaches versioned `domain_facts` to supported
resolved references in complete captured contexts. Existing
`compiler_binding_at` results carry these facts through the audit artifact and
compact index, with qualified target descriptors and the original raw source
span, build context, bound framework API, enclosing symbol, and compiler version.
Targets can be followed through `compiler_definitions`.

| Observation | Supported first rule |
| --- | --- |
| DI registration | Microsoft DI `AddSingleton`, `AddScoped`, `AddTransient` type-only generic overloads; service, implementation, and lifetime |
| Message publish | MassTransit `IPublishEndpoint.Publish<T>`; explicit or inferred nongeneric named message type |
| Message consumer | A class explicitly implementing MassTransit `IConsumer<T>`; contract type and declaring class |
| Storage entity | EF Core `DbSet<T>` property; entity type |
| Table mapping | Generic EF Core `ToTable<T>` with constant nonempty name and optional constant schema; entity and mapping values |

Recognition uses bound metadata types and assembly identities, not matching
method spellings. The rule is `csharp-framework-v1`, the worker extractor version
is 2, and evidence scope is explicitly `compile_time`. Captured assembly digests
bind the metadata used; this is not package-publisher authentication.

The API interpretation follows the framework contracts documented by
[Microsoft DI](https://learn.microsoft.com/en-us/aspnet/core/fundamentals/dependency-injection?view=aspnetcore-10.0),
[MassTransit messages](https://masstransit.io/architecture/encrypted-messages.html),
and [EF Core DbSet](https://learn.microsoft.com/en-us/dotnet/api/microsoft.entityframeworkcore.dbset-1?view=efcore-10.0).
The executable gate below uses pinned real metadata rather than relying on these
descriptions or synthetic framework stubs.

## Independent source-to-tool gate

The independently authored [fixture and labels](../../internal/eval/testdata/semantic-intelligence/domain-v1/gold.json)
use .NET SDK 10.0.100, real DI reference assemblies from the ASP.NET Core 10.0.0
framework pack, MassTransit.Abstractions 8.3.6, and EF Core Relational 10.0.0.
Five restored public package archives have recorded sizes and SHA-256 hashes.
Compilation and restore use disposable projections outside the repository.

| Gate | Result |
| --- | ---: |
| Worker source anchors | 26 / 26 |
| Expected positive domain facts | 10 / 10 |
| Complete-context exclusions | 14 / 14 |
| Incomplete-context exclusions | 2 / 2 |
| Complete-context source → artifact round-trip → index reopen → MCP anchors | 24 / 24 |
| Distinct target types navigated to captured declarations | 3 / 3 |
| Forged real-stream variants rejected | 4 / 4 |

The emitted fact total is checked globally, not just at positive anchors, to
exclude extra unlabelled positives. Every complete-context anchor passes through
the production MCP handler with exact typed targets, scalar values, source hash,
token-start offset, context, and extractor version. Structured response hashes
are recorded. This is a real index with a test session provider, not a deployed
daemon or a live snapshot-publication trial. Existing snapshot and server tests
cover attachment compatibility and serving lifetimes separately.

The gate caught an ownership bug: Roslyn's generic enclosing-symbol lookup at a
consumer base-list token returned its namespace. The worker now records the
semantically declared consumer class for that fact, and the independent gate
requires it. An incomplete capture remains importable as incomplete audit
evidence, emits no domain positives, and cannot build a serving index.

## Compatibility, bounds, and validation

Optional facts participate in binding identity; absent facts retain legacy
binding JSON and IDs. Index v2 adds a sorted directory to bounded canonical fact
payloads, preserving direct source-position lookup and v1 index reads. Lookup
returns owned strings and expanded target identities under the existing 2 MiB
result-materialization budget. The audit artifact remains limited to 64 MiB and
the compact index to 128 MiB; each domain payload is limited to 64 KiB, with at
most eight facts and two targets per fact. These are representation/materialization
bounds, not a process RSS or serialized-response ceiling.

Admission and mapped-index validation check rule, scope, compiler provenance,
supported API signatures, concrete target types, cross-record references, scalar
bounds, and canonical payload structure. Tests reject corrupt domain directories,
wrong framework APIs, invalid targets, runtime-scope claims, oversized results,
and unsupported factory signatures. Concurrent-reader/race checks pass.

Full `go test ./...`, `go vet ./...`, and `go build ./...` pass. Race checks pass
for semantic artifacts/import/index, MCP, snapshots, and serving; tagged LSP
compiler-tool checks pass. The real source-to-tool gate also passes under race
detection. The existing comprehensive worker integration suite passes all 19
original labels plus configuration, context, UTF-8/BOM, constructor, generator,
incomplete-input, escape, and mutation checks. That suite ran the initial domain
implementation; the final scalar guards and consumer-owner correction are covered
by the final real-package gate.

## Recorded evidence

The [machine-readable result](results/domain-20261001/result.json) records worker,
source, gold, package, and final source fingerprints; the exact compiler streams;
MCP result hashes and normal/race logs; and explicitly transcribed validation
command outcomes. Independent final implementation review found no blocking
issue. Package binaries and restored build directories remain outside the repo.

## Scope and next work

These facts establish compiler-bound source observations, not runtime DI
activation, message delivery, persisted database effects, or a proven cross-service
flow. Unsupported cases retain ordinary compiler results: factory/instance DI,
open and constructed generic targets, runtime table names, callback table
configuration, and private messaging wrappers. Absence of a fact is not proof
that the behavior is absent.

CodeGraph's private `TC.Common.TcServiceStack.Queue` wrapper is not treated as
MassTransit merely because it has a method named `Publish`. This fixture is
generated source against real frameworks; it is not held-out application quality,
whole-CodeGraph coverage, or a competitive superiority result. Large Roslyn graph
and lexical gates were not rerun because this slice changes the offline compiler
pipeline and compiler tools, not syntax graph extraction or lexical retrieval.

Next work should join these typed observations into bounded contract-impact
queries across captured projects, preserving build-context scope, source-linked
paths, unsupported-wrapper diagnostics, and explicit runtime uncertainty.

The scoped query follow-up is now recorded in the [contract-impact acceptance](CONTRACT-IMPACT-ACCEPTANCE.md).
