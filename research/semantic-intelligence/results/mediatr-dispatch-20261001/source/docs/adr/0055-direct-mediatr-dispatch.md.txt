# ADR 0055: Direct MediatR request and qualified response identities

Status: accepted. Date: 2026-10-01.

## Decision

Worker13 records `mediator_send_configuration` under `csharp-mediatr-v1` for the
exact ISender generic Send API, a directly constructed nongeneric named request,
and its exact declared IRequest response. Do not follow indirect values or infer
runtime handler selection.

To preserve IEnumerable<OrderViewModel> faithfully, introduce
`constructed_named_type_v1`: an original type documentation ID plus up to eight
qualified simple argument keys, bounded to one generic level and 32 KiB. Emit this
only for the new response role. Preserve existing binding representations and
restrictions in other domain rules. Artifact and index readers validate the shape;
new rules require worker13 while older captures remain readable.

## Consequences

Dispatch facts preserve response arguments without synthesizing generic definitions
or handler links. Indirect requests, variance adaptation and more complex type
shapes remain absent. Native adversarial fixtures and public persistence/schema
checks verify the boundary. The two frozen send anchors gain observations while
the separate generic handler gap remains open. See
[acceptance](../../research/semantic-intelligence/MEDIATR-DISPATCH-ACCEPTANCE.md).
