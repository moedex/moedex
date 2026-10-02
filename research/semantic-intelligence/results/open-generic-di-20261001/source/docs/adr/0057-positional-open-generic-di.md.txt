# ADR 0057: positional open-generic DI registration templates

Date: 2026-10-01. Status: accepted.

## Context

The frozen eShopOnWeb registration calls
`AddScoped(typeof(IReadRepository<>), typeof(EfRepository<>))`. Existing DI facts
accept simple named type arguments and cannot faithfully describe this open
registration. Treating generic definitions as concrete services would overstate
what the compiler establishes.

## Decision

Worker15 adds `di_open_generic_registration_configuration` under
`csharp-open-di-v1`. It records `service_template`, `implementation_template` and
`lifetime`, with `compile_time` scope. Targets retain original generic definition
keys and their owning project or assembly. No new symbol descriptor is needed.

Only the exact metadata `AddSingleton`, `AddScoped` and `AddTransient` overloads
on `ServiceCollectionServiceExtensions` taking `IServiceCollection, Type, Type`
are admitted. Both type arguments must be direct `typeof` operations on unbound,
non-nested generic definitions with matching arity from one through eight. Named
arguments and static invocation are matched by compiler parameter identity.

The service must be an interface and the implementation a nonabstract class.
Roslyn's implemented interfaces must include that service definition with the
implementation's own type parameters in identical ordinal positions. Thus this
rule explicitly means service parameter n corresponds to implementation parameter
n. Inherited interface correspondence is allowed; swapped, repeated, constant or
nested substitutions are excluded. This bounded rule needs no general mapping
expression or synthesized closed service.

Artifact and index readers validate target shape, matching arity, distinct
identities, exact API/lifetime, rule and extractor version. Compiler provenance
establishes type kind and positional correspondence. Worker15 is required for the
new rule; supported older captures remain readable. Unknown future workers and
older workers claiming the new rule are rejected.

## Consequences

Generic template definitions are navigable through existing public definition,
contract-impact and context tools. The native fixture covers constrained classes
and a private constructor deliberately: this evidence does not validate generic
constraints for any closed type, constructor accessibility, registration order,
container resolution or runtime activation.

Factories, runtime `Type` variables, explicit conversions, conditional type
expressions, closed registrations, class services, nested owners, overly wide
arity, TryAdd and keyed overloads are outside this rule. Existing simple DI facts
and symbol identities remain unchanged.

The frozen development baseline improves to 15/19 requested facts, with all 21
anchors resolving. Independent solver coverage remains 3/12. See
[acceptance](../../research/semantic-intelligence/OPEN-GENERIC-DI-ACCEPTANCE.md).
