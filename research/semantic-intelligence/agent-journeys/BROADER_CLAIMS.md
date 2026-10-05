# Frozen source-only paired benchmarks

[`broader_claim.py`](broader_claim.py) validates a declared plan, selects tasks
from a source-only frame, and reports a conditional paired comparison. It uses
only Python's standard library and makes no product, provider or network calls.
This manual workflow runs outside merge-request CI. Synthetic unit tests remain
in the ordinary harness test suite. It does not change existing frozen runs.

```sh
python3 broader_claim.py validate-plan --plan PLAN.json --output VALIDATION.json
python3 broader_claim.py sample --plan PLAN.json --frame FRAME.json --output SAMPLE.json
python3 broader_claim.py analyze --plan PLAN.json --frame FRAME.json --sample SAMPLE.json --results RESULTS.json --output ANALYSIS.json
```

Outputs use exclusive creation: an existing output file causes failure. Without
`--output`, JSON goes to stdout. Invalid inputs return exit status 2; a valid
report with unmet readiness also returns 0 and explicitly reports the reasons.
Do not interpret process success as permission to execute or evidence of a lead.
The sampler is a separate execution step; validating a plan does not select tasks.

## Freeze and review

Freeze the source frame, source pins/manifests, sampling quotas/seed, method,
practical margin, exact sample size, repetition count, assignment schedule,
review policies, and tool/model/service protocol before observing answers.
Prepare equal-access setup before sampling. A denied source scope discovered
before freezing belongs outside the eligible frame. After freezing, retain
every planned task, arm and repetition, including access losses and scope failures.
Never replace a failed attempt or shrink the denominator after seeing answers.

A pilot is exploratory and cannot name a lead. Confirmation is a separate plan
containing the actual prior pilot sample and its plan/sample hashes. The sampler
automatically excludes prior task IDs, prompt/rubric/source fingerprints, source
families and witness hashes, in addition to explicit exclusion lists. The
coordinator must independently establish the prior sample's authenticity and
absence of undeclared source/answer reuse. Hash bindings do not discover concealed
or semantically equivalent overlap. Freeze a final confirmation
sample size before answers and do not repeatedly add batches until an interval
crosses the margin. The generic helper supports a fixed positive repetition count;
a protocol that specifies three repetitions must declare `repetitions: 3`.

Review declarations are **not authenticated evidence**. The program checks
hash/identity consistency in JSON and does not open referenced artifacts,
authenticate identities, check source content, audit OS isolation, validate a
live service revision, or reconstruct raw transcripts. The independent audit
must establish those facts and the accuracy of each claim's cited source span.
Physically valid file/line provenance alone does not establish semantic support.
The required `comparability_policy` review reference must certify exact
model/provider settings, symmetric budgets, fixed scheduling, fresh isolated
solver contexts, and equal native source scope under the separately frozen
protocol. The program checks this reference's presence and syntax; only external
review can establish its content or truth.

## Inputs

All objects have exact field sets; unknown fields fail. Hashes are lowercase
SHA-256, source commits are lowercase 40-character hex pins, and a reference is
`{"path": "reviews/example.json", "sha256": "…"}` with a confined relative
POSIX file path. The helper hashes JSON with sorted keys, compact separators,
UTF-8 and unescaped Unicode (`ensure_ascii=False`). It rejects duplicate JSON
keys and nonfinite numbers. File-byte artifact hashes and canonical JSON object
hashes are distinct; record the appropriate one in each binding.

Plan (`schema: broader-claim-plan-v1`):

| Field | Required content |
| --- | --- |
| `plan_id`, `phase` | Nonempty ID; `pilot` or `confirmation` |
| `arms`, `repetitions` | Exactly `["A","B"]`; integer 1–100, fixed before answers |
| `sample_seed`, `frame_sha256` | Integer 0–2^64−1; canonical frame hash |
| `corpus` | Nonempty unique roster of `{repository, commit, manifest_sha256}` |
| `strata` | Unique `{id, domain, language, task_kind, count}` cells; positive integer quotas |
| `task_kind_weights` | Exactly five declared kind names, each exactly `0.20` |
| `excluded` | `{source_families: [...], witness_sha256s: [...]}`, unique lists |
| `governance` | `{frozen_before_answers, sample_size_final, fixed_schedule, prior_pilot}`; first three are booleans; prior pilot is null for pilot or `{plan_id, plan_sha256, sample_sha256, sample}` for confirmation |
| `readiness` | Boolean `setup_ready`; six references or null: `source_only_frame_review`, `source_pin_alignment_review`, `raw_capture_policy`, `independent_scoring_policy`, `blinding_review`, `comparability_policy` |
| `bootstrap` | `{method, seed, replicates, confidence, practical_margin}`; method below, seed 0–2^64−1, 1,000–100,000 draws, confidence `0.95`, margin `0.05` |
| `coverage` | All coverage floors below; values may strengthen them |

Frame (`schema: broader-claim-frame-v1`) has `provenance: "source-only"` and
`tasks`. Each candidate has exactly:

```text
id, stratum, source_family, system, domain, language, task_kind,
source_scope, prompt_sha256, rubric_sha256, witness_sha256s
```

IDs and prompt/rubric/source fingerprints are unique; each family's tasks map to
one coarser system. The fingerprint binds prompt and rubric hashes plus the source
scope sorted by repository. Identical questions under different source scopes
can be genuine distinct tasks; source-family/system grouping truth still requires
external review. The stratum's
domain, language and kind must match the task. `source_scope` is a nonempty unique
subset of exact frozen corpus roster records. Witness hashes are nonempty and
unique per task. Source families and systems are declared dependency groups,
not automatically inferred from repository names or retrieval hits.

The inline prior `sample` uses the complete sample schema below, with nonempty
full task records. Its `plan_sha256` must equal the declared pilot plan hash,
and its canonical hash must equal `sample_sha256`. Source pins and task identity
are structurally checked; authentic linkage to an actual pilot plan/run remains
an external audit obligation.

Sampling removes declared exclusions and prior task IDs, fingerprints, families
and witnesses. Within each stratum, candidates are ordered by SHA-256 of canonical
`[sample_seed, stratum_id, task_id]`, with task ID breaking ties. The first quota
tasks are selected; a deficit fails. Native hits, tool success, solver answers and
review outcomes never enter selection. Input ordering does not affect selected
task IDs. Output (`schema: broader-claim-sample-v1`) contains `sampling_method`,
`plan_sha256`, `frame_sha256`, and the selected full `tasks`.

Results (`schema: broader-claim-results-v1`) contain `plan_sha256`,
`sample_sha256`, `shared_harness_defect`, `equal_access`, and `attempts`:

```text
shared_harness_defect: {confirmed: boolean, review: reference or null}
equal_access: {established: boolean, review: reference or null}
attempt: {
  task_id, arm, repetition, task_sha256, solver_id, outcome,
  failure_class, final, raw_review, source_review
}
```

A confirmed shared defect requires a review reference; an unconfirmed defect
uses null. Established equal access requires a review reference; a false access
declaration may retain a review reference. There must be exactly one row for
every planned task × arm × repetition (numbered from 1). Missing, unknown and
duplicate rows are invalid. `task_sha256` binds the complete selected task,
including prompt, rubric, source scope, witnesses and dependency groups.

`answered` requires a final reference and null failure class. Stops remain zero:

| Outcome | Allowed failure class |
| --- | --- |
| `infrastructure_stop` | `infrastructure`, `shared_harness`, `unclassified` |
| `budget_stop` | `budget` |
| `product_stop` | `product` |
| `scope_stop` | `access`, `scope` |
| `no_final` | `product`, `budget`, `infrastructure`, `shared_harness`, `unclassified`, `access`, `scope` |

`no_final` requires null `final`; other stops can retain a partial final.
A `shared_harness` cause requires a confirmed cohort defect. Post-freeze `access`
failure cannot coexist with a declaration of established equal access. A `scope`
violation, unclassified failure, unequal access, or confirmed shared harness
defect blocks a lead. Confirmed shared defects block a lead even if every task
still retains its reviewed primary outcome.

Both reviews contain `reviewer_id`, boolean `independent` and `complete`,
`task_sha256`, `final_sha256` (null when no final), and `artifact` reference.
Every planned attempt requires a distinct declared solver identity. A reviewer
must be outside the entire cohort's solver identity roster. These checks catch
declared reuse, but cannot prove separate processes, fresh contexts or actual
independence. Raw review adds boolean
`capture_integrity` and `classification_confirmed`. Source review adds `criteria`
with exactly these boolean fields:

```text
all_required_correct, physical_provenance, claim_citation_accuracy,
no_critical_unsupported_claims, source_scope_complete
```

An answered task is successful only when its independent reviews are complete,
raw capture/classification checks pass, and **all** source criteria pass.
Task success is binary; atom counts and partial-credit fields are rejected.
A completed source review can mark an incorrect answer zero without making the
run unready. Missing/incomplete raw review, or missing/incomplete source review
for an answered attempt, blocks readiness. Documented stops require raw review
and do not require source review.

## Paired method and interpretation

The only method is `shared-exp1-cluster-weighted-percentile-v1`.
First average A and B's binary verified success over the frozen repetitions of
each unique task. Then take their paired difference. Compute the mean difference
within each task kind and sum using the five exact weights of 0.20. Repetitions
do not increase the independent task or cluster counts. Unequal task-kind sample
sizes do not change the fixed kind weights.

Each bootstrap draw assigns one strictly positive Exp(1) multiplier to every
source family, shared by all its tasks across kinds, both arms and repetitions.
Within each kind, compute its multiplier-weighted ratio mean; retain the exact
0.20 weights for every draw. Repeat independently at the coarser system grain.
No kind disappears, redraw is unnecessary, and kinds are not independently
resampled. The fixed seed is separately keyed by grain. Report the 2.5th and
97.5th percentiles as **approximate** uncertainty intervals.

Coverage floors are global ≥30 families and ≥20 systems; within **each kind**
≥10 families and ≥8 systems. Within each kind, maximum cluster weight share must
be ≤0.20 at both grains; Kish effective cluster count must be ≥10 at family and
≥8 at system grain. Shares are unique-task count proportions within kind, before
random multipliers; effective count is `1 / sum(share²)`. Plans may raise count
floors or lower the dominance ceiling, but cannot weaken them.

A degenerate observed cluster distribution cannot estimate unseen adverse tails.
If either grain has effectively zero cluster influence sum of squares
(`≤1e−24`) or interval width (`≤1e−12`), the report declares
`uncertainty_not_estimable` and cannot name a lead. Cluster influence is the
derivative of the fixed-kind weighted ratio mean at unit multipliers. This guard
does not create a calibrated alternative interval.

`readiness_not_met` includes unmet freeze/setup/review/access/coverage/uncertainty
gates or exploratory pilot status. A ready confirmation names
`conditional_practical_lead` for A only if **both** interval lower bounds are
strictly above +0.05, or B only if both upper bounds are strictly below −0.05.
Otherwise it is `inconclusive`. A point difference alone cannot name a lead.

These intervals have no finite-sample 95% guarantee or stratified-survey
calibration claim. The benchmark's frozen balance does not estimate population
usage frequencies or infinitely many products. A conditional lead applies only
to the declared benchmark, grouping assumptions, retained attempts, external
audits and observed service identities. JSON declarations and their hashes alone
do not establish those conditions. Reports expose per-kind rates, all planned
denominators/outcomes, both grain intervals and coverage, bindings and limitations.

Synthetic validation:

```sh
python3 -B -m unittest discover -s research/semantic-intelligence/agent-journeys -p 'test_broader_claim.py'
```
