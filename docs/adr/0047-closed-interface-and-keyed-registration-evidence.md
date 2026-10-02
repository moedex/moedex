# ADR 0047: Closed interface and keyed registration evidence

Status: accepted, 2026-10-01.

## Problem

The pinned eShop Webhooks closure uses `IIntegrationEventHandler<T>` methods,
an explicit default interface body forwarding from the nongeneric handler, and
`AddSubscription<T, TH>` to configure keyed DI. Worker6 deliberately omitted these
relationships. Erasing generic arguments would conflate different event handlers;
matching the helper's name would mistake application vocabulary for semantics.

## Decision

Worker7 adds three bounded interpretations without changing index layout v5:

- `csharp-interface-closed-v1` records compiler-verified method correspondence for
  nongeneric source classes implementing a closed interface. The interface has
  no containing type, at most eight type arguments, and each argument is a
  resolved nongeneric named type. Generic methods remain excluded.
- `constructed_interface_method_v1` keys retain the original documentation ID
  plus ordered qualified argument keys in a JSON descriptor. The outer symbol
  namespace identifies the interface's owning project/assembly. Qualified
  argument namespaces prevent equal simple names from collapsing. Both recorded
  invocations and implementation targets use this representation. Its exact
  serialized descriptor is identity-bearing; callers consume returned IDs.
- `csharp-interface-default-v1` records explicit default interface bodies at
  their own source declarations. These are templates, not selected closed class
  implementations. Existing reference bindings expose the forwarding call; source
  retrieval exposes its cast. Reverse lookup includes these records, but automatic
  contract paths exclude default-template implementation hops until closed
  substitution and dispatch obligations can be represented explicitly.

`csharp-keyed-di-v1` records the two-generic-argument Microsoft DI keyed
singleton/scoped/transient overload with `IServiceCollection, object` parameters
and a `typeof` key. Source-local lookalikes, factories and arbitrary key values do
not qualify. The [Microsoft API reference](https://learn.microsoft.com/en-us/dotnet/api/microsoft.extensions.dependencyinjection.servicecollectionserviceextensions)
defines the overload family; the captured metadata signature remains the matcher.

Direct observations use `di_keyed_registration`. A source helper invocation uses
`di_keyed_registration_configuration` only when a complete captured source method
has a recognized keyed call as its first top-level expression statement. The
helper must be static, synchronous and generic with at most eight type parameters.
Substitution accepts only its own method type parameters or already supported
named types. One helper level only: no recursive summaries, conditional/nested
statement traversal, factories or arbitrary data-flow analysis. The helper's
name is irrelevant.

Both fact kinds expose ordered targets `service`, `implementation`, `key_type`,
and `registration_api`, plus lifetime. The metadata API target is validated as
an exact overload/lifetime witness; it is not a service type. A helper fact's bound
symbol identifies the source helper, so definitions/source retrieval can audit
its body. These are compiler-backed configuration observations, never proof that
a particular container, broker or handler ran. Later removal/replacement of a
registration is not resolved by this rule.

## Compatibility and validation

Worker1–6 artifacts remain readable. New rules require worker7 provenance; older
workers cannot claim them. Old readers reject the new rules/descriptor kind.
Recapture and republish are required to obtain new evidence; rebuilding an old
artifact cannot infer it. New captures are published separately from old snapshots.

The generic/default and keyed metadata shapes are validated on artifact import,
index open and public implementation projection. The usual result, work and
materialization bounds apply. Native fixtures cover qualified identity separation,
closed calls, explicit defaults, keyed helpers with different names, unsupported
shapes, and incomplete-context suppression. Public eShop workflows discover IDs
through source/compiler tools; they remain development regressions, not blind
agent tests. The tuned eShop sample is retired from holdout use.
