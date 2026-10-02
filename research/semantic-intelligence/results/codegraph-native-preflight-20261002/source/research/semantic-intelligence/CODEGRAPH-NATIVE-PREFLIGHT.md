# Native CodeGraph provenance gate

2026-10-02. Read-only development setup audit; no product execution or score.

The prebuilt API and indexer are not a coherent build of the checked-out reference.
Portable PDB metadata links to each inspected CodeGraph DLL, but document checksums
show source disagreement. This replaces an unverified-provenance concern with
specific, reproducible evidence.

| Check | API | Indexer |
| --- | ---: | ---: |
| CodeGraph assemblies inspected / linked PDBs | 24 / 24 | 20 / 20 |
| Non-generated source documents matching | 551 | 468 |
| Non-generated source documents mismatching | 2 | 30 |
| Non-generated source documents missing | 0 | 11 |
| Generated documents matching / mismatching / missing | 50 / 23 / 4 | 42 / 19 / 7 |
| Resolved package entries in existing assets file | 150 | 174 |
| TC.* package entries | 62 | 58 |
| Package directories missing from recorded cache | 150 | 174 |
| Package directories missing from default user cache | 150 | 174 |

Counts are PDB document entries, not a claim of exhaustive source coverage.
The two API source mismatches are
`TC.CodeGraphApi.McpHub.Provider.GitLab/GitLabMcpServer.MrReview.cs` and
`TC.CodeGraphApi.McpHub/McpHubExecutionHelper.cs` under `src/`.
Indexer mismatches include data models/store, service configuration and pipeline
code. The exact full rosters and expected/current checksums are retained in the
[setup record](results/codegraph-native-preflight-20261002/evidence/final/setup.json)
and raw API/indexer metadata reports.

Of 154 DLL filenames shared between the host directories, **15 differ in hash**.
They include CodeGraph Data, Models, Services and Host.Shared, plus
`TC.Jarvis.ServiceRegistry.Models.dll` and `YamlDotNet.dll`. Different hashes alone
do not prove incompatibility; combined with source mismatches they rule out
claiming these directories as one verified build of the current reference.

## Reproducible preflight

The [metadata probe and runner](codegraph-native/README.md) use .NET metadata APIs
without loading or executing inspected assemblies. The probe builds offline with
no external packages. Five controls pass: unchanged source, modified source,
missing source, unrelated PDB and missing PDB. Its generated setup record is
explicitly `ready: false` and can feed the paired harness as setup evidence.
An audit completing successfully is not a native product passing setup.

PDB linkage/checksums establish consistency rather than trusted provenance. No
private dependency build history, exhaustive input roster, runtime dependency
closure or live service readiness is proven. Missing recorded/default caches do
not exclude alternative bundles or authorized feeds. No feed, credentials, host
or database was accessed; no CodeGraph source/binaries were edited.

## Concrete next inputs and sequence

1. Obtain one coherent source/build bundle, or a local package bundle covering
   the pinned package identities in the setup record. Freeze source/project/import
   files, SDK, package hashes and build commands. The current reference lacks Git
   metadata, so use a full content manifest unless a corresponding revision is
   supplied. Do not reconstruct package provenance merely from loose DLLs.
2. Rebuild API and indexer together in a fresh isolated workspace. Re-run PDB
   consistency checks, review generated/missing inputs, and verify runtime closure.
   Preserve failed attempts separately; do not substitute the older indexer silently.
3. Prepare isolated native configuration, schema and authentication. Source
   inspection shows `Program.cs` removes appsettings JSON, uses Consul-oriented
   startup, and binds API/indexer to all interfaces on 5037/5042. API startup maps
   PAT-protected MCP and schedules documentation generation. These behaviors must
   be accounted for before launch; setting `ASPNETCORE_URLS` alone does not replace
   the explicit listeners. No operational configuration has been attempted.
4. Complete native initialization/catalog and indexing smoke, then freeze both
   arms for a fresh corpus/protocol. Use the paired harness to account for every
   task, including any failed setup. Quartz is development evidence after tuning.

The package/build-bundle location has been requested from the user. Other source
or package locations have not been exhaustively searched. This gate does not
require changing Moedex or weakening the comparison to an extraction-only arm.

## Evidence

[Sealed manifest](results/codegraph-native-preflight-20261002/result.json) includes
raw metadata reports, source snapshots of the probe/runner, fixture controls,
build logs and hashes of inspected binaries and assets files. It verifies the
preceding compact-artifact archive remains unchanged. No agents, GPU work, native
CodeGraph queries, holdout answers or benchmark scores were added.
