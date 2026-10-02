# Independent Webhooks capability holdout

Recorded October 1, 2026. Local capture and serving verification; not deployed.

Unchanged Moedex worker6 source captures all nine selected supported observations
in eShop's Webhooks.API closure. Five unsupported patterns remain explicit gaps;
two HTTP negative controls produce no messaging assertions. The first public MCP
run exposes six of those observations and the declarations for three method
implementations. A separately recorded public API successor now exposes all nine
from the same artifact; the original baseline remains unchanged.

## Source and frozen labels

The independent repository is official `dotnet/eShop`, release/8.0 commit
`f2369529433374a01b864b6fa1499ad894756f53`. The selected Webhooks.API closure
contains five projects and 57 C# files, including two linked shared sources.
It excludes the full Aspire solution, MAUI workloads, and Catalog's ONNX path.
This is a selected capability holdout, not whole-eShop recall or an agent journey.

The [source labels](results/eshop-webhooks-holdout-20261001/source-gold-v2.json)
were frozen and independently reviewed before capture. SHA-256:
`8d703f654a091d8c803b173612d3af4401b7e1034a1eb2ba20d34d3f00ac4f62`.
Original gold is retained. A pre-capture successor corrects only the owner of
the `DbSet` type reference: the existing binding convention identifies the
containing class, not the property's declaration. That correction used an older
fixture's documented evidence convention, not Webhooks extraction output.

| Frozen case family | Native artifact | First public MCP run |
| --- | --- | --- |
| DI registrations | 3 exact | 3 exact |
| EF entity property | 1 exact | 1 exact |
| Interface calls | 2 exact | 2 exact |
| Method implementation relationships | 3 exact | 3 declarations only; relationships unavailable |
| Unsupported patterns | 5 explicit gaps | 5 explicit gaps |
| HTTP negative controls | 2 without forbidden assertions | 2 without forbidden assertions |

The unsupported cases comprise three custom integration-event handlers, an Aspire
DbContext registration, and the callback `Entity<T>` mapping overload. They receive
no credit as successful negatives. HTTP `SendAsync` is not relabeled as a message
publisher. Generic/default-interface dispatch, source-defined event registration,
and HTTP transport paths remain separate capabilities.

Thirteen additional native facts outside the selected positives are retained and
independently reviewed as source-consistent: seven interface implementations and
six DI/EF facts. They do not expand the frozen success denominator or establish
broad precision.

## Capture and compiler alignment

SDK 8.0.400 builds the pinned five-project closure with zero warnings/errors.
The original worker binary uses Roslyn 5; with SDK 8 evaluation it rejects a
generated configuration-binding interceptor with `CS9137`, making EventBusRabbitMQ
and its Webhooks dependent incomplete. The capture correctly refuses publication.
The failed attempt and diagnostic stream are retained.

Rebuilding byte-identical worker6 source against SDK 8's matching Roslyn 4.11
libraries resolves this environment mismatch. SDK 8's different build-host layout
requires staging unchanged official files into the layout expected by the existing
worker project. Source, staged dependency mappings, binaries, and compiler versions
are hashed. The application, detector rules, and frozen labels are unchanged.

The accepted production capture has five complete contexts, 73 sources, 1,479
symbols, and 3,946 bindings. Its 67 compiler diagnostics are hidden diagnostics;
there are zero warnings or errors. All 57 reviewed source hashes are present, and
all 1,057 tracked repository files remain unchanged. Artifact SHA-256:
`0690938d31c51740216d456cdaa6ce079f2133c08323988bef78ff52b0cf76bf`.
The production snapshot contains 828 indexed files and deliberately omits the
syntax graph; this gate is about compiler evidence, not graph-scale acceptance.

The SDK archives have verified official checksums. An offline bundle retains 135
exact NuGet packages with source and hash provenance. Initial restore diagnostics,
including the pinned dependency advisory failure, remain archived. Reproduction
uses the existing capture pipeline's `NuGetAudit=false` setting without changing
package versions. This establishes compilation reproducibility, not deployment
readiness of the sample dependencies.

## Public serving baseline

The frozen public gate loads the actual published snapshot through serving leases.
It performs 16 context-discovery calls and 16 exact source-binding calls, checks
source/context/snapshot identities and typed targets, and retains complete response
hashes. Normal and race runs produce identical reports. The 32 complete serialized
responses total 43,605 bytes; this is a descriptive gate cost, not a paired task
efficiency or latency claim. The affected serving package suite also passes.

The baseline exposed a product gap: the index retains implementation facts, but
the binding/definition response omits them. Existing publisher-path responses
can expose the relationship only when a direct publisher seed exists. These
Webhooks cases have no such seed. Native success therefore does not imply public
access to those relationships. The original gate source and results are archived
before any public API successor.

See the [baseline evidence manifest](results/eshop-webhooks-holdout-20261001/result.json)
for native and public reports, source gold, extras review, compiler alignment,
dependency provenance, failed attempts, and replay commands. Persistent raw inputs
remain under ignored `.local/application-impact/independent/`. No GPU is involved.

## Public implementation evidence successor

The separate public projection change exposes existing implementation facts and
a deduplicated symbol table in binding and definition responses. It changes no
detector, artifact, or index format. See
[ADR 0044](../../docs/adr/0044-public-compiler-implementation-evidence.md).

Replaying the same artifact now returns all nine supported assertions publicly.
The three implementation cases also pass exact binding-versus-definition response
parity. This successor runs 35 calls: the original 32 discovery/binding queries
and three definition lookups. Complete responses total 54,575 bytes. Normal and
race reports agree exactly. Five gaps and two clean HTTP controls remain; the
first public baseline is preserved rather than relabeled as nine successes.

Existing Outbox wrapper paths also pass all eight positive/negative queries with
normal/race response equality after the additive projection. Full Go tests, vet,
build, MCP tests and focused race checks pass. Malformed symbols, inconsistent
provenance, missing/duplicate references, oversized evidence and legacy omission
are tested; review found and corrected a required-symbol-text validation gap.
The [successor evidence manifest](results/application-implementation-public-20261001/result.json)
records the replay and checks. This is a regression result on now-observed source,
not a new held-out quality result or an agent journey.

Next work is reverse interface-to-implementation discovery, generic source
relationships and discovery-inclusive agent journeys. A known implementation's
interface correspondence is now accessible; finding unknown implementations from
the interface remains a separate query capability.
