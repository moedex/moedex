# ADR 0063: Preserve native build diagnostic severity

Status: accepted, 2026-10-02.

## Context

SDK10.0.401/Roslyn5.9 reports ordinary MSBuild warnings as workspace failures.
Rejecting every diagnostic blocked CleanArchitecture; ignoring warning-like text
could admit real load failures. The public workspace logger overload retains
binary logs from the actual design-time builds.

## Decision

Worker20 supplies `BinaryLogger` to `OpenProjectAsync` and replays its logs through
the selected SDK's public `BinaryLogReplayEventSource`. Workspace diagnostics can
be classified as warnings only when:

- Every diagnostic matches a typed warning event by exact project/message identity
  and multiplicity, with no leftover warning or diagnostic.
- Every log contains completed builds. Build/project starts and finishes balance,
  all finishes succeed, and every loaded source project is represented.
- No typed error, failed build, unsupported format, recoverable read error or
  incomplete log is present. Forward-compatible event skipping is disabled.

The exact English workspace message envelope is a correspondence format, not a
severity heuristic. Other formatting/localization fails closed. Independent
compiler/generator errors and source/input-integrity failures still block capture.
Identical warning/error text cannot bypass the typed-error check.

Capture identity includes normalized event evidence and the reader assembly
identity, excluding raw log timestamps and temporary paths. Raw logs are temporary,
removed on normal disposal, and bounded to 64 files, 128 MiB each and 256 MiB total.
Artifacts retain unique diagnostic facts; capture evidence retains event counts.

Older workspaces that ignore the logger overload retain their existing policy:
no logs **and no workspace diagnostics** can complete, subject to every other
capture check. This separate `workspace-no-diagnostics-v1` policy cannot downgrade
a diagnostic. Native evidence uses `native-build-events-v1`. Import and publication
revalidation require internally consistent worker20 evidence.

Compiler index and MCP implementation queries use the semantic layer's version
validator instead of divergent supported-worker lists. Earlier workers remain
readable; unknown future versions remain rejected.

## Evidence and limits

Tests cover warnings, identical-message errors, promoted compiler warnings,
missing projects, absent/truncated logs, and duplicate/missing/unmatched diagnostics.
Go admission rejects contradictory proof records. Existing integration passes on
SDK10.0.100 and SDK10.0.401; pinned Roslyn4.11/net8 also passes.

CleanArchitecture completes managed capture, isolated publication and native
compiler queries. This is development evidence, not fresh independent evaluation.
Normalized records are worker-produced evidence, not a signed build attestation
or an OS sandbox. See the
[acceptance report](../../research/semantic-intelligence/BUILD-EVENT-SEVERITY-ACCEPTANCE.md).

Public API references:
[binary-log replay](https://learn.microsoft.com/en-us/dotnet/api/microsoft.build.logging.binarylogreplayeventsource)
and [dotnet-format's workspace loader](https://source.dot.net/dotnet-format/Workspaces/MSBuildWorkspaceLoader.cs.html).
