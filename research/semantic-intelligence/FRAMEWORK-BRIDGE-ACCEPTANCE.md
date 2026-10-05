# Closed interfaces, default templates and keyed registration

Date: 2026-10-01. Single-threaded development continuation; no subagents, no GPU,
embeddings disabled. The tuned eShop sample is now development data, not a reserve
holdout. Independent agent coverage remains **3/12**.

## Delivered

Worker7 adds compiler evidence for the selected eShop Webhooks patterns:

| Source pattern | New selected observations | Public verification |
|---|---:|---|
| Closed `IIntegrationEventHandler<Event>.Handle` correspondence | 3 | Distinct qualified identities, declarations, reverse implementation queries |
| Explicit default implementation of nongeneric `Handle` | 1 | Default declaration, reverse query, exact binding of its generic forwarding call and source cast |
| `AddSubscription<Event, Handler>` keyed configuration | 3 | Exact service/handler/key/API targets, lifetime, source helper definition/body and key-type impact |

All seven selected observations are available through existing public compiler
tools. One additional closed `IConfigureOptions<SwaggerGenOptions>` correspondence
is inventoried separately; it is not added to the frozen denominator. No custom
eShop type is relabelled as a MassTransit consumer.

The new source helper rule follows actual compiler-bound code, not the name
`AddSubscription`. A fixture with a differently named helper also succeeds.
The exact metadata API witness remains separate from service/implementation/key
identities. A registration fact does not establish final container state, event
name routing, broker delivery or handler activation.

The default method remains an **open interface template**. Its source forwards
through `(TIntegrationEvent)` into the generic member. This does not select a
closed class implementation. Automatic contract-path implementation hops explicitly
exclude default templates; reverse lookup still exposes them for inspection.
Full automatic closed default dispatch remains future work.

See [ADR 0047](../../docs/adr/0047-closed-interface-and-keyed-registration-evidence.md)
for the supported subset, identity format and compatibility boundary.

## Pinned source and capture

- Official dotnet/eShop commit `f2369529433374a01b864b6fa1499ad894756f53`.
- Same five-project Webhooks closure and 57 reviewed authored C# files; all source
  hashes and the tracked checkout remain unchanged.
- Worker7, SDK8.0.400/Roslyn4.11 assets, net8.0 Debug; frozen offline dependency bundle.
- Final capture: 5 complete contexts, 73 captured sources including generated
  inputs, 1,484 symbols, 3,946 bindings, 67 hidden diagnostics, zero warnings/errors.
- Final artifact SHA-256:
  `fecd3b53d7d2af58297fdf3cda103685c2643a34825481d178ebe16ff0ae33a8`.
- Separate snapshot under `.local/framework-bridge/eshop-release/index`; prior
  worker6 snapshots remain untouched. Existing index layout v5 is reused. New
  evidence requires recapture; it cannot be recovered by reindexing a worker6 artifact.

## Public workflows

Each source-authored workflow starts with an event/interface name and a public
catalog. Paths, compiler IDs, source hashes, offsets and contexts come from public
responses. The registration workflow verifies complete raw source bytes with or
without a BOM before computing its call offset. Qualified key identity matches
the closed interface argument, and the registration's implementation ID matches
the class identity in the compiler correspondence.

| Workflow | Calls | Full response bytes |
|---|---:|---:|
| Paid event | 14 | 62,697 |
| Shipped event | 13 | 60,321 |
| Price-changed event | 12 | 60,736 |
| Default template | 6 | 31,494 |
| Total | 45 | 215,248 |

Each remains within its own 24-call/131,072-byte budget. This is a scripted
regression, not a blind solver score or model-token/latency comparison.

Two development attempts are retained: the first assumed handler basenames were
unique across eShop; the second used a broad two-term search that returned handler
code before registration code. The corrected workflow checks compiler coverage
for same-name candidates and scopes registration search to the returned project.
These retries are not folded into an independent success rate.

## Validation

- Native SDK8 fixture: two distinct closed handlers and matching invocations,
  one default template, four direct/helper keyed cases, nine unsupported controls,
  and suppression of interpreted facts for incomplete compilation.
- Fixture output passes import, artifact write/read, mapped-index build/open,
  public symbol discovery, reverse implementation and key-type impact queries.
- Frozen eShop regression: all nine previously supported assertions and both HTTP
  negative controls still pass. The original five unsupported domain labels stay
  unchanged; new method correspondence is reported separately from those labels.
- Preserved worker6 Outbox/eShop reverse-discovery regression still passes.
- SDK10 worker integration passes its 19 labelled cases plus context separation,
  raw UTF-8 offsets, capture integrity, deterministic output, configuration changes,
  incomplete restore, outside-source rejection, aliases, primary constructors,
  generators, generator failure and concurrent input mutation.
- Full `go test ./...`, `go vet ./...`, `go build ./...`, focused race tests across
  semantic/import/index/MCP/serving packages, the default-path guard race test,
  and 12 Python client/workflow tests pass.

Early failures are retained: SDK selection in disposable fixtures, the index
reader's old descriptor allowlist, the reverse tool's old identity/version checks,
a test expecting `matches` instead of contract-impact `paths`, and the explicitly
worker6-only gold replay before making its expected worker version configurable.
The default remains worker6 for preserved replay. No original gold labels changed.

## Remaining boundaries

Generic classes/methods, nested interface containers, array/open/constructed generic
arguments, inherited class correspondence, arbitrary helper control/data flow,
recursive helpers, factories and arbitrary DI keys remain excluded. Callback
`Entity<T>` and Aspire DbContext registration are still outside this cycle.
The helper summary does not interpret the event-name dictionary or RabbitMQ
activation path. Default-template exposure does not close automatic class dispatch.

No new large-corpus resource claim, fresh independent agent score, paired CodeGraph
execution or PROGRAM30 completion is claimed. Next work should explicitly address
closed default selection/forwarding and generalization on fresh source before any
competitive quality claim.

Hashed evidence inventory (archival evidence maintained separately) includes
all public transcripts, final source copies, protocol, native observation inventory,
validation logs and artifact/binary provenance. Corpora, packages, indexes and
binaries stay under ignored `.local/` storage.
