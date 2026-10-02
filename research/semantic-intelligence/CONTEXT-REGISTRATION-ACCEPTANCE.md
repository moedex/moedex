# EF context-registration acceptance

Recorded October 1, 2026. Locally verified; not deployed.

Worker 4 adds `storage_context_registration` facts for the exact single-type EF
`AddDbContext` overload taking an options callback. It records the context service
and implementation identity plus a compiler-constant context lifetime. The
optional callback may be omitted when the compiler selects that same overload.
Options lifetime, provider configuration, and runtime service activation are not
inferred. See [ADR 0041](../../docs/adr/0041-compiler-context-registration-evidence.md).

## Source-authored successor

The unchanged Sample-Outbox commit
`1ab8e66ebf96e5733e68c2f4d2201276f38ed9c5` was recaptured, composed, published,
and queried through leased MCP. Before capture, the independent evaluator froze
[source-gold-v3.json](results/application-outbox-20261001/source-gold-v3.json),
SHA-256 `29848c766c675f261ef2204eaf93ab74753432705da5622c4f46f6f57f26cece`.
It promotes only the API and worker context-registration sites; earlier gold files
and result directories remain unchanged. Both omitted lifetime arguments are
independently grounded in the pinned EF 8.0.4 API's scoped default.

| Measure | Result |
| --- | ---: |
| Supported source observations | 12 / 12 |
| Context-specific observations | 20 / 20 |
| Audit facts preserved through MCP | 20 / 20 |
| False positives / false negatives | 0 / 0 |
| Negative-control violations | 0 / 2 |
| Selected unsupported patterns | 5 |

The five remaining cases are two state-machine fluent publishes, one saga event
configuration, and two private-wrapper calls. This score covers the frozen
supported roster; it is not overall application recall or evidence of a
competitive win.

## Verification

Real EF metadata fixtures pass seven supported lifetime/registration cases and
ten exclusions. They distinguish runtime context lifetime (excluded) from runtime
options lifetime (context fact remains valid), positional/named/default constants,
alternate overloads, factory/pooling APIs, generic targets, and lookalikes.
The full worker regression suite passes.

Go tests reject conflicting service/implementation targets, invalid lifetime or
attributes, mismatched APIs, and checksum-recomputed worker downgrades, including
shared decoded payloads. Query tests ensure the two matching target roles produce
one observation rather than duplicates or false truncation. Adversarial scorer
tests reject wrong lifetimes, missing roles, wrong rule generations, and invented
table claims.

Full Go tests, build, vet, and targeted race checks pass. Normal and race
application, paired-task, and discovery reports agree. The previous four frozen
paired retrieval tasks also pass against the new capture. Current evidence and
reproduction metadata are in the [v3 result directory](results/application-outbox-v3-20261001/).
Persistent capture inputs and publication are retained in the ignored
`.local/application-impact/run-v3/` workspace. No GPU work is involved.

Next is a separately labelled MassTransit configuration slice; private-wrapper
propagation and broader independent application/competitive gates remain separate.
