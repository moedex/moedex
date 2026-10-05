# Declared MediatR assembly-scan acceptance

Date: 2026-10-01. Status: accepted for bounded declared scan scope.
Single-agent, CPU-only execution.

## Result

The frozen eShopOnWeb `assembly-scan` anchor now records the exact
`BasketViewModelService` marker from `typeof(BasketViewModelService).Assembly`.
Its project identity remains `eShopOnWeb/src/Web/Web.csproj`. Public MCP resolves
that marker's source definition and returns the scan observation through reverse
contract-impact and context queries.

The call remains at byte 365 in `src/Web/Configuration/ConfigureWebServices.cs`,
SHA256 `74723f33e98728b101e499581d0e758b54b8f99f3f048f8a198ae8873cce8fb8`.
Only this frozen expectation changes from the worker15 baseline.

| Frozen application | Requested facts | Resolved anchors | Remaining gaps |
| --- | --- | --- | --- |
| eShopOnWeb | 8/8, previously 7/8 | 10/10 | None in the frozen observation set |
| ForkJoint | 8/11, unchanged | 11/11 | Consumer scan, activity scan, routing slip |
| Combined | 16/19 | 21/21 | Three |

Two binding-only expectations are excluded from the fact denominator. These counts
measure source-authored development observations, not independent solver results
or overall project completion. Independent coverage remains 3/12. The paired
CodeGraph comparison remains blocked on its private dependencies and database.

## Rule boundary

Worker16 adds `mediator_assembly_scan_configuration`, rule
`csharp-mediatr-scan-v1`, with one `assembly_marker` target and compile-time scope.
It matches the exact MediatR metadata RegisterServicesFromAssembly overload and
a direct System.Runtime `System.Type.Assembly` property operation on `typeof` of
a simple named type. The target preserves the existing qualified type identity.

The marker witnesses the declared assembly, not a namespace filter or a discovered
handler. There is no assembly enumeration or inference of registrations, callback
execution, container use, activation or runtime handler selection. A detached
configuration call intentionally remains a positive configuration observation.

Generic types/owners, arrays, tuples, type parameters, variables, factories,
reflection assembly/type lookups, explicit casts, conditional values, TypeInfo
chains, plural/convenience APIs and source lookalikes remain excluded. The verified
fixture uses SDK8 and MediatR12.0.1; the metadata owner boundary is explicit.

## Validation and evidence

- Full Go suite, vet, and semantic/import/index/MCP race checks with all ten native
  fixtures enabled.
- New fixture: eight scan observations, twenty-one exclusions, exact qualified
  marker keys, and incomplete-compilation suppression.
- Artifact persistence, index reopening, public binding/definition/impact/context
  navigation and public response-schema validation.
- Nine earlier native fixtures pass on worker16; all nine retained worker15 streams
  pass current public import compatibility checks.
- Worker6–16 persisted implementation compatibility; new-rule downgrade and future
  version rejection. Pinned and package-free builds have zero warnings/errors.
- Both frozen captures preserve commits, tracked source hashes and source gold.
  All 21 anchors resolve and every previous observation remains present.

Evidence: manifest (archival evidence maintained separately),
marker navigation (archival evidence maintained separately),
eShopOnWeb observations (archival evidence maintained separately),
ForkJoint observations (archival evidence maintained separately).
The archive retains source snapshots, native streams, capture metadata, validation
logs and public requests/responses. Previous milestone archives remain unchanged.
Corpora, dependency caches, binaries and generated indices stay local.

## Next move

Bounded MassTransit namespace-scan configuration: the frozen ForkJoint calls are
AddConsumersFromNamespaceContaining<CookOnionRingsConsumer> and
AddActivitiesFromNamespaceContaining<GrillBurgerActivity>. Preserve their exact
marker identities and declared scan category, without inferring the discovered
consumer/activity inventory. Routing-slip evidence remains separately gated.
