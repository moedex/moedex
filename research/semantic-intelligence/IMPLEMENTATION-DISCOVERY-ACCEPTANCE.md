# Reverse implementation discovery acceptance

Recorded October 1, 2026. Locally verified; not deployed.

`compiler_implementations` now lets an agent start from an exact interface method
and discover captured implementation declarations. Index v5 provides a reverse
posting rather than scanning declarations. Queries retain qualified project or
assembly identity, source anchors and explicitly selected compilation contexts.
No new extractor behavior or runtime-dispatch inference is introduced. See
[ADR 0045](../../docs/adr/0045-reverse-compiler-implementation-discovery.md).

## Frozen application regression

The [query gold](results/implementation-discovery-20261001/query-gold.json) was
frozen before reverse-query outputs, SHA-256
`a3e991360154e516b6c151e44cfff3c1f1928b72fa22a134e5c3bbab7c9ca261`.
Existing source and native baselines informed it, so this is a development API
regression, not a new holdout or natural-language discovery benchmark.

Six interface identities cover three Webhooks service methods, two Outbox wrapper
interfaces, and metadata `IHostedService.StartAsync`. The metadata identity retains
its assembly version and public-key token. Both Outbox interfaces have two retained
context alternatives, queried separately.

| Check | Result |
| --- | ---: |
| Positive interface identities | 6 / 6 |
| Selected positive context queries | 8 / 8 |
| Positive context-discovery calls | 6 |
| Same-project alternative selections rejected | 2 / 2 |
| Preserved v4 snapshots report upgrade required | 2 / 2 |
| Unsupported bridge control calls | 2, no recorded evidence |
| Total calls, including discovery/rejections/controls | 20 |
| Complete serialized response bytes | 43,125 |
| Charged posting rows across those calls | 16 |
| Normal/race report equality | Exact |

The control starts from eShop's nongeneric `IIntegrationEventHandler.Handle`
bridge, whose concrete handlers implement the generic interface. The existing
worker excludes this generic/default-interface implementation shape. Empty
discovery and a selected query across all five contexts remain an explicit
coverage gap; they are not evidence that concrete handlers do not exist.
Same-name project/assembly separation is tested with adversarial fixtures rather
than inventing a collision in either application corpus.

## Publication and compatibility

The two production snapshots were rebuilt in fresh index directories from the
unchanged accepted compiler artifacts. Both previous v4 snapshots remain intact.
The Webhooks index grows from 1,616,363 to 1,616,619 bytes; Outbox grows from
781,284 to 781,396 bytes. These increases reflect a 16-byte section descriptor
plus 24 bytes per recorded implementation relationship. No compiler recapture,
package restore, source changes or GPU work is involved.

Versions 1–4 remain readable. Reverse lookup returns `index_upgrade_required`
there, distinguishing an unavailable capability from an empty supported result.
Snapshot manifests admit versions 1–5 and verify the writer/manifest version
agreement. The binary reader validates reverse-posting completeness, uniqueness,
sort order, fact ordinal and exact target identity before queries can run.

Existing public gates also pass on both new snapshots: all nine Webhooks
assertions with binding/definition parity, and all eight Outbox wrapper-path
queries. Normal and race reports match for both. The original independent
Webhooks baseline and public-projection successor remain unchanged.

## Bounds and validation

Discovery returns up to `min(limit, 32)` recorded context choices. It seeks past
duplicate declarations within a context. Selected queries accept up to 32 contexts,
reject alternatives of the same repository/project, and return at most 100
relationships. Results are bounded by 10,000 charged posting rows and 2 MiB of
estimated materialization, with explicit truncation and cancellation. The byte
budget includes full referenced symbols; it is not a serialized-output ceiling.
Reported rows do not include binary-search probes.

Tests cover large matching populations, exact/overflow context limits, metadata
and same-name identity separation, multiple projects, cumulative symbol costs,
malformed reverse directories, legacy binaries, lease release and cancellation.
Independent review corrected discovery-limit and public provenance validation
gaps before final acceptance. Full Go tests, vet, build, index/MCP race checks and
the public normal/race gates pass. A five-second reader fuzz run also passes;
its short duration is not a comprehensive fuzzing claim.

The [evidence manifest](results/implementation-discovery-20261001/result.json)
retains gold, reports, publication commands, artifact identities, checks and source
hashes. Raw snapshots remain in ignored `.local/application-impact/reverse-v1/`.

The next acceptance boundary is discovery-inclusive agent journeys that start
without supplied symbol IDs. This gate counts discovery costs but supplies exact
source-authored interface identities. Generic source relationships and
registration summaries remain separate unresolved capabilities.
