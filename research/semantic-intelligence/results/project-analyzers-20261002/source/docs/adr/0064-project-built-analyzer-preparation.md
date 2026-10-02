# ADR 0064: Prepare project-built analyzers in isolated compiler capture

- Status: accepted
- Date: 2026-10-02

## Context

Quartz's fresh capture failed because a project-produced analyzer DLL was absent
from the exact-commit projection. Its upstream build succeeded. A restore alone
does not produce analyzers, and globally selecting the application's framework
also retargeted its netstandard analyzer project incorrectly.

## Decision

Restore the declared project graph without a global TargetFramework. Then invoke
the pinned SDK's `ResolveReferences` target with the requested entry framework and
configuration, `BuildProjectReferences=true`, one MSBuild node and shared compiler
execution disabled. Keep the existing timeout, process-group cleanup, cleared
feeds, private package cache and source/dependency verification around this phase.
Do not copy canonical bin/obj directories or guess output paths. Preparation may
build ordinary referenced projects as well as analyzer/generator projects.

Worker21 opens the declared framework graph without a workspace-global target.
For each loaded project it matches the workspace output path to exactly one
evaluated declared-framework TargetPath. Missing or ambiguous matches fail closed.
It selects the requested entry framework and only its transitive compiler project
references. Analyzer projects remain build inputs, with their binary hashes in
the capture; they are not automatically emitted as application compiler contexts.
The worker's direct invocation expects reference preparation to have happened.
A missing analyzer produces an explicit error before extraction; it is never
silently omitted. Captures retain per-project selected properties and assembly
input hashes, not an attestation that arbitrary project build code is trustworthy.

Native diagnostic policy `native-build-events-v2` retains Roslyn diagnostics that
explicitly have `Warning` kind. Ambiguous workspace failures still need exact
native warning correspondence, with balanced successful builds and no native
errors or log-read failures. Counts distinguish workspace warnings from matched
native warnings; import and revalidation enforce them. Legacy workspaces without
native logs retain the diagnostic-free-only policy. Version20 and earlier readers
remain supported under their existing contracts.

An explicit `semantic capture --max-worker-bytes` can enlarge the JSONL transport
budget from its unchanged 64 MiB default to at most 256 MiB. The importer keeps
its separate source/input budget, and the persisted artifact limit remains
64 MiB. The capture summary records the effective worker budget. Larger wire
permission does not authorize oversized persisted artifacts or source inputs.

## Consequences and validation

Offline restore may require packages for all declared framework variants, even
though only one entry variant is selected for capture. Ambiguous output identities
and projects without a declared TargetFramework remain unsupported rather than
being guessed. Workspace diagnostics from the loaded graph can still block a
selected closure. This is trusted build execution, not an OS sandbox.

A deterministic netstandard source-generator fixture uses SDK custom artifacts
paths. Debug/Release produce different expected generated source and publish;
tampered analyzer outputs fail revalidation. A multi-target application retains
its netstandard library context and excludes the analyzer as a compiler context.
Broken generators and missing analyzers fail without artifacts. Go tests cover
phase ordering, mutation/failure cleanup, diagnostic proof and byte boundaries.

Quartz is now development evidence. Its retry reaches artifact serialization but
exceeds the unchanged persisted-artifact size limit. This is not a successful
publication or a fresh heldout score. Prior failed attempts remain immutable.
