# ADR 0058: declared MediatR assembly-scan scope

Date: 2026-10-01. Status: accepted.

## Context

The frozen eShopOnWeb configuration calls
`RegisterServicesFromAssembly(typeof(BasketViewModelService).Assembly)` inside an
AddMediatR callback. Worker15 binds the call but records no domain evidence. The
source identifies a scan scope; it does not enumerate discovered handlers or prove
that the callback executes.

## Decision

Worker16 adds `mediator_assembly_scan_configuration` under
`csharp-mediatr-scan-v1`, with `compile_time` scope and one `assembly_marker` target.
The target is the exact compiler-bound named type used in `typeof(Marker).Assembly`,
retaining its existing qualified definition identity. No assembly member inventory,
handler relationship or synthetic assembly symbol is emitted.

Match only MediatR's exact metadata
`MediatRServiceConfiguration.RegisterServicesFromAssembly(Assembly)` method and a
direct property operation for metadata `System.Type.Assembly` from System.Runtime,
whose receiver is a direct typeof operation on a simple named type. This verified
metadata boundary covers the pinned SDK8/MediatR12 fixture. Generic types and
owners, arrays, tuples, type parameters, reflection lookups, variables, factories,
explicit casts, conditional expressions, TypeInfo chains, alternate overloads and
source lookalikes are excluded.

The rule records the invocation's declared scope independently of its enclosing
callback or control flow. A detached configuration object therefore still produces
a configuration observation. A native positive explicitly tests that boundary.
The marker is an assembly witness; its namespace is not a namespace filter and the
marker itself is not asserted to be a handler.

Readers enforce the exact API, fact shape, simple target identity, rule and worker
version. Worker16 is required for this rule; current readers preserve support for
older captures and reject unsupported future versions. Existing fact and symbol
identities are unchanged.

## Consequences

Public definition, contract-impact and context tools can navigate the marker and
its scan declaration. Actual assembly traversal, discovered registrations, callback
execution, container activation and runtime handler selection remain unproved.
This closes the final frozen eShopOnWeb observation, giving 8/8 there and 16/19
across the two applications. Independent solver coverage remains 3/12.

See [acceptance](../../research/semantic-intelligence/MEDIATR-SCAN-ACCEPTANCE.md).
