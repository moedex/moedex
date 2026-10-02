# ADR 0060: bounded routing-slip activity configuration

Date: 2026-10-01. Status: accepted.

## Context

ForkJoint calls IItineraryBuilder.AddActivity with a nameof activity label and a
readonly address field. Its constructor initializes that field using
`new Uri($"exchange:{formatter.ExecuteActivity<TActivity,TArguments>()}")`.
Matching the label alone would not establish the address's activity correspondence.

## Decision

Worker18 adds `routing_slip_activity_configuration`, rule `csharp-routing-slip-v1`,
with compile-time targets `activity`, `arguments`, `address_field`, `formatter_api`.
Only the exact metadata IItineraryBuilder.AddActivity(string, Uri, object) overload
is admitted. The label must be nameof of a simple named type.

The address must reference the current instance's private readonly field, with no
initializer, on the same nongeneric owning type. That type must have one source
instance constructor with a block body. Analysis is capped at 32 top-level
statements and 1,024 descendant syntax nodes. The constructor must reference the
field exactly once, in a direct top-level assignment to the current instance.

The value must be the exact System.Runtime Uri(string) constructor with a two-part
interpolated string: literal `exchange:` and the direct metadata
IEndpointNameFormatter.ExecuteActivity<TActivity,TArguments>() invocation, without
alignment or format specifiers. Both type arguments must be simple named types,
and the activity must equal the nameof type by compiler identity.

The bound formatter API and field keep existing qualified symbol identities.
Reader validation checks target roles/shapes, API, scope, rule and worker version;
compiler provenance establishes the constructor witness. Public source definitions
make the field and its owning source available for inspection.

## Consequences

This records a declared formatter-derived exchange address configuration, without
resolving its runtime string, formatter implementation, payload compatibility,
itinerary execution, compensation, transport reachability or delivery. Branches
around an AddActivity call do not establish execution.

Mutable/public/initialized fields, aliases, other-instance reads, conditional or
repeated constructor assignments, ref escapes, multiple constructors, factories,
other URI patterns and AddActivity overloads remain excluded. Oversized constructor
bodies fail closed. Existing rules and symbol identities remain unchanged; the new
rule requires worker18 and retains older-reader compatibility only for old captures.

All 19 frozen requested observations are now recorded, with 21/21 anchors resolved.
Independent solver coverage remains 3/12. See
[acceptance](../../research/semantic-intelligence/ROUTING-SLIP-ACCEPTANCE.md).
