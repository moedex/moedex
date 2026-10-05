# Worker19 tuple capture and evaluation startup

2026-10-02. Named tuple fields no longer abort the compiler worker. Native MCP
startup now checks connectivity and retains initialization instructions before
assignment. These are development regressions and an unscored transport smoke;
there is no new independent holdout score or paired CodeGraph result.

## Compiler investigation and fix

The earlier CleanArchitecture capture failure contained two independent problems.
An instrumented copy of worker18, run against the unchanged restored checkout,
reported 15 workspace events with kind `Failure` (nine distinct messages). It
also aborted while identifying `(string Summary, string Description).Summary`:
Roslyn gives this tuple alias a source location but a `System.Runtime` containing
assembly. The worker incorrectly required that combination to identify a source
project.

Worker19 maps tuple element aliases to their actual `System.ValueTuple` storage
fields before serializing symbol identity. It follows the `Rest` chain for long
tuples. Synthetic tests verify that named and positional aliases have identical
identities, distinct storage fields remain distinct, and two- and nine-element
tuples extract successfully. The Go importer admits the complete tuple artifact.
Version gates accept worker19, retain older versions and reject the next unknown
version; framework rule scope is unchanged.

The final worker now reaches a terminal result on CleanArchitecture: six
projects, 778 declarations and 4,106 references, **incomplete**. It no longer
throws the tuple ownership exception. All 258 tracked source hashes remain
unchanged. This rerun is development evidence after debugging on the repository;
it is not another heldout trial.

## Workspace failures remain a capture blocker

A separate minimal project emits an ordinary MSBuild `Warning` during its
design-time compile. With SDK10.0.401/Roslyn5.9, the public workspace reports it
as `Failure`, even without `TreatWarningsAsErrors`. A corresponding `Error`
fixture is also incomplete. Both streams are rejected by the Go import command,
with no artifact published.

The retained Roslyn source checkout at
`36d26c5466e4d25940657ccb8d5b9557ccaf7be1` corroborates the mechanism:
`MSBuildDiagnosticLogger` distinguishes warnings and errors, but
`DiagnosticReporter.Report(IEnumerable<DiagnosticLogItem>)` sends both through
the `Log` path, which constructs a workspace failure. Those source files and
their hashes are retained with the evidence. This source inspection supplements
the actual SDK regression; it does not establish a source-to-binary match for the
SDK's Roslyn DLLs.

The worker therefore keeps its conservative policy: every workspace diagnostic
prevents complete status. It now includes the original workspace kind in the
emitted diagnostic message. There is no message-based warning allowlist,
suppression flag, project rewrite, package downgrade, or fabricated successful
capture. CleanArchitecture's remaining failures are still visible: 15 events
repeated in each of six project diagnostic streams, plus incomplete-context
markers. There are no compiler `CS` error diagnostics in this retained rerun.

The next compiler step needs an integration that preserves original build-event
severity. A supported severity-preserving API or a separately versioned, tested
workspace implementation is preferable to reconstructing severity from text.
Acceptance must include ordinary warnings, real load failures, identical
warning/error message text, missing references, and warning-as-error behavior.

## Native startup before assignment

The new journey `prepare.py` performs `initialize`, `notifications/initialized`
and `tools/list` before creating a task accounting directory. It retains raw
exchanges and a readiness report. A transport or protocol failure is setup
failure, not a scored attempt. Existing task directories cannot be reset or
recovered through this path. Stateful servers and incomplete catalogs are
explicitly unsupported by this stateless harness.

`browse.py instructions` displays native initialization results with the same
bounded pagination used for evidence. Future protocols must require reading all
instruction pages and recording their displayed views. The accounting client is
unchanged. The README explicitly requires setup and solver calls to use the same
network permission context: coordinator preflight cannot grant access to a
differently sandboxed solver.

A live source-only smoke retained all 22 tool descriptors and 1,177 instruction
bytes. Task accounting remained at zero calls/bytes with no clock started. The
first subsequent `list_repos` succeeded and charged one call and 414 complete
response bytes. This was not a solver retry or a replacement for the blocked
holdout task. The temporary server is stopped; all ten frozen index-file hashes
and all 637 files in the prior holdout archive remain unchanged.

## Validation and scope

- Full Go suite and `go vet ./...` pass.
- Existing worker integration passes, including its 19 fixture labels,
  configuration/context controls, generators, source escapes and mutation checks.
- New tuple/warning/error regression and importer admission/rejection controls pass.
- All 20 journey unit tests pass, including initialization paging, zero-budget
  setup, denied transport, existing-run rejection and malformed protocol controls.
- No GPU, subagents, product deployment, or paired CodeGraph execution was used.

See the sealed evidence manifest (archival evidence maintained separately)
and [journey setup instructions](agent-journeys/README.md). The previous
[source-only holdout result](HOLDOUT-CLEANARCHITECTURE-ACCEPTANCE.md) is unchanged.
A fresh independently reviewed holdout still follows compiler readiness; this
change does not close that gate.
