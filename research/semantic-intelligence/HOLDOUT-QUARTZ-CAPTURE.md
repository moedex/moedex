# Quartz holdout: reviewed oracle, failed compiler setup

On 2026-10-02, the authorized fresh source reviewer completed review of all six
tasks and 30 atoms. Compiler-backed evaluation then failed at capture setup.
No semantic artifact, snapshot, solver assignment, answer review or task score
was produced. No source-only substitute was run and no paired CodeGraph result
is claimed.

## Independent review

The reviewer verified the pinned commit and all 2,713 source hashes, plus every
oracle excerpt. Twenty atoms were accepted unchanged. The coordinator accepted
ten amendments before capture: calendar-loop conditions, startup cleanup exception
masking, shutdown coordination boundaries, and conditional job-disposal ownership.
The review also made partial-credit rules explicit. Prompts and atom count stayed
unchanged; the reviewed oracle uses nine source files.

Reviewed gold SHA-256:
`0b6018a5fa3ed73d524439797ab0cd4bd10bcf14b1659f2a164b73ae92bccb8b`.
The original [preparation archive](results/holdout-quartz-20261002/result.json)
remains immutable. The successor preserves the reviewer proposals and every
coordinator amendment decision.

## Two distinct capture attempts

| Attempt | Result | Classification |
| --- | --- | --- |
| Original frozen plan | Exit 1 in 0.51 seconds: checkout basename `source` did not match public repository `Quartz` | Coordinator launch-configuration error; worker did not run |
| Explicit setup successor | Exit 1 in 8.24 seconds: worker `FileNotFoundException` for `source/artifacts/bin/Quartz.Analyzers/debug/Quartz.Analyzers.dll` | Frozen-product compiler setup failure |

For the second attempt, a local canonical checkout named `Quartz` was created
and all tracked hashes verified identical. Only checkout/workspace/output paths
changed in a separately recorded launch plan. The first failure was retained;
this was not a replacement score or a product correction. The reviewed gold,
CLI, worker, SDK and dependency bundle stayed unchanged.

The second failure is the substantive blocker. Quartz declares its analyzer as a
`ProjectReference` with `ReferenceOutputAssembly=false` and
`OutputItemType=Analyzer`; `UseArtifactsOutput=true` places its DLL under the
custom artifacts tree. Source inspection shows managed capture performing offline
restore and then opening the compiler workspace. The worker hashes each analyzer
reference as a required input, but the reported analyzer binary is absent in the
fresh projection. The separate upstream normal and offline builds had generated
that binary and passed without warnings or errors.

The missing path is directly observed in CLI stderr. The diagnostic location in
the worker is supported by source inspection, not a retained full stack trace.
The CLI cleaned the failed workspace; no successful compilation, context roster
or binding counts can be inferred. Suppressing or dropping this analyzer would
weaken the capture contract and is not an acceptable workaround.

## Remaining work and evidence limits

The next development increment should prepare project-built analyzer/generator
outputs inside the isolated capture workspace, respecting each project's target
framework, configuration and declared output paths. It needs provenance and
source-integrity checks, a deterministic fixture with a project-built analyzer,
and fail-closed diagnostics for missing outputs. After any product correction,
Quartz becomes development evidence; a new untouched repository is needed for a
fresh holdout claim. The paired CodeGraph setup gate remains separate and open.

One fresh reviewer ran, with maximum concurrency one. The six authorized solvers
and six answer reviewers remain unstarted because publication failed. No GPU was
used and no product implementation changed in this phase. The earlier source-only
and development scores remain unchanged; this result is a setup failure, not zero
correct answers out of six assigned tasks.

The final audit verified the source checkouts, product/toolchain/dependency
manifests, both pre-capture freezes and all files in four prior archives. No
Go test rerun was needed for this evidence-only phase. Preparation's 24 passing
harness tests remain recorded in the original packet; the new live per-task
initialization gate was never reached.

[Sealed result](results/quartz-capture-attempt-20261002/result.json): 38 evidence
files, 2,427,537 bytes excluding its result manifest. SHA-256:
`e55494f1944c81374a155e1ea93a8a1796271d181ec77a1ba85c6b9b8f6f2e93`.

[Independent review](results/quartz-capture-attempt-20261002/oracle-review.json),
[amendments](results/quartz-capture-attempt-20261002/oracle-amendments.json),
[reviewed protocol](results/quartz-capture-attempt-20261002/protocol-reviewed.json),
and [worker failure](results/quartz-capture-attempt-20261002/capture-02/capture.stderr).
