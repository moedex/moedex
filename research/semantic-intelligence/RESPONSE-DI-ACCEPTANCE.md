# Typed response and conditional DI acceptance

Date: 2026-10-01. Status: accepted for the bounded rules below. Single-agent,
CPU-only execution; no embeddings or GPU work.

## Result

Two of the twelve frozen higher-level coverage gaps are closed. ForkJoint rises
from 4/11 to 6/11 recorded requested facts; eShopOnWeb retains 3/8. All 21 frozen
source anchors resolve, with two binding-only expectations excluded
from those fact denominators. Across the two applications, 9/19 requested facts
are now recorded and 10 remain missing. Sources, commits and gold are unchanged.
These are source-authored development checks; the independent solver score stays
3/12 and the paired CodeGraph comparison remains open.

The exact new ForkJoint facts are:

- `CookFryConsumer`'s typed response identifies `ForkJoint.Contracts.FryReady`.
- `TryAddSingleton<IFryer,Fryer>` identifies the service and implementation with
  singleton lifetime and an explicit **if-absent** kind. It does not assert that
  the registration took effect or that Fryer is the runtime implementation.

Both facts were inspected through public MCP, including typed target descriptors,
source hashes, selected compiler contexts and extractor provenance. Presence alone
was not treated as sufficient acceptance.

## Rule boundary

Worker10 adds `csharp-framework-v5` without changing the artifact wire format:

| Fact | Accepted bound API | Recorded targets |
| --- | --- | --- |
| `message_response` | `ConsumeContext.RespondAsync<T>(T)` and `RespondAsync<T>(object)` | message contract |
| `di_registration_if_absent` | one/two-type generic `TryAddSingleton`, `TryAddScoped`, `TryAddTransient`, taking only IServiceCollection | service, implementation, lifetime |

Matching requires the exact metadata owner, assembly name and method signature.
The response target is the compiler-bound type argument, not an inference from
payload property names. These facts describe calls in source; they do not prove
execution, delivery, a requester, a route, or runtime service selection. For DI,
the conditional fact does not evaluate earlier registrations or control flow.
Assembly identity remains captured evidence rather than publisher authentication.

Factory, instance and runtime-type DI overloads, response pipe and non-generic
overloads, source lookalikes, open/constructed generic targets and incomplete
compilations remain excluded. Existing unconditional registration facts retain
their distinct kind. Import, artifact, persisted index and MCP gates accept the
new worker while retaining earlier versions; unsupported future versions fail.

API semantics were checked against the pinned package metadata and
[.NET8 DI source](https://raw.githubusercontent.com/dotnet/runtime/v8.0.0/src/libraries/Microsoft.Extensions.DependencyInjection.Abstractions/src/Extensions/ServiceCollectionDescriptorExtensions.cs).
[MassTransit ConsumeContext source](https://raw.githubusercontent.com/MassTransit/MassTransit/master/src/MassTransit.Abstractions/Contexts/ConsumeContext.cs)
provides additional context; matching and the fixture use the captured 8.2.1
assembly, not a moving source branch.

## Validation and retained evidence

- New native fixture: 10 exact positive facts, 11 exclusions, distinct
  unconditional registration and complete suppression on incomplete compilation.
- Artifact/index write-reopen and public MCP checks: all fixture targets and
  negative anchors checked, plus contract-impact navigation.
- Forwarding, default-selection and framework-bridge native fixtures pass with
  worker10; retained worker9 fixtures still pass public persistence tests.
- A non-optional persisted-index regression checks workers 6 through 10.
- Full Go tests with all four native fixtures enabled pass; Go vet and race tests
  for semantic, semanticimport, semanticindex and MCP pass.
- Pinned Roslyn4.11 and default package-free worker builds both pass with zero
  warnings/errors.
- ForkJoint and eShopOnWeb were recaptured and published with worker10. The first
  ForkJoint publication exposed a remaining worker9-only index allowlist; that
  failure is retained, the gate is fixed, and the final fresh run passed.

Final artifacts:

- ForkJoint: `21c8e07824aae6a98c33757831db398690eb9c6541cb38993db69b24721f8380`.
- eShopOnWeb: `13533bf1e20e2d05660a1ca19daff7bda6b23c4eae8c0b2eeb25f03b557f9755`.

See result.json (archival evidence maintained separately) for source snapshots,
commands, validation logs, native fixtures and public requests/responses. Large
artifacts, dependencies, corpus checkouts and binaries stay in `.local/response-di`
or their previously retained locations.

## Remaining work

ForkJoint: consumer scanning, activity scanning, local-method POST mapping,
two-response request configuration, routing-slip configuration.
eShopOnWeb: two MediatR sends, nested-generic handler correspondence, assembly
scanning, open-generic repository registration. Absence of a recorded fact is not
absence of application behavior. Next, tackle the frozen typed request/response
configuration with similarly bounded identities and adversarial controls.
