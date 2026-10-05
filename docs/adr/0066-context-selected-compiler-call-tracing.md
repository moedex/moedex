# ADR 0066: Context-selected compiler call tracing

Status: Accepted · Date: 2026-10-05

## Context

The syntax graph can associate unrelated methods with the same name. Captured
Roslyn bindings already preserve qualified invocation targets, enclosing symbols,
source spans and build contexts, but native callers cannot traverse them directly.
Contract paths offer a specialized caller step; they are not a general call trace.

## Decision

Add `compiler_trace_calls(symbol_id, context_ids, direction?, depth?, limit?)`.
Require an exact recorded method identity and 1–32 explicit contexts. Existing
symbol/binding tools provide those choices. Reject duplicate, unknown and
same-project context variants. Distinct selected projects form a union of
historical observations; selection does not establish dependency compatibility.

Traverse resolved owned method invocations in deterministic breadth-first order.
Preserve static interface/virtual targets without inserting implementations.
Deduplicate invocation witnesses, visit each method once, and retain cycle edges.
Distinct call sites to the same target remain separate. Only named `M:` descriptors
qualify as method endpoints (nameless anonymous-function IDs are excluded): property/initializer owners, local/lambda fallback
identities, method groups, unresolved/ambiguous references and object-creation
references do not become call edges. Calls inside named constructor methods can
remain ordinary invocation witnesses; object creation is a separate fact kind.

Semantic index v6 adds an eight-byte fact-row posting per resolved owned invocation,
sorted by enclosing symbol, context and fact. The header grows from 400 to 416
bytes. Opening checks complete, unique correspondence to the validated inbound
invocation directory. Versions 1–5 remain readable but report
`index_upgrade_required` for this tool. Rebuilding from the same admitted complete
artifact needs no compiler recapture and preserves the audit artifact identity.

Depth is 1–8 (default 1), direction defaults outbound, and limit is 1–100 call-site
witnesses (default 20). Each query shares a 10,000-row work and conservative 2 MiB
materialization budget. Limits return explicit truncation. Oversized root identity
fails before materialization. MCP trims complete trailing edges under a 64 KiB
serialized tool-result cap. Each edge retains caller identity, compiler target,
commit, context, source snapshot/hash/span, generated status and binding provenance.
One immutable serving lease supplies the whole result. The adapter validates
provider identities, context joins, duplicate witnesses, directed connectivity,
depth and bounds. Responses are non-cacheable and identify snapshot/artifact.

Keep `trace_calls` available with its confidence-scored graph candidates. Native
instructions and both tool descriptions distinguish the two evidence models.

## Consequences

An empty trace describes missing recorded evidence, not program-wide absence.
Compiler coverage is limited to captured projects; static paths do not prove
runtime activation, dispatch, execution, messaging or deployment impact. Nested
lambda calls remain outside named-method traversal until an explicit ownership
model is designed. HTTP route observations in another graph are not directly
comparable to compiler invocation witnesses.

## Evidence

Colocated tests cover homonyms, cycles, depth/direction, context isolation,
work/materialization/serialized bounds, corruption, v5 compatibility, cancellation
and serving leases. `tools/semantic-dotnet/test_call_tracing.py` captures an offline
real-SDK fixture and exercises the public compiler discovery/tracing tools for
interface targets, overloads, method groups, lambdas and constructors.
