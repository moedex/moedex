# ADR 0042: MassTransit configuration evidence

Date: 2026-10-01
Status: Accepted

## Context

The external sample configures two state-machine publish activities and a saga
event. All three calls resolve in captured compilations. Treating them as direct
publication or consumer activation would obscure what the source establishes.

## Decision

Worker 5 adds `csharp-framework-v4` facts with distinct kinds:

- `message_publish_configuration` for the exact synchronous three-type-parameter
  `MassTransit.PublishExtensions.Publish` metadata overload taking
  `EventActivityBinder<TInstance,TData>`,
  `EventMessageFactory<TInstance,TData,TMessage>`, and
  `Action<PublishContext<TMessage>>`. The bound third method type argument is the
  message identity.
- `message_event_configuration` for the exact
  `MassTransitStateMachine<TInstance>.Event<TData>` metadata overload taking an
  expression returning `Event<TData>` and an
  `Action<IEventCorrelationConfigurator<TInstance,TData>>`. The bound first
  method type argument is the message identity.

Both require the `MassTransit` assembly, exact documentation descriptors,
concrete named message targets, and compile-time scope. Each has one `message`
target and no lifetime, table, or schema attributes. Other overloads, async
factories, generic targets, unresolved calls, and source lookalikes remain outside
these rules. No lambda body, dispatch, delivery, or runtime flow is inferred.

Previous rules remain unchanged. Artifact and mapped-index validation admit these
facts only with worker 5 provenance; existing persisted formats do not change.

## Evidence discipline

Preserve earlier source and task gold. Freeze a source-authored successor before
capture, promoting only the three configuration sites and changing their kinds
explicitly. Previously frozen retrieval tasks involving these messages require
versioned successor expectations because the valid evidence set grows.

Use real pinned metadata fixtures for exact signatures, constructed type
arguments, extension/static spelling, callbacks, overload exclusions, generic
targets, and lookalikes. Compare ordinary audit facts with published MCP results,
verify source and owner provenance, and preserve normal/race reports.

The two private service-wrapper gaps remain separate. A call through an interface
does not establish its runtime implementation. Future wrapper paths need explicit
compiler member-implementation relationships and bounded call traversal; they
must not manufacture direct publisher facts at wrapper call sites.
