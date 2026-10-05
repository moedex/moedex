# CleanArchitecture source-only holdout result

2026-10-02. **Five evaluable source-only tasks pass strict review**, with 26/26
correctness and 26/26 evidence points and no material unsupported claims. The
sixth assigned task was blocked by local transport before receiving source
evidence. The compiler-enabled arm failed during setup. This is not a
compiler-enabled acceptance result or a paired CodeGraph win.

## Coverage and scores

| Task | Correctness | Evidence | Strict | Outcome |
| --- | ---: | ---: | --- | --- |
| Locate title validation | — / 4 | — / 4 | No | Infrastructure blocked; abstained |
| Locate database wiring | 5/5 | 5/5 | Yes | Evaluable |
| Trace completion event | 7/7 | 7/7 | Yes | Evaluable |
| Trace validation error | 5/5 | 5/5 | Yes | Evaluable |
| Impact of Done semantics | 5/5 | 5/5 | Yes | Evaluable |
| Impact of title uniqueness/length | 4/4 | 4/4 | Yes | Evaluable |

Both denominators matter. Conditional on receiving evidence, the result is five
strict successes in five tasks and 26/26 points on both axes. Across the original
six assignments, five succeed strictly; counting blocked requirements as zero
produces a 26/30 point lower bound on each axis. The blocked four requirements
are not evidence of product answer errors. No task was replaced or restarted,
and this small selected holdout is not pooled with the development-corpus result.

## Compiler setup failed; source-only fallback was explicit

The pinned repository is `jasontaylordev/CleanArchitecture` at
`5353a9edae000d576eade1a4f2c0d72d3b1c1785`. Its SDK10.0.401 build and offline
restore passed during [environment preparation](HOLDOUT-CLEANARCHITECTURE-ENVIRONMENT.md),
with nine upstream target-framework compatibility warnings. Unchanged worker18
with Roslyn5.9 then failed Moedex's semantic capture gate: workspace diagnostics
include those warnings, and the worker treats workspace diagnostics as incomplete
capture. The CLI rejected publication and cleaned its failed projection. Its
failure log retains the bounded error tail, not a complete raw worker stream.
No warning suppression, dependency/source rewrites, detector changes, or different
commit were used to make the gate pass.

Before the first solver, the executed protocol explicitly switched to native
source-only serving with `graph=false`, no semantic artifact and `embed=none`.
The snapshot indexes 252 files from the same 258-file checkout. The six questions,
reviewed 30-atom rubric and budgets stayed fixed. Source-only success does not
repair or erase compiler setup failure. No GPU or dense arm was exercised.

## Independent review and evidence integrity

The initial [source packet](HOLDOUT-CLEANARCHITECTURE-READINESS.md) preceded product
queries. A fresh source-only reviewer verified the pinned commit, all 258 tracked
file hashes and all 44 initial citation ranges. Eight wording/evidence amendments
and a common partial-credit clarification were accepted and frozen before capture.
They clarify compilation versus deployment, EF model metadata versus database
enforcement, and complete the source support for selected paths; no tasks or atoms
were added. Original gold, amended gold, independent review and decisions remain.

The user authorized one child at a time: one fresh oracle reviewer, six fresh
solvers and six fresh answer reviewers. Every agent began without conversation
history. The coordinator separately scored answers; answer reviewers did not see
those scores. One evidence-score disagreement occurred on the Done-impact atom:
the coordinator initially gave half evidence credit for not additionally citing
the ClearDomainEvents body. The independent reviewer gave full credit: the
required impact sites were precisely cited and that body was fully displayed.
Adjudication adopts full credit for this impact-site requirement, consistent with
the pre-query source review's recommended additional body citation. Both original
scores and the rationale are retained; the rubric was not changed after capture.

Audits verify all 148 returned source windows against pinned files, normalizing
BOM/line endings while retaining raw source hashes. All valid responses use one
snapshot fingerprint. All 116 recorded browser views stay within the 8,192-byte
stdout cap, and displayed fragments/offsets reconstruct exactly from recorded
responses. Index, source, executable, worker, browser, accounting client and
reviewed-oracle hashes remain unchanged. The temporary MCP server is stopped.

## Budgets and infrastructure failure

Limits were 24 attempted calls, 131,072 complete response bytes and 600 seconds
from assignment per solver. All five evaluable tasks satisfy all three. Maximum
observed use is 23 calls, 118,626 bytes and a 462.85-second assignment-to-completion
upper bound. In total, the run records 88 attempts, 409,123 observed response bytes,
one native `graph_unavailable` response and one transport failure.

The first solver's initial call encountered sandbox loopback PermissionError.
The client stopped with unknown transport accounting; an escalated retry was
blocked by that persisted stop. No source response was received, and the solver
abstained. The frozen run was not reset. Subsequent not-yet-launched solvers were
explicitly instructed to request escalated loopback access from their first call.
Zero retained bytes for the failed call must not be presented as known complete
transport accounting. All six assignment-to-completion upper bounds are below
600 seconds, but full response-byte compliance is unknown for the blocked task.

Only four of 82 search/read attempts requested `format=structured`; the rest used
default/text output. This is an observed adoption/cost issue, not a controlled
estimate of savings. Native tools/list discovery was supplied, but MCP initialize
server instructions were not supplied to solvers. Results therefore characterize
this catalog-based harness, not every production client's onboarding experience.

## Limits and next work

Source and task selection was coordinator-chosen and small. This does not prove
whole-repository coverage, random-sample generalization, absence from model
training, cross-service tracing, or the program's larger competitive gate.
Filesystem separation was instruction-based, not OS-enforced. Exact displayed
views and agent attestations are retained, but full host/model activity and model
attention cannot be verified. Model settings were inherited defaults without
overrides; exact build, seed and token use are unavailable. The CodeGraph product
arm remains unready and unrun; earlier component comparisons remain separate.

Next, investigate the compiler workspace-diagnostic failure with a minimal
regression and an explicit diagnostic policy, preserving fail-closed handling of
real load failures. Correct local-access preflight and include native MCP
initialization instructions in a separately frozen next harness. Any tuning on
this repository makes it development evidence; use a fresh holdout for subsequent
generalization claims. A paired native CodeGraph run remains necessary for a
competitive claim.

The immutable evidence manifest (archival evidence maintained separately)
contains the final adjudicated result, source review/amendments, failed capture
logs, executed protocol, six attempts, six independent answer reviews, coordinator
scores, displayed pages, raw requests/responses, audits and validation scripts.
Large corpora, SDKs, dependencies, binaries and shards remain in ignored `.local`.
