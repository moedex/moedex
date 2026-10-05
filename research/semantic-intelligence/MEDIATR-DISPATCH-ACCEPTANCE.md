# Direct MediatR dispatch acceptance

Date: 2026-10-01. Status: accepted for the bounded dispatch rule.
Single-agent, CPU-only execution.

## Result

Both frozen eShopOnWeb send anchors now carry compiler-grounded configuration facts:

- `GetMyOrders` requests `IEnumerable<OrderViewModel>`, preserving the qualified
  project identity of the element type.
- `GetOrderDetails` requests `OrderDetailViewModel`.

Public MCP verifies the exact request and response identities, request source
definitions and reverse response context. eShopOnWeb improves from 3/8 to 5/8
recorded requested facts; ForkJoint remains 8/11. All 21 frozen anchors resolve.
The combined observation baseline is 13/19, with six missing observations and two
binding-only expectations excluded from the denominator.

These facts do not select the handlers named in the source-gold descriptions.
The separate orders-handler correspondence gap remains open. Sources, commits and
gold are unchanged. This is a source-authored development baseline, not a new
independent solver result; that score remains 3/12 and the paired CodeGraph gate
remains open.

## Rule boundary

Worker13 adds `mediator_send_configuration` under `csharp-mediatr-v1`. It matches
exactly MediatR ISender's generic Send overload taking IRequest<TResponse> and
CancellationToken. Only direct object construction beneath bounded implicit
compiler conversions is accepted. The constructed request must implement the
MediatR.Contracts IRequest interface for the exact bound response type.

The rule does not follow variables, factories, explicit casts or conditional
values. Object/no-response overloads, concrete Mediator methods, source lookalikes,
covariant response adaptation, generic requests, open/nested generic responses,
arrays, tuples and over-wide generic responses are excluded. Incomplete
compilations emit no interpreted facts. Surrounding branch execution, pipeline
behavior, runtime dispatch and handler selection are not asserted.

Response identities support simple named types and a single closed generic level.
The new `constructed_named_type_v1` descriptor stores the original type ID and up
to eight independently qualified simple argument keys, bounded to 32 KiB. This
prevents IEnumerable<One.Reply> and IEnumerable<Two.Reply> from collapsing. Existing
ordinary binding keys retain their previous representation. Constructed response
identities do not imply synthetic source definitions or generic handler matches.

The new descriptor is validated in artifact and index readers; only this rule's
response role admits it as a domain target. Other rules retain their existing type
restrictions. Older captures remain readable; new facts require worker13 and
unsupported future versions fail. API semantics were checked against pinned
MediatR12.0.1 / Contracts2.0.1 metadata and the official
[ISender source](https://raw.githubusercontent.com/jbogard/MediatR/v12.0.1/src/MediatR/ISender.cs).
Assembly names remain provenance rather than publisher authentication.

## Validation

- Seven positive native cases and sixteen exclusions, including namespace-distinct
  generic arguments, multiple arguments, inherited request contracts and branch
  configuration. Incomplete compilation suppresses all interpreted facts.
- Artifact/index write-reopen, exact request declaration navigation and request/
  response contract evidence through public MCP, validated against JSON schemas.
- Tests reject malformed, oversized and mismatched constructed descriptors,
  incorrect roles/APIs, downgraded rules and future worker versions.
- Six prior native suites pass with worker13. Retained worker12 fixtures still
  pass public persistence tests; index compatibility covers workers6 through13.
- Full Go tests, vet and race checks pass with all seven native fixtures enabled.
  Pinned Roslyn4.11 and default package-free worker builds pass without warnings
  or errors.
- Both applications recapture and publish. Only `mediator-orders` and
  `mediator-details` change observation presence; all other frozen checks remain
  stable. Exact generic arguments were reviewed, not just fact presence.

The initial test-runner syntax failure and initial worker nullability warning are
retained with the passing corrected runs.

Final artifact SHA256:

- eShopOnWeb: `579894dc5e24c30ea8f36ff4978f13bc98692e3fcc399ffcfd8c4d661168c8f9`.
- ForkJoint: `796dd83d2ced6cf7044573a2e15d40ed0f2b81d01d067ec84d82194e8051db53`.

See result.json (archival evidence maintained separately) for hashed source
snapshots, commands, logs, native captures and public requests/responses. Large
artifacts, corpora and binaries remain local. Test servers were stopped.

## Next work

The remaining observations are orders-handler correspondence, assembly scanning
and open-generic repository registration in eShopOnWeb; consumer scanning,
activity scanning and routing-slip configuration in ForkJoint. Next: bounded
handler correspondence with closed generic response arguments. Missing facts do
not imply missing application behavior.
