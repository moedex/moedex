# Declared MassTransit namespace-scan acceptance

Date: 2026-10-01. Status: accepted for bounded declared namespace scans.
Single-agent, CPU-only execution.

## Result

Both frozen ForkJoint scan anchors now carry category-specific configuration facts:

- Consumer scan: CookOnionRingsConsumer, byte 6298.
- Activity scan: GrillBurgerActivity, byte 6376.

Both calls remain in `src/ForkJoint.Api/Startup.cs`, SHA256
`791dd61d4fc07062799ff3eab8e9d7345f666f64d016d73c36cedb8592ae268d`.
The marker types retain their exact ForkJoint.Api project ownership. Public MCP
navigates their source definitions and returns the respective scan declarations
through reverse impact and context queries. Only these two frozen expectations
change from the worker16 baseline.

| Frozen application | Requested facts | Resolved anchors | Remaining gaps |
| --- | --- | --- | --- |
| eShopOnWeb | 8/8, unchanged | 10/10 | None in the frozen observation set |
| ForkJoint | 10/11, previously 8/11 | 11/11 | Routing slip |
| Combined | 18/19 | 21/21 | One |

Two binding-only expectations are excluded from the fact denominator. These are
source-authored development observations, not independent solver completions or an
overall project completion percentage. Independent coverage stays 3/12. The paired
CodeGraph comparison remains blocked on its private dependencies and database.

## Rule boundary

Worker17 adds `consumer_namespace_scan_configuration` and
`activity_namespace_scan_configuration`, rule `csharp-masstransit-scan-v1`, with
compile-time scope and one `namespace_marker` target. Exact metadata overloads on
MassTransit RegistrationExtensions, the bound simple marker type and a constant-null
optional predicate are required. Omitted filters, explicit null, named arguments
and static calls are supported. A branch is recorded without evaluating it.

The marker witnesses declared assembly/namespace scope; it need not implement a
consumer or activity contract. We neither enumerate registrations nor assert that
the marker is discovered. The API-specific scan category remains distinct.
Runtime registration, endpoint configuration, delivery and execution are unproved.

Non-null/unknown predicates, generic types or owners, arrays, type parameters,
global-namespace markers, Type-taking overloads and source lookalikes are excluded.
The native fixture uses pinned MassTransit8.2.1 and SDK8. Exact API/category pairing
and worker versions are enforced by artifact and index validation.

## Validation and evidence

- Full Go suite, vet, and semantic/import/index/MCP race checks with all eleven
  native fixtures enabled.
- New native fixture: fourteen positives, twenty-four exclusions, exact qualified
  marker keys, both scan categories and incomplete-compilation suppression.
- Artifact persistence, index reopening, public binding/definition/impact/context
  navigation and response-schema validation.
- Ten earlier native fixtures pass on worker17; all ten retained worker16 streams
  pass current public import compatibility checks.
- Worker6–17 persisted implementation compatibility; scan-category confusion,
  downgrade and future-version rejection. Both worker builds have zero warnings/errors.
- Both application captures retain frozen commits, tracked source bytes and source
  gold. All 21 anchors resolve and prior observations remain present.

Evidence: [manifest](results/masstransit-scan-20261001/result.json),
[marker navigation](results/masstransit-scan-20261001/public/navigation/report.json),
[eShopOnWeb observations](results/masstransit-scan-20261001/public/eShopOnWeb-public/report.json),
[ForkJoint observations](results/masstransit-scan-20261001/public/ForkJoint-public/report.json).
The archive includes source snapshots, native streams, capture metadata, validation
logs and public requests/responses. Previous milestone archives remain unchanged.
Corpora, dependency caches, binaries and generated indices stay local.

## Next move

Bounded routing-slip configuration is the final missing frozen observation. Inspect
the exact future/builder API and preserve only compiler-grounded configuration
identities; do not infer itinerary execution, compensation or message delivery.
After closing the observation set, rerun independent task-level evaluation before
making any stronger claim about CodeGraph parity.
