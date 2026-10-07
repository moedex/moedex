# Opt-in offline panel methods

`panel_methods.py` provides deterministic matched-panel selection and bounded
task-level inference. It has no dispatch, provider, network or comparison-run
entry point. Existing broad-claim gates remain unchanged. Study-specific policies,
candidate requests, labels, source inventories and results belong in their
authorized evidence repository, never in generic code or synthetic fixtures.

Supply a frozen `offline-panel-policy-v1` object specifying unique strata, equal
tasks per stratum, total tasks, repeat IDs, arms `["A","B"]`, total attempts,
alpha, practical margin and required exposure scope. Set `optional_stopping=false`,
`stratum_claims=descriptive-only`, `estimand=fixed-panel-equal-task-mean`,
`launch_authorized=false` and `methods_module_sha256=module_hash()`. Include the
study's independently reviewed protocol-document hash and review-receipt hashes
in that same policy. Hashes bind data; asserted PASS strings do not independently
prove review qualifications, access rights or exposure-registry completeness.

`lock(candidates, exposure, seed_commitment, policy)` binds canonical input hashes
and a committed SHA-256 seed before selection. Canonical JSON uses sorted keys,
compact separators and UTF-8; nonfinite numbers are prohibited. Candidate IDs,
fingerprints, obligation IDs and material repository/path/inclusive-line spans
support exposure and collision checks; each candidate references a reviewed pin
manifest hash. They do not authenticate source. The exposure registry must
include the study's required scope and derivation hashes, prior task identities,
obligations, source spans and touched path prefixes. An independent preflight
must verify actual derivation and source/review artifacts before any execution.

`sample(candidates, exposure, seed, lock, policy)` rejects changed inputs/seed,
records exposed exclusions, allocates cell quotas by largest remainder (ties
by cell ID) and ranks candidates by NUL-delimited seed/stratum/cell/ID SHA-256.
Selected fingerprints, obligations and overlapping material spans cannot repeat.
Candidate collision skips remain inside the same cell quota; insufficient
capacity fails without redistribution or seed retry. The ordering is deterministic
and collision constrained; do not call it an unconstrained probability sample.

`schedule(panel, policy)` uses a distinct seeded task ordering and alternates
which arm starts across tasks, reversing it across successive repeats. Freeze
the exact panel hash and schedule hash before outcomes. Record every scheduled
slot exactly once as terminal, missing or unlaunched, with unchanged task/repeat/
arm identity. Terminal assessment fields `semantic`, `integrity`, `ledger` and
`closure` are pass/fail/unknown. A confirmed failure is zero; otherwise unknown
evidence remains [0,1]. Product failures use a separately typed terminal reason.
Do not manufacture semantic failures for unlaunched slots.

`analyze(panel, panel_hash, schedule_hash, results, policy, shared_fault=False)`
rejects tampering/duplicate/truncated rosters. Missing/unlaunched slots or an
independently established shared method fault block confirmatory analysis. For
complete sound runs, repeats remain clustered into task differences in [-1,1].
Unknowns receive worst-case assignments in both directions. The interval adds
`sqrt(2*ln(2/alpha)/tasks)` to the task-mean identification bounds. It declares
practical superiority only when the entire interval exceeds the frozen margin
in one direction. Otherwise no superiority is established; that is not a tie.
Secondary/stratum endpoints remain descriptive.

The bound requires independent task-unit solver randomness under stable frozen
conditions. Within-task dependence is allowed. Reviewers must assess source,
labels, capture validity, qualified adjudication, shared faults and independence
separately. The target is the fixed selected panel, not a corpus-wide product
winner. The mechanism cannot certify those assumptions or guarantee sufficient
precision; calculate sample size before freezing. This application follows
[Hoeffding's original bounded-variable inequality](https://www.cs.rpi.edu/academics/courses/spring06/random/hoefding.pdf).

Run invented-fixture tests offline:

```sh
python3 -B -m unittest test_panel_methods
```
