# Current-build journey evaluation readiness

Execution update: the user approved sequential fresh solvers/reviewers, and all
twelve tasks are now complete. See the
[worker18 acceptance](AGENT-JOURNEY-WORKER18-ACCEPTANCE.md). The preparation record
below and its frozen archive retain their pre-execution state; do not rerun the
initialized solver directories as fresh trials.

The twelve frozen Sample-Outbox tasks are ready for a separate current-build run.
No independent tasks have run in this cycle. The previous baseline executed three
of twelve tasks, with two strict successes, correctness 14/14 and evidence 13.5/14.
Those results remain unchanged and must not be pooled with this run.

Evidence: [readiness manifest](results/journey-readiness-20261001/result.json),
[frozen protocol](results/journey-readiness-20261001/protocol.json), and
[capture summary](results/journey-readiness-20261001/capture-summary.json).

## Verified preparation

- All 40 checked-in file hashes match the original source rubric and pinned commit.
- API and Worker captures compose into four complete worker18 contexts. The local
  CPU-only snapshot publishes 40 files, with embeddings disabled.
- The SDK-matched worker uses Roslyn 5.0 and SDK 10.0.100. An initial attempt using
  the pinned Roslyn 4.11 worker with SDK10 failed with a StringTools assembly
  mismatch; its logs are retained. This is a recorded setup change.
- Twelve logging/accounting harness tests pass. A separate scripted public
  discovery regression passes 11 calls and 45,931 complete response bytes,
  discovering source, contexts, bindings, definitions and implementations.
- The public catalog contains 22 tools. Original prompts, rubric, budgets and
  instruction-only isolation remain fixed. The coordinator smoke is not scored.

## Launch procedure

The user's earlier instruction, “Continue single threaded, no more subagents for
now,” remains active. Launch requires an explicit exception for fresh solvers and
reviewers, one child at a time. Root has seen source and gold and cannot supply
independent answers itself. No solver directories have been initialized.

Before launch, verify every frozen input hash in `protocol.json`, the archive
manifest, and every file under the local snapshot index against
`local-index-hashes.json`. Stop on drift rather than silently changing the run.
The readiness server has been stopped; restart it from the repository root:

```sh
.local/routing-slip/moedex serve \
  --index-dir .local/journey-readiness/capture-sdk10/index \
  --mcp-http 127.0.0.1:19457 --embed none --mcp-max-concurrency 1
```

The coordinator must own the server for the entire run. Verify that `tools/list`
matches the frozen catalog before assigning tasks. For each task, in protocol
order, initialize a new local directory using the logged client:

```sh
python3 research/semantic-intelligence/agent-journeys/client.py init \
  --run-dir .local/journey-readiness/solvers/TASK_ID \
  --endpoint http://127.0.0.1:19457/mcp \
  --prompt research/semantic-intelligence/results/journey-readiness-20261001/prompts/TASK_ID.txt \
  --catalog research/semantic-intelligence/results/journey-readiness-20261001/catalog.json
```

Assign a fresh `fork_turns=none` solver only the frozen solver instructions and
its run-directory path. Record assignment time and enforce 600 seconds from
assignment, including time before the first request. Count every attempted call,
error and retry. Preserve partial answers and budget failures. Do not restart a
solver that has already seen evidence as an unreported fresh trial.

Score against the original source rubric and actual returned evidence; then use
a separate fresh reviewer sequentially. Retain disagreements and adjudication.
Record observable tool activity and isolation attestations. If complete activity
is unavailable, disclose incomplete auditing; shared filesystem access is not
technically blocked. Do not claim enforced isolation, heldout quality, a paired
CodeGraph win, controlled latency or known model-token consumption.

This is another development-corpus evaluation. Fresh holdouts and a functioning
paired CodeGraph arm remain separate acceptance gates.
