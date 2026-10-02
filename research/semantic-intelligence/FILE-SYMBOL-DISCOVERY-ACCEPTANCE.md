# File-scoped compiler discovery and task walkthroughs

Date: 2026-10-01. Completed single-threaded, CPU-only, embeddings disabled.

## Result

`compiler_symbols` discovers exact recorded declaration IDs from a public source
path and an optional descriptor fragment. It returns raw spans/hashes and separate
context alternatives without full-file retrieval or manually computed offsets.
It uses existing position postings; no artifact recapture, index rebuild or format
change. See [ADR 0046](../../docs/adr/0046-file-scoped-compiler-symbol-discovery.md).

On the same final executable, public workflows return identical symbol IDs,
context IDs, raw source hashes and offsets using either approach:

| Workflow | Previous calls | New calls | Previous bytes | New bytes |
|---|---:|---:|---:|---:|
| Validation interface | 11 | 6 | 45,929 | 40,822 |
| Submission interface | 11 | 6 | 41,413 | 36,401 |
| BOM-prefixed class | 8 | 3 | 47,161 | 39,397 |
| Total | 30 | 15 | 134,503 | 116,620 |

Both interface workflows continue through reverse implementation discovery and
selected queries for both recorded contexts. The class retains raw offset 230,
including its three-byte BOM. Search parameters and corpus are unchanged. This
is a scripted call/byte comparison, not a model token, latency or agent-quality
benchmark. Search responses still dominate returned bytes.

## Nine development walkthroughs

The remaining nine frozen Outbox prompts were walked through public search and
complete-source reads, with 76 calls returning 629,483 bytes across nine separate
budgets. Every task stayed below 24 calls and 131,072 response bytes. The scripts
use source-authored basename hints and evidence snippets. They check retrieval of
those snippets, not complete answer correctness or autonomous discovery. Root-authored
analyses accompany their precise source-line evidence in each task directory.

| Task | Calls | Bytes | Evidence covered |
|---|---:|---:|---|
| locate-persistence | 2 | 15,710 | Entity construction, insertion, publication, save order |
| flow-notification | 11 | 81,301 | HTTP call, interface/implementation, registration, publisher, consumer |
| flow-email | 7 | 59,160 | Saga publication configuration and logging-only consumer |
| flow-validation | 9 | 69,111 | Consumer, interface, implementation, DI, validated publication |
| flow-runtime-proof | 13 | 111,641 | HTTP success, saga/email code, retries, outbox and endpoint configuration |
| impact-submitted | 8 | 73,632 | Contract property, producer initializer, consumer type dependency, correlation |
| impact-validation-signature | 9 | 69,111 | Declaration, implementation, caller and registration |
| impact-entity-uniqueness | 6 | 52,413 | Composite uniqueness, identifier mapping, SQLSTATE translation, HTTP conflict |
| impact-attendee | 11 | 97,404 | Contract, two consumer declarations/registrations, configured publication |

The initial runtime-proof attempt used an incorrect scripted basename,
`RegistrationStateMachineDefinition`. Search returned the real
`RegistrationStateDefinition`, but the script's exact basename filter discarded
it. Both that failed assertion run and the corrected run are retained. This was
an authoring mistake, not a missing search result.

The source distinguishes configured routes from execution. In particular, the
email consumer only logs; source outbox/retry configuration does not establish
exactly-once email delivery. Changing `RegistrationSubmitted.RegistrationId`
affects producer initialization and saga correlation; the notification consumer
has a contract-type dependency without reading that property. Uniqueness handling
catches PostgreSQL UniqueViolation without testing a particular constraint name.

**Independent agent coverage remains 3/12.** These development checks do not add
independent scores, prove exhaustive impact coverage, satisfy PROGRAM30, or
establish a paired CodeGraph win. The root has prior source-gold knowledge.

## Bounds and validation

The new lookup seeks one exact repository/path, inspecting at most 10,000
occurrences including nonmatches. It returns at most 100 declarations within a
conservative 2 MiB materialization budget including context choices. Bound hits
report truncation, including empty partial results. Context choices cover only
returned declarations, not every captured context. No paging; late declarations
in a huge file may require the existing exact binding lookup. The materialization
budget is not a JSON-size or process-memory ceiling.

Tests cover exact scope, reference exclusion, case-sensitive matching, ordering,
context alternatives, result/work/byte bounds, cancellation, owned results after
close, argument/schema validation and lease release. Native public controls cover
truncation, absent names/files, invalid paths and unsupported context input.
Full `go test ./...`, `go vet ./...`, `go build ./...`, focused race tests for
semanticindex/MCP/serving, and 12 Python client/workflow tests pass.

The first baseline revealed that the previous evidence archive contained `.go`
source copies that the root Go module attempted to compile. Adding a nested
`go.mod` isolates those audit copies. Every previously archived byte/hash remains
unchanged; the added module marker is recorded separately. The full baseline
passes after isolation. No large Roslyn resource-gate rerun is claimed: this
change adds no eager catalog or serving allocation at startup.

## Evidence and next work

[Machine-readable result and hashed file inventory](results/file-symbol-discovery-20261001/result.json)
retain both walkthrough attempts, final same-binary comparisons, public controls,
source copies, test logs and binary identities. Source copies in this archive use
`.txt` suffixes to avoid accidental compilation. Corpora, indexes and binaries
remain under ignored `.local/` storage.

Next implementation work is the eShop closed-generic/default-interface bridge
and keyed registration evidence. Fresh independent tasks and an executable
CodeGraph production query path remain necessary before competitive scoring.
