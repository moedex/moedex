# Production compiler capture composition

Recorded October 1, 2026. Locally implemented and verified; not deployed.
See [ADR 0039](../../docs/adr/0039-compiler-capture-composition.md) and the
[command examples](../../internal/semantic/README.md#compose-separate-captures).

## Delivered behavior

`semantic compose --input A --input B --output C` stages a deterministic complete
artifact from independently validated captures. Exact repeated records deduplicate;
contradictory records or bindings fail. It preserves qualified symbols, snapshots,
build contexts, dependencies, generated source bytes, domain facts, and diagnostics.
Records own their data. Composition does not assert runtime or dependency-closure
compatibility. The command prints source snapshot IDs and contributing input paths
to support subsequent workspace mapping; paths are not embedded in the artifact.

The command accepts up to 32 inputs. Both actual aggregate file bytes and canonical
input envelope sizes are bounded by 64 MiB, including repeated inputs; output has
the same bound. Callers can lower it. This is a serialized-data bound, not a heap
limit. Malformed/incomplete inputs, contradictions, cancellation, and existing
outputs fail without replacing staged artifacts or CURRENT.

Snapshot publication accepts `--semantic-workspaces FILE`, a JSON map from every
artifact source snapshot ID to its retained directory. Keys must be exhaustive
and unique; unknown IDs are rejected. Relative paths resolve beside the map file.
The map is a local publication input. Existing single-workspace publication and
corpus-directory validation remain available.

Publication preserves all existing repository, commit, project, privacy,
retained-input, and indexed-source checks. A composition can retain alternatives
that cannot attach to the same current corpus; publication then rejects it.
Explicit context selection and same-project variant rejection remain enforced
by the contract-impact tool.

## Acceptance gates

The independently labelled six-project real framework fixture now uses the
production composition command adapter instead of a test-only union. Debug and
Release captures compose into two source snapshots and twelve build contexts.
The eleven existing contract-impact queries still check exact typed joins,
same-name distractor exclusion, variants, source/owner evidence, and truncation.

A separate gate copies retained captures into distinct directories, creates a
Git corpus of the fixture source, composes both artifacts, and invokes production
snapshot building with an exhaustive workspace map. Publication creates CURRENT
for a 13-file, one-repository snapshot. The real serving holder then resolves that
published snapshot, leases its mapped compiler index, and runs MCP discovery,
selected-scope impact, and conflicting-variant rejection. Provenance is checked
against the published manifest. This exercises local publication and real serving
leases; it is not a deployed daemon or network transport trial.

A hermetic two-repository gate separately checks composition and successful
publication through distinct workspace roots. Missing mappings and changed
retained sources reject replacement and preserve CURRENT. Parser tests cover
unknown, duplicate, missing, null, malformed, oversized, and trailing map content.
Core/app/CLI tests cover deterministic order, overlapping dependencies, deep-owned
records, conflicting bindings, byte-boundary accounting, cancellation, strict
input decoding, and concurrent exclusive output publication.

A compiled-CLI smoke test reverses the two input paths and produces the exact
SHA-256 of the published composed artifact, confirming input-order determinism
through the user-facing command.

Full Go tests, vet, and build pass. Changed-package race checks cover semantic
artifacts/import, command adapters, CLI, publication, snapshots, and serving.
Tagged LSP compiler checks pass. The real composition/publication, leased MCP,
and eleven-query contract gates also pass under race detection. Independent core
and publication review found no blocking issue.

The [machine-readable evidence](results/composition-20261001/result.json) records
source fingerprints, input/output artifact hashes, published manifest identity,
normal/race logs, and the prior capture/package evidence used by this gate.
Restored workspaces, package binaries, composed artifacts, and generated lexical
shards remain outside the repository. The real publication deliberately omits
the syntax graph, which is unrelated to compiler contract lookup.

To repeat the real publication gate, generate the contract fixture captures using
its existing harness, then choose a fresh output directory:

```sh
export MOEDEX_CONTRACT_CAPTURE_DIR=/scratch/contract-gold
export MOEDEX_COMPOSITION_OUTPUT=/scratch/new-composition-run
go test ./internal/app/indexcmd -run '^TestPublicComposedContractPublication$' -count=1 -v
go test ./internal/app/servecmd -run '^TestPublicComposedContractLeasedMCP$' -count=1 -v
go test ./internal/semanticimport -run '^TestPublicContractImpact$' -count=1 -v
```

The publication test requires an absent output directory and leaves its snapshot
for the subsequent serving test. Use another fresh directory when repeating the
publication gate with `-race`.

## Limits and next work

These fixtures are generated source against real pinned framework metadata, plus
hermetic publication cases. They establish the workflow, not held-out application
impact quality, runtime delivery, whole-CodeGraph coverage, or superiority.
Private framework wrappers remain unsupported without verified adapters.
Existing large Roslyn lexical/graph gates are unchanged and were not rerun for
this composition and publication slice.

The next priority is independently labelled application impact tasks using this
production workflow, measuring missed observations and unsupported wrappers as
well as false joins. Automatic capture orchestration and compatibility inference
remain separate work.

The production workflow is now exercised against an unchanged external application sample in the [application impact acceptance](APPLICATION-IMPACT-ACCEPTANCE.md), including a fix for unindexed optional build-input variants.
