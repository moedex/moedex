# ADR 0056: bounded generic handler correspondence

Date: 2026-10-01. Status: accepted.

## Context

The frozen eShopOnWeb orders handler implements
`IRequestHandler<GetMyOrders, IEnumerable<OrderViewModel>>`. Worker13 resolves its
source method but excludes implementation evidence because its closed-interface
identity accepts only simple named type arguments. Erasing the response's generic
argument would conflate different contracts.

## Decision

Worker14 adds `constructed_interface_method_v2` and
`csharp-interface-closed-v2`. The identity retains the original method documentation
ID and independently qualified argument keys. At least one argument must use the
existing `constructed_named_type_v1` representation; remaining arguments may be
simple named types. Each generic level has at most eight arguments, and the entire
serialized interface descriptor is limited to 32 KiB. Constructed type arguments
contain only simple named arguments; recursion is excluded.

The extractor requires a closed, non-nested generic interface's ordinary,
abstract, nonstatic, nongeneric member. Declaration evidence additionally requires
an ordinary source method on a nongeneric class and exact Roslyn
`FindImplementationForInterfaceMember` correspondence. Existing exclusions for
inherited method declarations and the 32-fact cap remain. Open, deeper generic,
array, tuple, overly wide and explicit generic class implementations are excluded.
Calls to a supported interface member use the same closed key as its implementation
fact. MediatR response facts and interface arguments share the bounded type-key
builder, allowing exact identity comparison.

Simple closed interfaces keep their v1 identities and rules. Default selection and
forwarding retain their previous bounds. The new rule requires worker14; older
workers and unknown future workers cannot claim it. Current readers continue to
accept supported older captures. Older readers require an upgrade for v2 keys.

## Consequences

Public reverse implementation lookup can navigate the exact closed orders contract
to its source method after artifact persistence and index reopening. Multiple
matching declarations remain visible. This is compiler correspondence: it does not
prove DI registration, handler selection, pipeline behavior or runtime execution.
No framework-specific name heuristic or assembly scan is introduced.

The frozen source-authored baseline rises from 13/19 to 14/19 requested facts.
Independent solver coverage stays 3/12; this change does not close that separate
acceptance gate. See [acceptance](../../research/semantic-intelligence/GENERIC-HANDLER-ACCEPTANCE.md).
