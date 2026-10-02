# Quartz.NET source-first holdout readiness

Prepared 2026-10-02, single-threaded. Subsequent [independent review and capture
attempt](HOLDOUT-QUARTZ-CAPTURE.md) completed review but failed compiler setup;
the preparation record below describes the pre-launch state. Six tasks and 30 scoring atoms are ready for
independent source review. This is preparation and prerequisite-build evidence,
not a compiler-capture pass, independently reviewed oracle, solver score, or
competitive win.

## Corpus and task scope

[Quartz.NET](https://github.com/quartznet/quartznet) is pinned at
`15d90a9c2681cd9e273dcd3901c0fbbdbdc5fe90`, with 2,713 tracked files. It was the
first candidate selected for this packet. A search of current semantic-intelligence
research and planning records found no prior Quartz use before cloning. This
supports novelty within those records, not absence from training or every session.
The source was selected and inspected before any Moedex capture or query.

| Task | Scope |
| --- | --- |
| Locate hosted options | Shared/named registration, defaults, derived service retention |
| Locate job resolution | Keyed/unkeyed activation and unscoped-construction eligibility |
| Trace cron misfire | Builder policy, trigger state, calendar handling, execution limits |
| Trace host startup | Factory resolution, startup signal, delay, cancellation and failure |
| Assess shutdown changes | Concurrent stops, runtime schedulers, job waits, failure aggregation |
| Assess cleanup changes | Job ownership, scope failures, derived state and duplicate disposal |

The coordinator-authored oracle uses seven source files. Every excerpt, range and
raw file hash was checked. Prompts and scoring distinguish static conditional
behavior from observed execution and bound impact claims to the selected paths.
Independent amendments must be recorded before capture; no product output may
be used to revise the oracle.

## Frozen inputs and prerequisite checks

The local execution directory is `.local/holdout-quartz`. Large checkouts,
packages, SDK and binaries remain local. The durable archive contains manifests,
source evidence, preparation scripts, logs, protocol and launch/review instructions.

- CLI and worker20 binaries are copied to `frozen/`, alongside worker source and
  the four execution scripts (`prepare`, `client`, `browse`, `view`).
- SDK 10.0.401 is hash-pinned. The selected project is
  `src/Quartz/Quartz.csproj`, Debug/net10.0, including its analyzer project.
- Public NuGet restore and upstream build passed with zero warnings/errors.
- A fresh checkout, copied dependency bundle and cleared package feeds also
  restored and built with zero warnings/errors. Vulnerability-audit network access
  was disabled for that offline prerequisite check.
- The dependency bundle contains 1,626 files, totaling 208,193,532 bytes.
- All 24 journey-harness tests pass. `view.py` is now maintained beside the other
  scripts; tests verify exact logged instruction-page reconstruction, deadline
  handling, invalid selection, and rejection of abbreviated display-cap overrides.
- Three prior archives were verified unchanged: worker20 severity acceptance,
  worker19 diagnostics/startup, and the CleanArchitecture heldout journeys.

No upstream tracked files were edited. All 2,713 source hashes match across the
original, normal-build and offline-build checkouts. No GPU or subagents were used.
Only Python evaluation harness files and repository documentation changed; no Go
or compiler-worker implementation changed in this preparation phase.

## Launch contract and open gates

The frozen launch plan requests compiler capture with offline dependencies into a
fresh workspace. Publication will use an isolated graph-disabled snapshot with
lexical and compiler capability, and no embeddings. Capture is **not attempted**.
Failures must be retained; no tuning or silent source-only substitution is allowed.
A product correction would retire this repository to development evidence.

Before each assignment, native initialization and catalog setup must succeed
under the solver's intended loopback permissions. This live setup remains pending;
unit tests and a frozen script are not a preflight for the new index. Solvers must
read every initialization-instruction page through the logged wrapper before
queries. Limits remain 24 attempted calls, 131,072 full response bytes, 600 seconds
from assignment, and 8,192 bytes per displayed page. This is supervised evaluation,
not OS-enforced filesystem isolation; model token use is unknown.

The user's no-subagents instruction remains in force. Staged work requires a new
exception for one fresh oracle reviewer, six fresh task solvers and six fresh
answer reviewers, **one agent at a time**. The coordinator cannot serve as a blind
solver after authoring this oracle. No agents have been launched.

The paired CodeGraph arm still needs verified dependency closure, binary
provenance and isolated service setup. Both products must use the same pinned
source, prompts and scoring to support a competitive claim. This packet does not
close that gate or change previous scores.

## Durable evidence

[Sealed result](results/holdout-quartz-20261002/result.json): 51 evidence files,
2,342,747 bytes, excluding the result manifest itself. Result SHA-256:
`b6281d4f9406b7f32743fe669d8ed6d0ecab2f31190ca21fc4e6cad1de56f830`.

[Protocol](results/holdout-quartz-20261002/protocol.json),
[source oracle](results/holdout-quartz-20261002/source-gold.json),
[launch plan](results/holdout-quartz-20261002/launch-plan.json), and
[reviewer instructions](results/holdout-quartz-20261002/oracle-reviewer.txt).
Do not modify the archive in place; record any reviewed successor separately.
