# ADR 0030: Managed compiler capture and retained input revalidation

- Status: Accepted for explicit local capture; production rollout remains pending
- Date: 2026-09-30
- Predecessors: [0024](./0024-managed-lsp-workspace-isolation.md),
  [0027](./0027-compiler-worker-and-semantic-artifact.md),
  [0028](./0028-semantic-snapshot-attachments.md),
  [0029](./0029-compiler-lookup-index-and-tools.md)

## Decision

Add an explicit `semantic capture` command that selects a repository from the
existing managed catalog and acquisition lock, materializes its locked commit in
a fresh private workspace, performs explicitly authorized offline restore, runs
the compiler worker, validates its output, and stages a new semantic artifact.
Use the existing acquisition project ID, namespace and commit. Do not accept a
caller-supplied replacement commit or create another branch manifest.

The command requires `--managed-root`, `--repo`, `--project`, `--framework`,
`--dotnet`, `--worker`, `--sdk-path`, `--workspace`, `--output`, and an enabled
`--restore-offline` flag. Configuration defaults to `Debug`; the overall deadline
defaults to five minutes and must be positive. Toolchain paths identify an
already installed SDK and built worker. The command does not install tools,
deploy an index, or advance `CURRENT`.

`internal/cli` owns flags and JSON output, `internal/app/semanticcmd` owns the
application adapter, and `internal/semanticrun` owns projection, subprocess
lifecycle and validation. The existing worker/importer/artifact contracts remain
the semantic admission boundary.

## Exact source and execution boundary

Materialize exact committed Git blobs rather than copying a working tree or
using checkout filters. Dirty and untracked checkout files do not become compiler
inputs. Archive substitution is not used. Symlinks and submodules are unsupported
in this slice. Managed group policy and whole-workspace privacy eligibility apply
before project code executes. The projection is outside the canonical managed
corpus. Source inventories, acquisition metadata and privacy state are rechecked
around the compiler stages; source mutation cannot be silently accepted as the
locked commit.

A fresh projection has no usable restored `obj` state. Consequently restore is
an explicit opt-in, using a generated configuration with cleared package and
fallback feeds, a private empty feed/cache, isolated CLI home/temp paths, and an
explicit environment. This initial route supports projects whose framework
references are supplied by the installed SDK. It does not download packages or
automatically inherit the operator's NuGet sources, cache or credentials. Projects
requiring unavailable packages fail. Broader offline package provisioning is a
separate contract.

Subprocesses have deadlines, output limits and process-group cleanup, including
descendants retaining inherited pipe handles after their parent exits. Worker
stdout is limited to 64 MiB, stderr to 64 KiB; restore output has smaller limits.
The projection itself has file/byte limits. These are lifecycle and admission
controls, **not an OS sandbox**. MSBuild targets, analyzers and generators are
trusted executable code. Empty NuGet feeds do not prohibit arbitrary project
code from accessing the network, reading external files, escaping a process
group, or allocating memory/disk. There is no OS-enforced network, RSS or disk
quota claim.

## Success, retention and failure

The output artifact and workspace parent must be new destinations. The worker's
complete terminal stream, source/configuration bytes and recorded identities must
validate before the artifact is written. Nonzero subprocess exit, incomplete or
truncated capture, timeout, output overflow and validation failure produce no
successful artifact. Cleanup removes only newly owned failed staging after
subprocess cleanup; preexisting destinations are preserved.

On success retain the workspace at its original absolute path. The JSON summary
returns `artifact`, `workspace`, `repo`, `project_id`, `commit` and artifact counts.
The returned `workspace` is the **exact compiler projection root**, not the parent
passed with `--workspace`. Restored assets and captured paths can depend on that
location; copying or rebuilding a workspace is not equivalent revalidation.
There is no separate receipt format in this slice. The existing artifact's raw
source/configuration digests and acquisition provenance remain authoritative.

## Attachment boundary

`snapshot-build --semantic-artifact ARTIFACT --semantic-workspace ROOT` supplies
the retained projection explicitly. Recorded compiler inputs are revalidated
there; indexed source correspondence is independently checked
against the canonical indexed repository/path bytes, including ingestion's
documented leading-BOM normalization. A matching retained workspace cannot
substitute for matching indexed source, and an absent or changed workspace is not
silently replaced with the current checkout. Existing callers without a separate
workspace retain their previous revalidation behavior.

The result remains historical compiler evidence for captured contexts. It does
not reevaluate arbitrary source globs, reconstruct hidden generator dependencies,
prove runtime dispatch or establish incremental-versus-clean equivalence.

## Validation and remaining work

Application and CLI tests cover explicit restore consent, required execution
inputs, positive deadlines, context cancellation and destination preservation.
Projection/process tests and real managed source-to-artifact-to-attachment
acceptance are recorded separately in
[DELIVERY.md](../plans/semantic-intelligence/DELIVERY.md). Do not interpret passing
flag tests or a synthetic runner as a real compiler acceptance result.

Remaining gates include broader package graphs, multi-target contexts, OS-level
execution containment, resource measurements on representative projects,
incremental/clean equivalence, and independently reviewed paired CodeGraph tasks.
