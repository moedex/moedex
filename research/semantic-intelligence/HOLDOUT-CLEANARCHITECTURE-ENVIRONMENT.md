# CleanArchitecture capture environment

Subsequent status: the [completed source-only holdout run](HOLDOUT-CLEANARCHITECTURE-ACCEPTANCE.md)
retains a compiler setup failure and one transport-blocked assignment. Historical
preparation details below and their immutable archives remain unchanged.

2026-10-02. Environment preparation only; no holdout extraction, indexing, product
queries, solver answers or quality scores. All work was single-threaded at the
agent level. CPU concurrency was limited to two for .NET commands; no embeddings
or GPU were used.

## Verified prerequisites

The source packet remains pinned at
`5353a9edae000d576eade1a4f2c0d72d3b1c1785`. Microsoft lists SDK10.0.401 in its
[official release metadata](https://dotnetcli.blob.core.windows.net/dotnet/release-metadata/10.0/releases.json).
The 230,753,980-byte macOS ARM64 archive was downloaded to the ignored workspace,
SHA-512 verified against that metadata, and extracted without changing a system
SDK. Its exact metadata and selection are retained.

An isolated clone restored from public NuGet and built `src/Web/Web.csproj` with
zero errors. The six-project closure is Web, Application, Domain, Infrastructure,
Shared and ServiceDefaults, all Debug/net10.0. MSBuild evaluated 74 distinct source
inputs, including all 17 source files cited by the oracle. All 258 tracked file
hashes remain unchanged in the original, build and clean offline checkouts.

This uses the checked-in template source with no custom conditional constants;
MSBuild reports TRACE/DEBUG, and the infrastructure's conditional provider falls
back to SQLite. PostgreSQL/SQL Server alternatives remain visible source but are
not compiled as active branches in this configuration. OpenAPI document generation
was disabled for build preparation to avoid executing the application. No publish,
SPA build, database initialization or application startup was performed.

The build emits **nine warnings**: Microsoft.NET.StringTools,
Microsoft.Build.Utilities.Core and Microsoft.Build.Tasks.Core version18.10.1 each
warn in Web, Application and Infrastructure that their packages do not support
net10.0. These are the pinned upstream versions; neither source nor versions were
changed to suppress them. Build success does not establish runtime compatibility.
The warnings remain in full build logs and are a capture-readiness limitation.

The frozen dependency bundle contains 5,528 files / 797,803,053 bytes, manifest
SHA-256 `04f34796ab256ddf9bab87a240048b8b7956dec1f467ee6acac8853942be645b`.
A second clean checkout restored successfully with package sources cleared,
NuGet audit disabled, and only those local packages available. Every bundle file's
hash/size still matches after restore. Packing used an explicit 2 GiB ceiling;
the result also fits the capture default of 1 GiB.

An isolated copy of the unchanged semantic worker builds against this SDK with
zero warnings/errors. A tiny synthetic fixture completes extraction with worker18,
Roslyn5.9.0.0, one complete project, four declarations and 35 references; its source
hash matches. This tests basic toolchain loading, not the holdout or full framework
regression coverage. Existing development results used a different compiler build
and are not re-scored or pooled.

## CodeGraph setup inventory

The earlier broad statement that CodeGraph is unavailable due to private packages
and MySQL needs qualification. MySQL client/server executables exist locally, and
the reference contains prebuilt API and indexer output with 170 and 188 DLLs.
Their source-to-binary provenance is not established. Recorded NuGet asset files
point at `/Users/ZKeown/.nuget/packages/`, where 150 API packages and 174 indexer
packages are absent. The asset rosters include 62 and 58 private TC packages;
the current user's default NuGet cache contains no TC package directories.
This inventory does not prove packages are unavailable from every possible feed.

Neither Consul nor Docker is on PATH. Read-only listener inventory sees no listener
on 3306, 8500, 5037 or 5042; it does not establish remote service availability or
account/database state. The native API startup removes appsettings JSON sources,
uses Consul-oriented service configuration, binds port5037 on all interfaces,
requires MCP PAT authorization, and starts best-effort MCP documentation generation.
The real arm still needs a reproducible dependency closure or verified binary
provenance, isolated service configuration, database schema and authentication.
No host was started, private feed contacted, credentials read, or database touched.
This is setup status, not a failed product task or a substitute component comparison.

## Reviewable next step

The readiness archive (archival evidence maintained separately) includes
commands, logs, evaluated project inputs, SDK/dependency/worker hashes, a concrete
unexecuted capture plan, and a source-only oracle-review assignment. Source and
task archives remain unchanged. The plan requires checking complete contexts,
project closure and cited source hashes before publication.

Independent oracle review remains the next gate, before any holdout product output.
The proposed execution exception is **one fresh agent at a time**: one source
reviewer, six task solvers, and six answer reviewers. The user's current no-subagents
instruction remains in force, so none has been launched. Gold amendments must be
retained and frozen before capture. CodeGraph paired execution remains a separate
unready arm; no competitive claim follows from a Moedex-only run.
