# Open-generic DI registration acceptance

Date: 2026-10-01. Status: accepted for bounded positional registration templates.
Single-agent, CPU-only execution.

## Result

The frozen eShopOnWeb `open-repository` anchor now records a scoped template from
`IReadRepository<>` in ApplicationCore to `EfRepository<>` in Infrastructure.
Both retain their exact owning project identity. Public MCP navigates both generic
definitions and returns their registration evidence through contract-impact and
context queries.

The call remains at byte 548 in
`src/Web/Configuration/ConfigureCoreServices.cs`, SHA256
`1e2431014bb7717c2916cea7984c25b86ff448847fd5a94edc9cc5dadd0d835a`.
Only this frozen expectation changes from the worker14 baseline.

| Frozen application | Requested facts | Resolved anchors | Remaining gaps |
| --- | --- | --- | --- |
| eShopOnWeb | 7/8, previously 6/8 | 10/10 | Assembly scan |
| ForkJoint | 8/11, unchanged | 11/11 | Consumer scan, activity scan, routing slip |
| Combined | 15/19 | 21/21 | Four |

Two binding-only expectations are excluded from the fact denominator. These are
source-authored development observations. Independent solver coverage stays 3/12;
the paired CodeGraph comparison remains blocked on its private dependencies and
database setup. The observation count is not an overall project completion score.

## Rule boundary

Worker15 emits `di_open_generic_registration_configuration`, rule
`csharp-open-di-v1`, with `service_template`, `implementation_template` and lifetime.
Only exact metadata AddSingleton/AddScoped/AddTransient `Type, Type` overloads and
direct unbound `typeof` operands are admitted. Both definitions must be non-nested,
have matching arity between one and eight, and represent an interface service and
nonabstract class implementation.

The compiler must establish interface correspondence using the implementation's
type parameters in identical positions. The rule means service parameter n maps
to implementation parameter n. Inherited interfaces are allowed; swaps, repeats,
constants and nested substitutions are excluded. Readers check shape, target arity,
identity, API, lifetime and version; compiler provenance establishes the relation.

No closed services are fabricated. Generic constraints, constructor availability,
registration precedence, successful resolution and activation are not inferred.
The fixture deliberately accepts a positional class with a private constructor to
keep that distinction testable. Existing concrete DI rules remain unchanged.

## Validation and evidence

- Full Go suite, vet, and semantic/import/index/MCP race checks with all nine native
  fixtures enabled.
- New native fixture: nine positives, eighteen exclusions, exact lifetimes and
  qualified definition identities, plus incomplete-compilation suppression.
- Artifact persistence, index reopening, public binding/definition/impact/context
  navigation, and public response-schema validation.
- Eight earlier native fixtures pass on worker15; all eight retained worker14
  streams pass public import compatibility checks.
- Worker6–15 implementation-index compatibility and new-rule old/future-version
  rejection. Both pinned and package-free worker builds have zero warnings/errors.
- Both frozen applications retain their commits, tracked source bytes and source
  gold. All public anchors resolve; prior observations remain present.

Evidence: [manifest](results/open-generic-di-20261001/result.json),
[template navigation](results/open-generic-di-20261001/public/navigation/report.json),
[eShopOnWeb observations](results/open-generic-di-20261001/public/eShopOnWeb-public/report.json),
[ForkJoint observations](results/open-generic-di-20261001/public/ForkJoint-public/report.json).
The archive retains source snapshots, native streams, capture metadata, validation
logs and public requests/responses. Previous milestone archives remain unchanged.
Corpora, dependency caches, binaries and generated indices stay local.

## Next move

Bounded assembly-scan configuration is the next remaining evidence class. Begin
with the exact MediatR registration API and a compiler-resolved assembly witness;
record declared scan scope before considering discovered registrations. Keep
MassTransit consumer/activity scanning and routing-slip behavior separately gated.
