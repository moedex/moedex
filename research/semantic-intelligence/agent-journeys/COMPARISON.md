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

The default launch policy requires verified immutable model/product identities
and a complete runtime dependency closure. A separately authorized observed-service
policy is available for interfaces that do not expose those identities; its
requirements and limits are described below. This harness neither contacts private
feeds nor starts or configures those services. Existing historical scores and
immutable archives stay untouched.

`native_http.py` provides a separate transport for authorized native HTTP
diagnostics, including HTTPS bearer authentication, negotiated MCP sessions, JSON
and multiline SSE responses. `NativeHTTP.exchange` returns the request bytes,
retained response-body bytes, and a receipt. A size crossing or interrupted body
is explicitly incomplete; partial bytes remain available. Redirects are not
followed, and plaintext endpoints are restricted to loopback. Receipts omit
credentials, session IDs, endpoint URLs and exception messages.

The caller must persist exchanges, account for attempted calls and initialization,
freeze the native catalog and enforce assignment/display budgets. This transport
does not replace `client.py` or the independent accounting auditor. Its byte count
measures response bodies, excluding HTTP headers, TLS framing and model tokens;
it does not establish deployed build identity or benchmark eligibility. Complete
native catalogs may exceed the example 128 KiB budget. Choose and freeze a shared
budget before assignments rather than silently omitting catalog bytes.

## Retain native assignments before interpreting them

`run_record.py` is a reusable recorder and offline auditor for new assignments.
It does not issue network requests, enforce operating-system isolation, collect
provider logs, or award semantic scores. Python 3.9 or newer on macOS/Linux is
required. Product adapters can use the following sequence:

```python
from pathlib import Path
from run_record import RunRecord, audit, reference

root = Path("EVIDENCE_ROOT").resolve()
record = RunRecord.create(
    root, "unique-assignment-id",
    {"task": "fresh-task", "arm": "native-arm", "solver_id": "fresh-solver",
     "contract_sha256": contract_digest, "prompt_sha256": prompt_digest},
    {"calls": 24, "response_bytes": 262144,
     "assignment_seconds": 600, "display_bytes": 8192},
    reference(root, root / "freeze.json"), session_id=coordinator_session)

ordinal = record.begin_call(request_bytes)  # Persist BEFORE network I/O.
request, response, receipt = transport.exchange(request_object)
record.finish_call(ordinal, response, receipt)
record.display(exact_display_bytes)          # Include the complete envelope.
first_answer = record.submit_answer(first_answer_bytes)
revised_answer = record.submit_answer(revised_answer_bytes)
report = audit(root, "unique-assignment-id")
```

The adapter must ensure `request_bytes` are exactly the bytes sent, log every
native exchange including initialization/catalog calls, and deliver only the
persisted display bytes to the solver. Retain exceptions and blocked outcomes;
call `stop(reason)` when abandoning an assignment. Native response receipts must
contain boolean `transport_complete` and integer `body_bytes_observed` equal to
the length of the retained body. A budget-crossing response is retained in full
and counted. Interrupted transport retains observed bytes as a lower bound.
Unknown headers, TLS framing, and provider tokens remain outside this body count.

Each assignment starts its own `journey_clock.monotonic` deadline at creation.
Creation belongs at actual task assignment, before onboarding. The recorder binds
that origin to the OS boot identity and a coordinator session identity. Preserve
the session across cooperating CLI processes; mint a new identity after a
coordinator restart. A different boot/session cannot mutate an old assignment.
Never mint a replacement origin for an existing task to reset its elapsed budget.
The optional clock/boot arguments exist for verified integrations and synthetic
tests; production callers must not invent boot identities or use process-local
clocks.

Events and evidence files are created exclusively and fsynced; a hash chain starts
at the immutable assignment file. Each answer revision gets its own evidence
file and completion receipt. Its completion time is sampled after the answer body
and submission event have been fsynced. A deadline crossing during persistence
retains the late answer and excludes the assignment. An interrupted completion
receipt preserves the body and blocks further solver actions. Pending attempts prevent retries and further solver actions.
A crash between blob creation and event creation leaves orphan evidence, which
fails audit and stops future mutation. Preserve that interrupted assignment and
start a new one. Do not delete orphan files, reset a ledger, or replace an answer.
The lock serializes local callers; external reference digests and a published
snapshot are still required to detect wholesale rewriting or truncation of a
locally writable ledger. A hash chain alone is not a trusted timestamp service.

The freeze manifest required by `freeze_gate(root, manifest)` has this shape:

```json
{
  "model": {"revision": "EXACT_PROVIDER_REVISION", "settings": {"temperature": 0}, "verification": "REF"},
  "solver_records": {"capture_available": true, "capture_probe": "REF"},
  "isolation": {"enforced": true, "verification": "REF"},
  "preflight": {"same_execution_environment": true, "verification": "REF"},
  "product": {"serving_sha256": "IMMUTABLE_LIVE_BINARY_OR_IMAGE_SHA256", "verification": "REF", "dependencies": "REF"},
  "auditor": "REF"
}
```

Every `REF` uses the exact path/digest object described above. The recorder rejects
symlinks even within the evidence root. Freeze the recorder, clock, transport,
adapter and auditor source hashes in the protocol/product bundle before launch;
`auditor` references the actual auditor used. Independent review must assess the
content of each proof: model revision/settings, complete provider capture probe,
filesystem/network isolation enforcement, preflight in the same solver sandbox,
currently serving immutable product identity and runtime dependency closure.
The dependency reference must contain JSON with explicit `complete: true` and a
`verification` reference to the independently checked closure evidence. Merely
referencing build metadata does not assert a complete runtime closure.
The gate verifies references and explicit declarations; it cannot establish that
those declarations faithfully describe a deployed process. Instructions to a
solver do not establish enforced isolation. Version strings or a successful
source build do not establish the identity of the binary currently serving.

Unknowns fail launch by default. `diagnostic=True` explicitly permits collecting
an assignment with preserved launch blockers; answers cannot erase those blockers
or promote that assignment into scored evidence. Even an otherwise ready
assignment remains ineligible until complete solver records are attached with
`seal(solver_records_ref, independent_verification_ref)`. That verification JSON
must bind `assignment_sha256`, `solver_records` (the exact reference),
`model_revision`, boolean `complete` and `access_checked`, and a nonempty
`reviewer_id` distinct from `solver_id`. The assertions remain reviewable evidence,
not facts inferred from a native call ledger. Independent sealing may occur after
the solver deadline; it does not extend the deadline or alter the recorded final
answer duration. Sealing stops further solver mutations.

```sh
python3 research/semantic-intelligence/agent-journeys/run_record.py \
  --root EVIDENCE_ROOT --assignment unique-assignment-id \
  --output NEW_AUDIT_FILE.json
```

The audit output must be a new file. Exit 2 means malformed/tampered records;
exit 0 can still mean an incomplete or diagnostic assignment. Check
`benchmark_eligible`, `validation_errors`, `eligibility_blockers`, pending calls,
all answer revisions, and the byte lower-bound flag. This is a recording gate;
source review, rubric review, independence, and complete paired coverage remain
separate requirements checked by the comparison process.

## Explicit observed-service provenance

An absent `provenance` field retains the immutable launch policy. An explicit
`{"mode":"immutable-v1"}` selects the same policy. Unknown or malformed policies
fail closed. A new protocol may select `observed-service-v1` only with recorded
user authorization and explicit reproducibility limits. This retains the capture,
isolation, budgets, matching preflight and independent review requirements.
Historical diagnostics cannot acquire eligibility by changing a policy label.

Add the following fields to a new arm freeze; keep the usual execution gates,
contract, image, endpoint and runner bindings. Replace every `REF` with a reference
object. `MODEL_SETTINGS` denotes the complete frozen CLI/provider settings object.

```json
{
  "provenance": {
    "mode": "observed-service-v1",
    "authorization": "REF",
    "reproducibility_limits": [
      "Serving image and runtime dependency closure are unverified.",
      "Requested and returned model names do not establish an immutable revision."
    ]
  },
  "model": {
    "requested_alias": "REQUESTED_MODEL",
    "provider_base_url_sha256": "EXACT_URL_DIGEST",
    "settings": "MODEL_SETTINGS",
    "observation": "REF"
  },
  "product": {"endpoint_sha256": "EXACT_URL_DIGEST", "observation": "REF"}
}
```

Authorization JSON uses schema `observed-authorization-v1`, mode
`observed-service-v1`, `authorized_by: "user"`, a nonempty `decision`, and a
`requirements` list containing `isolation`, `budgets`, `raw-capture` and
`independent-scoring`. Its `reproducibility_limits` must exactly match the freeze.
The reference binds the recorded authorization bytes; it is not a user signature.
Independent review must establish that the recorded decision actually occurred.

Model observations use schema `observed-model-v1`, a UTC `observed_utc`, the
frozen `requested_alias` and `provider_base_url_sha256`, and `settings_sha256`
over canonical JSON of the complete frozen `model.settings`. They include a
sorted, unique, nonempty `returned_models` list, `immutable_revision_verified:
false`, and raw `request`, `response` and `receipt` references. The request must
use the frozen `provider_fields`, without extra settings; `input`,
`prompt_cache_key` and `client_metadata` are the only additional fields allowed.
Raw JSON/SSE responses must contain matching model identities and a completed
response. Malformed events or empty/nonstring identities fail validation.

Product observations use schema `observed-product-v1`, UTC `observed_utc`, the
frozen `endpoint_sha256`, complete initialization `server_info`, and
`catalog_sha256` over canonical JSON of the complete `tools/list` result. They
include three `native_exchanges`, each containing `request`, `response` and
`receipt` references, in this order: `initialize`, `notifications/initialized`,
`tools/list`. Catalog pagination must be completed before freezing; the current
schema rejects a remaining `nextCursor`. `immutable_serving_identity_verified`
and `runtime_dependency_closure_verified` must both be false. Every observation
receipt must report successful, complete transport and the exact retained body
byte count. Observation references reject traversal and symlinks.

`provenance.identity_binding(root, freeze, read_ref)` derives the exact object
required in independently authored seal verification and accounting output:

```json
{
  "policy": "observed-service-v1",
  "authorization_sha256": "AUTHORIZATION_FILE_DIGEST",
  "model_observation_sha256": "MODEL_OBSERVATION_FILE_DIGEST",
  "product_observation_sha256": "PRODUCT_OBSERVATION_FILE_DIGEST",
  "reproducibility_limits": ["EXACT_FROZEN_LIMITS"]
}
```

An observed seal replaces the immutable policy's `model_revision` comparison with
an exact `freeze_sha256` and this `provenance` object. Assignment hash, exact solver
record reference, `complete`, `access_checked` and a reviewer distinct from the
solver remain required. A legacy seal with `model_revision: null` does not verify
an observed assignment. `run_record.audit` emits the checked binding; the accounting
adapter must preserve it. `compare.py` checks accounting against the frozen
observations rather than trusting a free-form provenance label.

The report adds `provenance_classification`, per-arm observed identities and
the frozen limits. All-observed arms report `observed-service comparison`; differing
policies report `mixed provenance policies`. The default report says `immutable
identity policy`, which describes the selected policy without independently
revalidating deployment attestation. Coverage `classification`, scoring
`strict_success` and planned/assigned/eligible denominators keep their meanings.
Observed identities enable a bounded service comparison, with reproducibility
limited by unverified serving/runtime/model revisions. They cannot establish an
immutable build comparison.

## Isolated solver and complete exchange capture

`Dockerfile.solver` builds the pinned CLI bridge. `isolated_solver.py` launches an
immutable local image with networking disabled, a read-only filesystem, dropped
capabilities, no host/corpus mounts, and fresh runtime directories. Docker attached
stdin/stdout carry provider and MCP exchanges. The host keeps credentials in
memory and dispatches only to configured, frozen endpoint identities and native
tool names. CLI flags alone are insufficient isolation: resource, patch and
collaboration helpers can remain available. Those helpers cannot reach host
source or an external network in this container.

```sh
docker build -f Dockerfile.solver -t recorded-solver:local .
# Obtain the immutable ID using docker image inspect.
python3 isolated_solver.py --probe --config LOCAL_CONFIG.json --capture NEW_PRIVATE_DIR
```

Probe config requires `image` (a `sha256:` image ID), `model`, `reasoning_effort`,
`prompt`, `enabled_tools`, and `timeout_seconds`. The probe initializes a synthetic
MCP broker, returns one synthetic metadata-only `ALL_TOOLS` call, then HTTP 400.
It performs **zero real model/provider or native corpus retrieval calls**. Its
expected CLI exit is nonzero. A complete capture proves recording capability;
it does not establish native product readiness, model identity or correctness.

For an actual run, omit `--probe` and supply `evidence_root`, `assignment`,
`identity`, `budgets`, `freeze`, `coordinator_session`, and environment-variable
names `native_url_env`, optional `native_token_env`, `provider_url_env`, and
`provider_token_env`. No credential values belong in configuration files. Identity
and budgets follow `RunRecord.create`. Freeze JSON must include its exact
`contract` reference, `arm`, `native_allowed_tools`, `isolation.image_sha256`,
`product.endpoint_sha256`, `model.requested_alias`,
`model.provider_base_url_sha256`, model settings including
`reasoning_summary: "none"`, and all eight observed `provider_fields`: `model`,
`reasoning`, `tool_choice`, `parallel_tool_calls`, `text`, `store`, `stream`, and
`include`. SHA-256 of the exact configured URL bytes binds endpoint identities.
`runners` binds executing `isolated_solver.py`, `run_record.py`, `native_http.py`,
and `journey_clock.py` bytes; observed runs additionally bind `provenance.py`.
All independent `freeze_gate` prerequisites for the selected policy still
apply; no diagnostic fallback is used. Changed tasks, prompts, budgets, model
settings, endpoints, images or executing code fail before native onboarding.

Native onboarding retains the complete original catalog and consumes the task's
original budget. The broker exposes the frozen subset, retains complete native
responses, and explicitly truncates oversized model-visible JSON-RPC envelopes.
Native semantic errors remain errors. Provider bodies are retained separately
with exact request bytes, raw response-body bytes, safe receipts, timestamped CLI
events and a non-overwriting inventory. HTTP headers/TLS framing remain outside
body accounting. Provider failure/partial transport, interruption and deadline
crossings remain evidence and cannot be silently retried as the same assignment.

Every completed agent message that parses as task-bound answer JSON is retained
as an answer revision. Other messages remain timestamped drafts in the capture.
The CLI's final output file is retained as a submission unless it exactly repeats
the last submitted body. Independent review must verify source/citations,
provider transcript coverage, actual native observations, isolation, and the
selected provenance policy before sealing. In observed mode, native onboarding
must match the frozen server/catalog identity and each provider response must
match the frozen returned model names. Raw mismatching responses and receipts
are retained before the controller stops the assignment. The controller does
not author semantic scores, declare transcript completeness, or infer immutable
model identity from an alias. Raw provider bodies contain prompts and retrieved
source: publish them only to the authorized evidence destination, never as
public fixtures. Container images and local configuration remain uncommitted.
