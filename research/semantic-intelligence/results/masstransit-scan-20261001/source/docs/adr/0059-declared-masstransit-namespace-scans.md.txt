# ADR 0059: declared MassTransit namespace scans

Date: 2026-10-01. Status: accepted.

## Context

The frozen ForkJoint configuration calls AddConsumersFromNamespaceContaining with
CookOnionRingsConsumer and AddActivitiesFromNamespaceContaining with
GrillBurgerActivity. Both calls identify scan categories and marker types, but do
not establish the discovered registration set or runtime activation. Their optional
predicates also affect scope and must not be silently discarded.

## Decision

Worker17 adds `consumer_namespace_scan_configuration` and
`activity_namespace_scan_configuration` under `csharp-masstransit-scan-v1`.
Each has compile-time scope and one `namespace_marker` target preserving the exact
qualified marker definition. No new symbol descriptor or scalar field is required.

Match only the exact MassTransit RegistrationExtensions generic overloads taking
IRegistrationConfigurator and Func<Type, bool>. The marker must be a simple named
type in a non-global namespace. The compiler operation must supply a constant-null
predicate, including the omitted default. Non-null or unknown predicates remain
excluded. Method identity, not spelling alone, selects the scan category.

The marker identifies the API's assembly and namespace scope. No consumer/activity
contract is required on the marker itself; an ordinary class is a valid witness.
We do not enumerate candidates, reproduce framework traversal or claim the marker
is a discovered registration. Nested namespaces, filters and discovery remain
framework behavior rather than a reconstructed inventory.

Generic markers or generic owners, arrays, type parameters, global-namespace
markers, Type-taking overloads, source lookalikes and nonconstant predicates are
excluded. Static calls, named null arguments and branches retain their declared
configuration facts without evaluating execution.

Readers validate rule/version, category-specific API, target shape and scope.
Worker17 is required for the new rule. Supported older captures remain readable;
old workers claiming it and unknown future versions are rejected.

## Consequences

Existing public definition, contract-impact and context tools navigate both markers
and their scan declarations. Registration success, endpoint construction, message
delivery and activity execution remain unproved. The frozen baseline improves from
16/19 to 18/19 requested facts, leaving routing-slip configuration. Independent
solver coverage remains 3/12.

See [acceptance](../../research/semantic-intelligence/MASSTRANSIT-SCAN-ACCEPTANCE.md).
