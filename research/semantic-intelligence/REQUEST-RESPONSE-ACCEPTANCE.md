# Typed request/response configuration acceptance

Date: 2026-10-01. Status: accepted for the bounded two-response rule.
Single-agent, CPU-only execution.

## Result

The frozen ForkJoint request/response gap is closed. Public MCP identifies
`ForkJoint.Contracts.SubmitOrder` as the request, with ordered response alternatives
`OrderCompleted` and `OrderFaulted`, all in the Contracts project context.
ForkJoint improves from 6/11 to 7/11 requested higher-level facts. eShopOnWeb stays
3/8. All 21 frozen source anchors resolve; two binding-only expectations are
excluded from the fact denominator. The combined fact baseline is now 10/19,
with nine gaps remaining.

Sources, upstream commits and gold remain unchanged. This is a source-authored
development check, not an independent solver evaluation. Independent solver
coverage remains 3/12; the paired CodeGraph comparison remains open.

## Rule and limits

Worker11 introduces `message_request_configuration` under `csharp-framework-v6`.
It accepts exactly the two non-callback `IRequestClient<TRequest>.GetResponse<T1,T2>`
metadata signatures taking a typed request or object initializer, followed by
CancellationToken and RequestTimeout. The target roles are `request`,
`response_1`, and `response_2`, preserving order and repeated types.

The request type comes from the bound containing interface, and response types
come from bound method arguments. Payload property names are not used. Matching
requires the exact metadata owner, assembly name and signature. Each target must
retain a supported concrete named identity. Assembly identity is provenance, not
publisher authentication.

This records call-site configuration. It does not prove execution, delivery,
consumer selection, destination, or which response occurs. Callback, single- and
three-response overloads, source lookalikes, open or constructed generic contracts,
and incomplete compilations remain excluded. Readers retain earlier worker
support and reject unsupported future versions and downgraded new-rule facts.
No artifact wire fields were added.

The fixture uses pinned MassTransit.Abstractions8.2.1 metadata. The official
[IRequestClient source](https://raw.githubusercontent.com/MassTransit/MassTransit/master/src/MassTransit.Abstractions/Clients/IRequestClient.cs)
provides API context; a moving branch is not the matching oracle.

## Validation

- Six native positive cases: initializer, typed payload, distinct same-named
  request, reversed responses, inherited client, and repeated response type.
- Eight exclusions plus suppression of all interpreted facts in an incomplete
  compilation.
- Artifact/index write-reopen and public MCP checks preserve exact ordered
  descriptors, source provenance and target navigation.
- Four earlier native suites pass with worker11. Retained worker10 captures still
  pass public persistence tests; index compatibility covers workers6 through11.
- Full Go tests and race checks run with all five native fixtures enabled. Vet
  passes. Pinned Roslyn4.11 and default package-free builds report zero warnings
  and zero errors.
- Both frozen applications recapture, publish and pass public checks. The only
  changed expectation is ForkJoint's `request-response`; its exact targets were
  reviewed rather than accepting fact presence alone.

Final artifact SHA256:

- ForkJoint: `0b9692b9d92debf2b85e87c869333a0af948185b1a79f2340b6eadda9e0f33c4`.
- eShopOnWeb: `9f064e726eb0548736ed62dcaa6c1cb332cf5362c22cc52eab7a858bab17caea`.

See result.json (archival evidence maintained separately) for source
snapshots, commands, native captures and public requests/responses. Large corpora,
artifacts, dependencies and binaries remain local. Temporary servers were stopped.

## Remaining work

ForkJoint: consumer scanning, activity scanning, local-method POST mapping,
routing-slip configuration. eShopOnWeb: two MediatR sends, nested-generic handler
correspondence, assembly scanning, and open-generic repository registration.
Next: source-backed local-method endpoint mapping. Missing observations do not
imply missing application behavior.
