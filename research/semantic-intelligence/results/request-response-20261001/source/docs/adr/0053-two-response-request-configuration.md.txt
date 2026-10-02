# ADR 0053: Preserve request and response contract identities

Status: accepted. Date: 2026-10-01.

## Decision

Worker11 emits `message_request_configuration` under `csharp-framework-v6` for
exact non-callback MassTransit IRequestClient two-response overloads. The bound
client supplies the request type; method arguments supply two ordered response
types. Preserve them as `request`, `response_1`, `response_2`, including repeated
types. All targets require supported concrete named identities.

The fact records compile-time call configuration. It neither selects a consumer
nor proves a route, execution or delivery. Callback overloads, other response
arities, generic shapes with erased identity and incomplete captures remain out
of scope. Reuse existing fact serialization and target navigation; gate the new
rule to worker11 while preserving older captures.

## Evidence

Real-framework fixtures cover order, distinct request identities, inherited
clients, repeated responses and negative controls. Persistence and public MCP
retain the same roles and descriptors. The unchanged ForkJoint gold gains one
fact while all other expectations remain stable. See
[acceptance](../../research/semantic-intelligence/REQUEST-RESPONSE-ACCEPTANCE.md).
