# ADR 0054: Preserve declared POST patterns and source handler identities

Status: accepted. Date: 2026-10-01.

## Decision

Worker12 adds `endpoint_post_configuration` under `csharp-endpoint-v1`, matching
only the exact ASP.NET MapPost overload taking System.Delegate. Record a bounded,
nonempty compiler-constant `route_pattern` and one `handler` target: a source
static ordinary method without generic method or containing-type parameters.
Unwrap only direct compiler conversions/delegate creation with a fixed bound.

The pattern remains declared rather than composed. Route groups, hosting,
conventions, control flow and runtime activation are not evaluated. No URL or
execution claim is synthesized. Unsupported handler forms and overloads emit no
endpoint observation.

## Compatibility and evidence

The optional route-pattern field flows through worker import, artifact/index
storage and public schemas, and is omitted from older facts. Existing serialized
identities and older capture support are preserved; new facts require worker12.
Method target validation is specific to this rule and does not relax type-target
validation for other domain facts. Navigate handlers by exact method identity;
class evidence lookup keeps its existing scope.

Native adversarial fixtures, public persistence/schema tests and unchanged
application gold verify the boundary. See
[acceptance](../../research/semantic-intelligence/ENDPOINT-MAPPING-ACCEPTANCE.md).
