# Compact evidence and fresh application baseline

Date: 2026-10-01. Status: compact retrieval accepted; fresh application baseline
recorded with one successful publication and one capture failure. Worker9 extraction
and the previously accepted indexes remain unchanged. This cycle used one agent,
CPU-only execution, and no embeddings.

## Retrieval result

`compiler_evidence_path` joins an explicitly selected class declaration, framework
observations naming the class, and recorded default-forwarding target, template,
and call witness in one immutable serving session. Symbol descriptors occur once.
The query does not infer runtime dispatch, cast success, message delivery, or a
compatible deployment from separately selected compiler contexts.

A paired public MCP comparison starts from the same previously discovered class
and context IDs. Both arms retrieve exactly the same five source records per
handler, including keyed registration. It verifies identical occurrence IDs,
source hashes, spans, implementation facts and registration facts.

| eShop handler | Previous calls / bytes | Compact calls / bytes |
| --- | ---: | ---: |
| Paid | 5 / 20,024 | 1 / 13,146 |
| Shipped | 5 / 20,072 | 1 / 13,170 |
| Price changed | 5 / 19,944 | 1 / 13,106 |

Across the three cases: 15 calls become 3; 60,040 response bytes become 39,422,
a 34.34% reduction. Discovery and source-text reading are excluded from both arms.
These measurements must not be compared directly with the earlier 8–10-call
end-to-end workflows, which also inspected reverse alternatives and source text.

The query shares a 10,000-row work budget, 2 MiB materialization budget and at most
100 records (default 20). The serialized **tool result**, including its envelope,
is capped at 64 KiB; JSON-RPC transport framing is outside that cap. Whole records
and unused descriptors are removed together when necessary, with `truncated=true`.
Cross-project call witnesses require explicit context selection. A class-only
query returns the missing context IDs without returning those witness records.
There is no recursive traversal or change to `compiler_contract_paths` semantics.

## Frozen applications

Expectations were written from source before the first capture, then hashed in
[pre-capture-freeze.json](results/fresh-paths-20261001/pre-capture-freeze.json).
No source expectation or extractor was adjusted to match output. These are
source-authored development baselines, not blind agent evaluations.

- [eShopOnWeb](https://github.com/dotnet-architecture/eShopOnWeb/tree/4da8212117e87d808d4bbc7da6286fd2147ce606):
  commit `4da8212117e87d808d4bbc7da6286fd2147ce606`, five-project Web closure,
  ten frozen anchors covering MediatR, repository and order-service flows.
- [ForkJoint](https://github.com/MassTransit/Sample-ForkJoint/tree/a0aa87647ea9124cb6d4051760e5f3908fb1ce91):
  commit `a0aa87647ea9124cb6d4051760e5f3908fb1ce91`, two-project API closure,
  eleven frozen anchors covering consumers, responses, registration and activities.

ForkJoint successfully captured and published with unchanged worker9 and its
frozen CLI. Artifact SHA256:
`5b50a58a0da90254f877d51c7351928558cab3c0ac0d5180d8180a376dfb1584`.
All eleven exact source anchors resolve through public MCP. Three interface
implementation facts and one conditional publish fact are recorded. Seven
requested domain observations are missing: typed response, consumer namespace
scan, activity namespace scan, conditional singleton registration, local-method
POST mapping, two-response request, and routing-slip activity configuration.
These remain explicit gaps rather than inferred runtime relationships.

**eShopOnWeb is a failed capture baseline, not a scored retrieval baseline.** Its
complete independently provisioned dependency cache is 1,166,409,179 bytes,
exceeding the current 1,073,741,824-byte bundle cap. A selected-package subset
passes packing but is not a faithful replacement for the full NuGet candidate
cache. An isolated worker diagnostic also finds five unresolved Razor-generated
component types and an incomplete dependent Web context. That diagnostic used a
provisioning copy with a pinned SDK file; it is diagnostic evidence, not a
published artifact of the unchanged commit. The ten source anchors remain
unmeasured through public MCP. The project closure and byte cap were not weakened.

## Provisioning findings and retained failures

The combined cache exceeded the bundle cap. Separating final resolved package
versions alone was insufficient: offline NuGet resolution also needs candidate
versions consulted during conflict resolution. ForkJoint's independently restored
complete cache packed successfully at 992,067,197 bytes (7,083 files); its manifest
SHA256 is `25ea4b2d1355655f472e09f95b8ce02e207f8785b5445e54dae15aadb7122f49`.

The combined SDK host initially selected SDK10 for restore despite an SDK8 worker
path. A separate host layout containing SDK8.0.400 and the required worker runtime
resolved that provisioning mismatch without editing application source. Earlier
restore/capture failures and the diagnostic host wrapper are retained. The first
compact comparison also caught an overly strict snapshot rehash: compact indexes
intentionally omit origin audit data, so admission now checks the retained
snapshot identity and record consistency without rehashing an incomplete snapshot.

## Validation and remaining work

Full Go tests with native forwarding enabled, `go vet ./...`, four-package race
checks, preserved worker6 published-index compatibility, and native worker7/8
compatibility passed. Tests cover shared work and record bounds, ownership,
cancellation, lease release, malformed readers, normalized schemas, serialized
truncation, and native forwarding witnesses. Public checks cover identical
five-record evidence and missing-context isolation on all three eShop handlers.
The frozen worker/CLI hashes, gold hashes and tracked application source bytes
were verified unchanged. Test servers were stopped after measurement.

Next priorities are faithful bounded dependency provisioning and Razor capture,
then the smallest independently testable ForkJoint domain gaps, starting with
explicit typed responses and conditional DI registration. Namespace scanning and
computed activity addresses require separate bounded rules. Independent solver
coverage remains **3/12**; paired CodeGraph evaluation and competitive superiority
remain unproved.

The reproducible evidence inventory is
[result.json](results/fresh-paths-20261001/result.json). Large corpora, dependency
caches, binaries and indexes remain under `.local/fresh-paths/`.
