# Paired native-arm comparison records

`compare.py` consumes independently collected results. It does not launch a
service, query a product, author scores, or convert development evidence into a
holdout. It performs no network access and never edits input artifacts. The report
must be a new file. Python 3.9 or newer is required.

```sh
python3 research/semantic-intelligence/agent-journeys/compare.py \
  --root EVIDENCE_ROOT --contract contract.json \
  --arm moedex.json --arm codegraph.json --output comparison.json
```

Invalid or mismatched evidence exits 2 without a report. Valid incomplete coverage
exits 0 and explicitly reports `incomplete paired coverage`. Consumers must check
that classification and the denominators; exit 0 is not a competitive pass.

## Freeze one shared contract before execution

The contract has this shape. Every `REF` is exactly
`{"path":"relative/path","sha256":"lowercase sha256 of raw file bytes"}`.
Referenced paths must resolve inside `--root`, including through symlinks.

```json
{
  "schema": "native-pair-v1",
  "corpus": {"repository": "https://github.com/owner/repository", "commit": "FULL_GIT_OBJECT_ID", "manifest": "REF"},
  "rubric": "REF",
  "protocol": "REF",
  "budgets": {"calls": 24, "response_bytes": 131072, "assignment_seconds": 600, "display_bytes": 8192},
  "tasks": [{"id": "locate-example", "prompt": "REF", "atoms": ["locate-example.1"]}]
}
```

Replace quoted `REF` placeholders with reference objects. The contract digest is
SHA-256 of UTF-8 JSON with sorted object keys, separators `(',', ':')`, default
Python ASCII escaping, and no newline (`compare.digest(compare.canonical(...))`).
Array order is retained. Both arms must declare this exact digest. Corpus commit,
source manifest, prompts, rubric, budgets and protocol are therefore bound to the
same contract; a missing task is an error rather than a smaller denominator.

The protocol should freeze solver model/settings, independence, arm order,
onboarding, evidence access, full-assignment timing, native endpoints, review and
adjudication policy, and any allowed setup work. Maintain a separate frozen product
manifest for each arm: binary/dependency hashes, index provenance, configuration,
capabilities, source revision, and all runner/auditor script hashes. Different
products naturally have different manifests. No per-task product substitution is
allowed. Preserve failures and retire tuned corpora to development evidence.

For logged CLI journeys, mint assignment deadlines with
`journey_clock.monotonic()` and freeze `journey_clock.py` with the other runner
scripts. This keeps persisted call and assignment budgets on the same system
clock across processes, including macOS Python 3.9. Do not resume historical
runs using a different clock implementation.

The script checks referenced bytes and declared identities. It does **not** infer
that a manifest is exhaustive or that a rubric's declared atom roster faithfully
represents its text. Independent pre-launch review must check those semantics.

## One record per arm

```json
{
  "name": "Moedex",
  "native": true,
  "contract_sha256": "CONTRACT_DIGEST",
  "freeze": "REF",
  "setup": "REF",
  "tasks": [{
    "id": "locate-example", "status": "answered", "solver_id": "fresh-solver-01",
    "setup": "REF", "answer": "REF", "accounting": "REF", "review": "REF"
  }]
}
```

Setup evidence is a JSON object containing boolean `ready`; include raw native
initialization/catalog exchanges and product prerequisite checks in the retained
setup bundle. `prepare.py` emits a compatible `ready` result for Moedex's stateless
endpoint. CodeGraph may need a different native setup adapter; do not silently
force its handshake or authentication into Moedex's setup assumptions. Each
assigned task also references its own successful preflight.

Every planned task must have exactly one of these statuses:

- `unassigned`: supply a nonempty `reason`; no execution or scores.
- `setup_failed`: supply `reason` and `setup` referencing `ready: false`; no scores.
- `blocked`: assignment occurred; supply successful `setup`, audited `accounting`
  and `reason`, with no answer/review scores.
- `answered`: supply successful `setup`, `solver_id`, raw `answer`, audited
  `accounting`, and final independent/adjudicated `review`.

An arm with failed setup cannot contain assigned tasks. Failed task preflight is
not an assigned task. Transport failure after assignment is blocked and remains in
the assignment denominator. Unknown wire bytes must never be reported as known
zero merely because no response was retained.

## Accounting audit and review

An accounting reference resolves to JSON:

```json
{
  "task": "locate-example", "contract_sha256": "CONTRACT_DIGEST",
  "freeze_sha256": "ARM_FREEZE_FILE_DIGEST", "prompt_sha256": "PROMPT_FILE_DIGEST",
  "calls": 2, "response_bytes_observed": 5000, "assignment_seconds": 31.2,
  "max_display_bytes": 8192, "transport_complete": true,
  "transcript_verified": true, "onboarding_complete": true
}
```

This is the **auditor's output**, not the solver's self-report. Audit raw request
and response files, attempted-call ledger, displayed views, transcript, setup,
assignment and final-answer timing. Use elapsed upper bounds where timestamps are
uncertain; `assignment_seconds` starts at assignment, not first MCP interaction.
`response_bytes_observed` always counts full retained wire bytes, including native
errors and budget-crossing responses; it is a lower bound when transport is
incomplete. `max_display_bytes` includes the complete display envelope. Native
semantic errors count as calls but do not themselves make accounting incomplete.
`transcript_verified` asserts inspection for unauthorized access and complete
execution evidence, including a final answer within the stated assignment period.

Review JSON:

```json
{
  "task": "locate-example", "answer_sha256": "ANSWER_FILE_DIGEST",
  "rubric_sha256": "RUBRIC_FILE_DIGEST", "reviewer_id": "fresh-reviewer-01",
  "adjudication_final": true, "material_unsupported_claims": 0,
  "runtime_distinction_preserved": true,
  "atoms": [{"id": "locate-example.1", "correctness": 1, "evidence": 0.5}]
}
```

Reviewer and solver identities must differ; the script cannot prove their
independence. Every frozen atom must appear exactly once. Scores are 0, 0.5 or 1.
Preserve original reviews, disagreements and adjudication evidence in the review
bundle; mark final only after resolving them. Do not change the rubric after
product queries. Answer hash, rubric hash and task identity are checked.

## Reading the report

Coverage is reported as planned, assigned and eligible task counts per arm.
Conditional correctness/evidence use only eligible tasks and state their atom
denominator. Blocked and unassigned tasks have null scores. Answers that exceed
budgets or lack verified accounting retain their reviewed points in task rows but
are excluded from conditional totals and paired deltas. Strict success additionally
requires every atom correct and evidenced, no material unsupported claims and the
static/runtime distinction preserved.

Paired rows use only the intersection of eligible task IDs, in frozen task order.
Deltas are **first arm minus second arm**, including resource deltas (where a
negative value means less use). Partial pairing remains explicitly incomplete;
there is no aggregate winner, significance test, selective zero-filling, or claim
that one product beats another. Report both setup failures and conditional quality.
Even complete coverage on six tasks is descriptive evidence, not a broad victory.

`test_compare.py` builds temporary synthetic packets as executable input examples.
Tests cover missing setup, unassigned and transport-blocked tasks, budget and audit
exclusions, semantic failures, changed products/prompts/rubrics, mismatched answers,
invalid scores, altered evidence, path escapes and non-overwriting CLI output.
These fixtures are harness tests, **not measured Moedex/CodeGraph results**.

The CodeGraph setup gate remains open: verified dependency closure and binary
provenance, isolated service configuration and database/auth prerequisites are
needed before a native paired trial. This harness neither contacts private feeds
nor starts or configures those services. Existing historical scores and immutable
archives stay untouched.
