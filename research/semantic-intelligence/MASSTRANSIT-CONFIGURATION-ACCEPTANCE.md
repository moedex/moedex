# MassTransit configuration acceptance

Recorded October 1, 2026. Locally verified; not deployed.

Worker 5 records two exact MassTransit metadata APIs as distinct configuration
facts. A fluent publish factory produces `message_publish_configuration`; a saga
event expression/correlation call produces `message_event_configuration`. Message
identities come from bound generic arguments. These observations establish neither
execution/delivery nor consumer activation. See
[ADR 0042](../../docs/adr/0042-masstransit-configuration-evidence.md).

## Frozen external-sample gate

Pinned Sample-Outbox commit `1ab8e66ebf96e5733e68c2f4d2201276f38ed9c5`
runs unchanged through capture, composition, publication, and leased MCP queries.
The independent evaluator froze [source gold v4](results/application-outbox-20261001/source-gold-v4.json)
before capture, SHA-256
`35744683f93f9ae7fb83ef17455b11da7d38a067951feb26acf0ad415e1d8586`.
Exactly three configuration sites are promoted; all previous gold and evidence
remain preserved.

| Measure | Result |
| --- | ---: |
| Supported source observations | 15 / 15 |
| Context-specific observations | 26 / 26 |
| Audit facts preserved through MCP | 26 / 26 |
| False positives / false negatives | 0 / 0 |
| Negative-control violations | 0 / 2 |
| Selected unsupported wrapper cases | 2 |

The remaining controller and validation-service wrapper calls dispatch through
interfaces. They remain explicit gaps. Supported-roster recall is not overall
application recall, and this is not a matched CodeGraph comparison.

## Retrieval and regression checks

The frozen [task successor](results/application-outbox-20261001/impact-task-gold-v2.json),
SHA-256 `04ae12b87c1819548380387b0f46202dba1a1dc6e8890d42efe680cd082b7f13`,
adds configuration evidence to affected tasks while preserving the original task
gold. Set `MOEDEX_APPLICATION_TASK_VERSION=2` to replay it. Four tasks across two
context selections retain exact evidence and definition parity between the old
tool sequence and combined context tool.

| Across eight selected-context comparisons | Existing tools | Combined tool |
| --- | ---: | ---: |
| Calls | 32 | 8 |
| Serialized complete-result bytes | 83,798 | 59,960 |
| Serialized structured-content bytes | 75,342 | 57,128 |

These numbers exclude separately reported discovery and transport framing. They
measure the larger successor evidence set and should not be treated as a speed
comparison with earlier captures. Source/owner provenance and configuration kinds
are checked independently; configuration cannot score as a direct publish or
consumer observation.

The real metadata fixture passes six supported configurations, eight overload,
async, generic, and lookalike exclusions, and two unresolved-call exclusions.
The unresolved capture intentionally returns the incomplete-compilation status;
the harness asserts that status and the absence of facts. Existing worker tests
pass. Go tests cover exact API/shape checks, old/new worker admission, and
checksum-recomputed mapped-index downgrades for both new kinds, including shared
payloads. Full Go tests, build, vet, and affected-package race checks pass.
Normal and race application, task, and discovery reports agree. The earlier EF
metadata gates also pass under worker 5; their explicit expected-worker version
parameter preserves the default worker 4 replay assertion for the v3 fixture.

The [v4 evidence directory](results/application-outbox-v4-20261001/) contains
application/task reports and reproduction metadata. Capture inputs and published
data remain in ignored `.local/application-impact/run-v4/`. No GPU work is used.

Next work requires explicit compiler interface-member implementation evidence and
bounded source-linked call paths for wrappers. Broader applications, scale,
latency, and a matched competitive gate remain independent acceptance work.
