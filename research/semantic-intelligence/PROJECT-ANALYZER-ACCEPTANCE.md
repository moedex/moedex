# Project-built analyzer acceptance and remaining artifact gate

Worker21 now captures code that depends on project-built analyzers and source
generators, including a netstandard generator and library behind a multi-target
application. Deterministic fixtures publish and serve through native MCP.
Quartz's development retry passes extraction/import and reaches serialization,
but its 225,620,341-byte encoded artifact exceeds the unchanged 64 MiB limit.
Quartz publication and competitive acceptance remain open.

## Implementation

Isolated capture now restores each project's declared framework graph, then runs
the pinned SDK's `ResolveReferences` with reference building enabled. Preparation
uses one build node, no shared compiler, the existing private cache/cleared feeds,
process deadline, and source/dependency integrity checks. Build errors or mutations
stop capture before the worker; failures leave no artifact or retained workspace.
Canonical checkout build outputs are never copied into the projection.

A workspace-global TargetFramework previously retargeted netstandard analyzer
projects to the entry application's framework. Worker21 instead loads the declared
graph, matches each workspace output path to exactly one evaluated framework,
selects the requested entry variant and follows compiler project references.
Analyzer-only projects remain hashed binary inputs rather than application source
contexts. Missing analyzers and ambiguous framework/output identities fail with
actionable diagnostics. Generated declarations remain marked generated.

The versioned `native-build-events-v2` policy distinguishes explicit Roslyn
`Warning` diagnostics from workspace `Failure` records. Failures still require
exact native warning correspondence; errors, failed builds, damaged logs and
unmatched diagnostics reject admission. Import and revalidation check counts and
policy/version compatibility. Older no-log workspaces retain their strict
no-diagnostics rule. Earlier extractor versions remain supported.

`--max-worker-bytes` provides an explicit JSONL budget up to 256 MiB, retaining the
64 MiB default. It does not enlarge source/input or persisted-artifact budgets.
The capture summary exposes the effective worker budget. Artifact oversize errors
now include the measured encoded size. See [ADR 0064](../../docs/adr/0064-project-built-analyzer-preparation.md).

## Evidence

The final offline fixture uses a multi-target net10.0/netstandard2.0 application,
a netstandard2.0 library and a netstandard2.0 source-generator project, with SDK
custom artifacts output paths. Its declared source is committed inside a
disposable local test checkout; the archive retains each case's exact source.

| Check | Result |
| --- | --- |
| Debug generated value 42, Release generated value 84 | Both complete and publish |
| Selected compiler contexts | Entry net10.0 plus library netstandard2.0; analyzer excluded as application context |
| Generated binding identity | Source reference and generated field declaration resolve to the same symbol in both configurations |
| Changed analyzer DLL | Publication revalidation rejects both variants |
| Generator compilation error | Reference-preparation failure; no artifact |
| Missing analyzer input | Explicit worker rejection; no artifact |
| Two declared frameworks with one output identity | Explicit ambiguity rejection; no artifact |
| Native MCP discovery and definitions on final fixture | Two successful calls, matching artifact/snapshot and source hash; server stopped |

The earlier single-target and initial multi-target fixture runs are retained too.
The final fixture uses the final worker source and binaries. The additional
binding-identity audit checks the generated symbol directly, not merely a nonempty
binding list.

Validation passed:

- Baseline and final `go test ./...`, final `go vet ./...`, and focused race tests
  for semanticrun and semanticimport.
- Full worker regression suites on SDK10.0.401 and SDK10.0.100: 19 fixture labels
  plus context/configuration, source/generator integrity, failure and warning-policy checks.
- Twelve native diagnostic controls, including real errors and damaged/missing
  logs or unmatched workspace failures.
- Pinned Roslyn4.11 with SDK8.0.400: five routing configurations, nineteen
  exclusions and incomplete suppression. An initial test invocation used a
  relative output directory unsupported by that runner; its failed log is retained,
  followed by the successful absolute-path invocation.

## Quartz development result

The original [heldout setup failure](HOLDOUT-QUARTZ-CAPTURE.md) remains immutable.
Five development attempts are separately recorded:

1. Reference preparation exposed the incorrectly restored netstandard target.
2. Corrected restore exposed workspace-global framework retargeting and output exhaustion.
3. Worker21 reached the default 64 MiB JSONL limit.
4. Explicit 256 MiB JSONL capture reached the persisted-artifact limit.
5. Final binaries confirmed the artifact size: **225,620,341 bytes**, about
   **215.17 MiB**, against **67,108,864 bytes** allowed; exit 1 in 59.28 seconds.

The last failure occurs in artifact writing, after the runner has imported,
validated and checked completeness of the worker stream. No Quartz artifact was
published; failed workspaces are cleaned by the runner. No context/binding-count
claim is reconstructed from a missing successful artifact. All 2,713 canonical
Quartz source hashes remain unchanged.

The next gate is compact artifact storage or an explicit artifact budget
supported consistently by writing, reading, publication and serving. Increasing
only the worker transport limit is insufficient. A new fresh holdout must follow
that integration work; Quartz is now development evidence. No independent solver
score or paired CodeGraph claim is added.

## Preserved evidence and limits

Sealed result (archival evidence maintained separately): 397 files,
10,129,941 bytes excluding its result manifest. SHA-256:
`8742e28d5ae694ff4bcffa837547d8773886f4b167c69bf414c2bfbc380cbfcb`.

Final fixture (archival evidence maintained separately),
generated binding audit (archival evidence maintained separately),
native MCP (archival evidence maintained separately), and
Quartz final failure (archival evidence maintained separately).

All files in five prior evidence archives were verified unchanged. No subagents
or GPU were used. This is trusted project-build execution in an isolated copy,
not an OS sandbox or signed build attestation. Restoring all declared frameworks
can require additional packages; ambiguous outputs and unsupported framework
identities fail closed. Diagnostics from unselected loaded projects can still
block capture. The existing source and artifact budgets remain enforced.
