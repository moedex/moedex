# Compact contract context and EF successor gate

Recorded October 1, 2026. Locally verified, not deployed. This milestone uses
CPU/compiler/index work; dense embeddings and GPU workloads are not involved.

## External sample coverage

The unchanged MassTransit Sample-Outbox revision
`1ab8e66ebf96e5733e68c2f4d2201276f38ed9c5` passes capture, composition,
publication, and actual leased MCP retrieval with worker 3. The original
[baseline](APPLICATION-IMPACT-ACCEPTANCE.md) remains preserved. A separately frozen
[successor gold](results/application-outbox-20261001/source-gold-v2.json) promotes
exactly two EF sites: `DbContext.Set<TEntity>()` and `ModelBuilder.Entity<TEntity>()`.

| Measure | Successor result |
| --- | ---: |
| Supported source observations | 10 / 10 |
| Context-specific facts, audit and served | 18 / 18 |
| False positives / false negatives | 0 / 0 |
| Negative-control violations | 0 / 2 |
| Explicit unsupported patterns | 7 |

The seven gaps are two fluent state-machine publishes, one saga event, two private
wrapper calls, and two AddDbContext registrations. Supported-roster recall is not
overall application recall. Source labels were frozen before the successor run.
Real EF metadata tests also cover two supported signatures and eight exclusions.

## Paired retrieval tasks

Four source-authored tasks cover two message contracts, an entity, and a service
interface. Each runs against two alternative context selections, with identical
published data and exact context IDs in both arms. The existing arm calls
`compiler_contract_impact` once and `compiler_definitions` for each of three
contexts. The new arm calls `compiler_contract_context` once.

All eight comparisons preserve the same evidence, source/owner provenance, and
definitions, and independently satisfy frozen source expectations. No response
truncates or exceeds the task's 12-call / 64 KiB response budget.

| Aggregate across eight comparisons | Existing tools | Combined tool |
| --- | ---: | ---: |
| Retrieval calls | 32 | 8 |
| Serialized complete tool-result bytes | 71,670 | 51,702 |
| Serialized structured-content bytes | 63,214 | 48,870 |

This is 75% fewer retrieval calls, 27.9% fewer complete-result bytes, and 22.7%
fewer structured-content bytes. Context/symbol discovery is outside these counts:
both arms start with the same known selections. Byte counts measure serialized
tool results, not network framing or model tokens. No latency or competitive
CodeGraph advantage is inferred from these measurements.

Discovery is also measured separately: four calls per arm total 6,232 old versus
9,418 new complete-result bytes (4,980 versus 8,002 structured bytes). The new
discovery includes both declaration-only component contexts for the service
interface; old impact discovery exposes only its API evidence context. Thus this
is a broader discovery answer, not an equivalent answer getting larger. Amortizing
one discovery per contract across the eight selected-context comparisons gives
36 versus 12 calls, 77,902 versus 61,120 complete-result bytes (21.5% less), and
68,194 versus 56,872 structured bytes (16.6% less). This amortization is not a
measured end-to-end agent journey or a guarantee for other selection patterns.

The production API independently bounds materialization to 2 MiB, records to 100,
and posting/definition work to 10,000 rows. It reserves separate evidence and
definition capacity and discovers definition-only contexts. Multiple captures of
one repository/project cannot be selected together. See [ADR 0040](../../docs/adr/0040-compact-contract-context-and-ef-observations.md).

## Evidence and remaining work

The [successor evidence directory](results/application-outbox-v2-20261001/)
contains application and paired-task reports. The persistent ignored workspace
`.local/application-impact/run-v2/` retains capture inputs, artifacts, publication,
and stage commands. Historical baseline files remain unchanged.

Normal and race application, paired-task, and discovery reports agree. The full
Go suite, build, vet, and focused race suites for semantic validation, import,
indexing, and MCP pass after restart. Regression tests cover worker-version
downgrades with recomputed checksums, shared-payload validation, definition-only
discovery and variant rejection, cancellation, ownership after index closure,
lease release, and shared result/work/byte limits.

Remaining work includes the seven selected framework/wrapper gaps, broader
independent applications, discovery-inclusive task journeys, latency/scale
evaluation, and an actual matched CodeGraph comparison. This closes one bounded
S4 retrieval slice; it does not complete the broader semantic-intelligence goal.
