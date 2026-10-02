# Compact semantic artifacts and paired-result accounting

2026-10-02 — development acceptance; no new heldout or paired quality score.

Quartz now completes compiler capture, inspection, publication and native MCP
queries. The blocker from the preceding analyzer milestone is closed. One
explicitly authorized subagent independently implemented the offline comparison
harness while the coordinator implemented storage.

## Storage result

| Measure | Bytes |
| --- | ---: |
| Expanded JSON payload | 169,213,758 |
| Legacy envelope of this exact payload | 225,618,473 |
| Compact envelope | 56,658,037 |
| File limit, unchanged | 67,108,864 |
| Maximum expanded JSON | 268,435,456 |

The same-payload reduction is **74.89%**. The prior capture's encoded size was
225,620,341 bytes; it is a different extraction and is not used as the exact
compression denominator. Artifact SHA-256:
`955f50ee6b89e5d07b9deadc1c968a56f534757056152c7e5b8371d268de5042`.

Pinned Quartz revision `15d90a9c2681cd9e273dcd3901c0fbbdbdc5fe90`, worker21,
SDK10.0.401 and the existing offline dependency bundle produced one complete
context, 702 sources, 31,881 symbols, 202,888 occurrences and bindings, and 72
retained diagnostics. Capture took 52.73 seconds and snapshot publication 19.38
seconds in this single development run; these are not controlled performance
comparisons or peak-memory measurements. The worker transport allowance was the
existing explicit 256 MiB option. No GPU or embeddings were used.

Two native MCP calls on the final CLI discover `Start` in `StdScheduler.cs` and
resolve its definition. Both identify the same snapshot/artifact, preserve
worker21 provenance, and match the canonical source hash. The local server was
stopped. The source-generator fixture also passes Debug/Release capture and
publication, rejects a changed analyzer binary, and preserves its three expected
failure controls (broken build, missing analyzer, ambiguous framework output).

## Contract and checks

[ADR 0065](../../docs/adr/0065-compact-semantic-artifacts.md) specifies the new
storage envelope. Payloads above 64 KiB use deterministic gzip. Payload identities
and schema remain unchanged; legacy files remain readable. Tests cover exact
capture-byte preservation, exclusive publication, deterministic bytes, legacy
reads, encoded/expanded boundaries, false length declarations, corruption,
truncation, concatenated streams and compressed trailing bytes.

Composition retains its existing conservative 64 MiB aggregate canonical legacy
envelope budget, with expanded input accounting before reading another artifact.
The large Quartz artifact can publish directly but does not fit that composition
budget. Older binaries cannot read compact envelopes. Readers materialize JSON
and Go objects: serialization limits are not heap ceilings. No global source
budget or warm semantic-index limit was raised.

Validation passed: full `go test ./...`, `go vet ./...`, focused race tests for
`internal/semantic` and `internal/app/semanticcmd`, the offline project-analyzer
fixture, final artifact inspection, publication and native MCP smoke. One initial
inspection used an unsupported `--input` flag; its error and the corrected
successful invocation are both retained. No compiler worker behavior changed.

## Comparison harness

[Comparison format and usage](agent-journeys/COMPARISON.md) describe `compare.py`.
It accepts independently collected native-arm records under one frozen corpus,
prompt, rubric, protocol and budget contract. It verifies evidence hashes,
product/prompt/answer/review bindings and task coverage. Planned, assigned,
blocked, unassigned and eligible task counts remain distinct. Paired deltas use
only mutually eligible tasks; unknown transport bytes or budget violations cannot
turn into zero-cost wins. Final reviewer identity must differ from solver identity.

All **35 journey-harness tests**, including 11 new comparison tests, pass. These
are synthetic validator tests, not Moedex/CodeGraph measurements. Supplied audit
claims still require independent transcript review; hashes alone cannot prove
truth or independence. CodeGraph dependency/provenance and isolated native setup
remain open. A fresh corpus and independent execution are still needed for new
heldout evidence after development tuning.

## Evidence

[Sealed result manifest](results/compact-artifacts-20261002/result.json) records
source snapshots, tests, capture/publication commands, fixture cases, exact size
accounting, raw native MCP exchanges and prior-archive integrity checks. Large
local artifacts, dependencies and binaries remain under `.local/compact-artifacts`
and are represented by hashes rather than copied into the research archive.
Historical holdout prompts, scores and archives are unchanged.
