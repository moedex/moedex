# Public agent journeys: complete worker18 development run

Date: 2026-10-01 (local; execution timestamps use UTC on October 2).
Status: **12/12 tasks executed and independently reviewed**.

The separate current-build run scores **66/67 correctness (98.5%)**, **64.5/67
citation evidence (96.3%)**, and **7/12 strict task successes**. No material
unsupported claims were found. This closes the unfinished development-task
execution gate; it does not establish heldout quality or a competitive win.

Evidence: [result manifest](results/agent-journeys-worker18-20261001/result.json),
[per-atom adjudication](results/agent-journeys-worker18-20261001/adjudication.json),
[logged-call audit](results/agent-journeys-worker18-20261001/accounting-audit.json),
and [source-block audit](results/agent-journeys-worker18-20261001/returned-source-audit.json).

## Execution and scoring

The user explicitly authorized fresh solvers and reviewers, one child at a time.
Each of twelve `fork_turns=none` solvers received one unchanged prompt, repository
label/pin, public catalog and logged-client instructions. No compiler IDs, source
paths, gold labels or previous answers were supplied. Each task then received a
fresh independent reviewer, blind to root's initial score and other task runs.
Both initial reviews are retained, with three atom-level disagreements resolved
explicitly. No solver was rerun.

The [frozen launch protocol](results/journey-readiness-20261001/protocol.json)
and original 67-atom source rubric were unchanged. Its awaiting-authorization
status is a historical preparation record; the actual authorization and run
timestamps are in [execution.json](results/agent-journeys-worker18-20261001/execution.json).
The current product uses worker18, SDK10/Roslyn5, the composed Outbox capture and
CPU-only HTTP MCP with embeddings disabled. All 40 source hashes match the rubric.

| Task | Correctness | Evidence | Strict success |
| --- | ---: | ---: | :---: |
| Duplicate handling | 4/4 | 4/4 | Yes |
| Deliberate retry failure | 5/5 | 5/5 | Yes |
| Validation registration and caller | 5/5 | 4.5/5 | No |
| Creation, publication and persistence | 5.5/6 | 5.5/6 | No |
| Notification flow | 6/6 | 6/6 | Yes |
| Email configuration and consumer | 6/6 | 6/6 | Yes |
| Validation flow | 6/6 | 5/6 | No |
| Runtime exactly-once proof limits | 5/5 | 5/5 | Yes |
| Submitted-registration contract impact | 6/6 | 6/6 | Yes |
| Validation-signature impact | 5/5 | 4.5/5 | No |
| Entity-uniqueness impact | 5.5/6 | 6/6 | No |
| Attendee-contract impact | 7/7 | 7/7 | Yes |
| Total | **66/67** | **64.5/67** | **7/12** |

Strict success requires every atom to earn full correctness and evidence credit,
with no material unsupported claims and the applicable runtime distinction stated.
The five non-strict tasks have specific gaps:

- Validation registration and signature impact contain off-by-one registration
  citations. Their underlying source claims are correct.
- Persistence omits the API's EF bus-outbox configuration, although it correctly
  denies a runtime transaction guarantee from source ordering alone.
- Validation flow cites a 64-line configuration block for two registration facts.
  The independent reviewer accepted the exact returned span; adjudication retains
  partial evidence credit, consistent with the original baseline's ruling on the
  same block. Both initial judgments remain visible.
- Uniqueness impact does not explicitly say that the mapping lacks an existing
  unique index on `RegistrationId`. Its complete mapping citation supports that
  fact, so adjudication accepts the independent reviewer's full evidence credit
  while retaining partial correctness credit for the omitted statement.

## Retrieval and integrity observations

The twelve runs make 74 attempted public calls and receive 605,020 complete
serialized response bytes. One `trace_calls` request returns the explicit
`graph_unavailable` result because this snapshot intentionally omits the graph
sidecar. The solver continues through source tools and discloses incomplete
caller coverage. All twelve `list_repos` and 25 `read_source` requests succeed;
the original pilot's closed-discovery defect does not recur in these requests.

Four solvers independently discover and use compiler tools: notification flow,
validation flow, submitted-contract impact and attendee-contract impact. They
use contract context, candidate paths or exact property bindings without supplied
IDs, and preserve compile-time/runtime distinctions. This demonstrates use of the
public workflow; without an ablation it does not measure its causal benefit, and
these tasks do not exercise every worker18 framework rule.

Every task remains below 24 calls, 131,072 response bytes and 600 seconds. Observed
maxima are 12 calls, 89,052 bytes and a 277.68-second assignment-to-completion upper
bound. Timing includes coordinator observation overhead and is not a controlled
latency benchmark. Model-token accounting and exact model build are unavailable.

All request/response hashes and byte accounting verify. All 200 returned source
blocks match the pinned checkout (line text, with BOM/newline handling). All 74
responses retain a consistent corpus identity, and compiler responses name the
frozen artifact. Post-run hashes verify unchanged binary, worker, artifact,
client, catalog, rubric and corpus. The serving process was stopped after solving;
reviewers use retained evidence. Preparation's twelve harness tests also remain
applicable to the unchanged client.

## Limits and next work

Isolation is instruction-enforced, not an OS boundary. Full model/tool activity
is not exposed for independent auditing; RPC logs and solver attestations are
retained. All solvers report initial catalog-display truncation followed by a
compact reread. Signature-impact and attendee-impact also report truncated
response displays despite complete raw logs, so the full-display instruction was
not fully met in those two runs. Excluding both yields 54/55 correctness, 53/55
evidence and 6/10 strict successes. This sensitivity slice is not a replacement
run, and no missing evidence is reconstructed into a solver answer.

The original partial baseline remains immutable: three tasks executed, two strict
successes, correctness 14/14 and evidence 13.5/14. Scores from that earlier binary
are not pooled here. This corpus was used during development; neither these
results nor the separate 19/19 framework-observation gate satisfy the paired,
heldout PROGRAM30 target. No CodeGraph arm or GPU/dense evaluation ran here.

Next work should make precise line citations easier to produce, reduce oversized
catalog/response displays, and make graph capability absence clear before a
traversal request. Then freeze unseen application tasks that exercise broader
framework patterns and run the heldout/paired evaluation gates. Preserve this
run when making those changes; do not repair its answers or scores retrospectively.
