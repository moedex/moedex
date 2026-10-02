# Roslyn compiler acceptance: offline capture and MCP passed

The original `dotnet/roslyn` ErrorFacts generator project passed all **10/10
source-frozen binding cases**, both in raw worker output and in the validated
semantic artifact. The production capture used a verified dependency bundle,
cleared NuGet feeds, and an OS profile denying outbound IP connections. It
produced one complete context, three sources, 73 symbols, and 270 occurrence/binding
pairs; its four diagnostics were hidden `CS8019` unnecessary-using diagnostics.

[The durable result record](results/roslyn-compiler-20260930.json) contains SDK,
package, bundle, binary, worker, artifact, and frozen-gold provenance. This gate
covers one original project and ten selected cases. It does not measure
whole-Roslyn semantic coverage, compiler precision/recall, or a CodeGraph win.
The artifact attached to full-corpus lexical/ranking snapshot
`20260930T230839Z-1`; production MCP passed the same **10/10** cases and the
local `Generate` definition. This snapshot explicitly uses `--graph=false`; the
separate syntax-graph scale trial failed and is retained below.

## Public source projection acceptance

The production public Git projection passed on the complete pinned checkout:
35,115 tracked files and 456,238,809 exact bytes, expected origin
`https://github.com/dotnet/roslyn.git`, no project ID, and an explicit 512 MiB
budget. Projection creation and a second inventory/identity verification both
passed; the temporary projection was removed. Test wall time was 161.35 seconds.
Reproduce without fetching or compiling upstream code:

```sh
go test ./internal/semanticrun -run TestPublicGitProjectionActualRoslyn -count=1 -v \
  -args -public-git-checkout /private/tmp/moedex-public-oracle/corpus/roslyn
```

The default suite skips this explicitly provisioned corpus check. Full default
tests, build, vet, focused source-projection regressions, and affected race tests
also passed. Projection success establishes source provenance and capacity only.

## Bounded first target

Use the existing project `src/Tools/CompilerGeneratorTools/Source/CSharpErrorFactsGenerator/CSharpErrorFactsGenerator.csproj`, configuration Debug, framework net10.0. Its original project has no direct PackageReference or ProjectReference, and its source is one 5,858-byte `Program.cs`, including the original UTF-8 BOM. The program contains a local method call, SDK overloads, a generic extension call with a lambda, and generic collection calls. This is a useful compiler binding smoke test without attempting Roslyn's compiler or workspace project graph. Executing the generator's application entry point is unnecessary.

The ten labels in [roslyn-errorfacts-gold.json](roslyn-errorfacts-gold.json) were authored from source **before any compiler execution on this project**. They freeze exact raw byte offsets, token lengths, source digest, reference kinds, and expected documentation-comment descriptors. Cases cover the local `Generate` method, `File.ReadAllLines`, generic `Enumerable.Select`, lambda receiver `string.Trim`, `string.StartsWith(string, StringComparison)`, `string.IndexOf(char)`, `string.Substring(int, int)`, generic `List<T>.Add`, the parameterless `StringBuilder` constructor, and `Encoding.UTF8`. The local target must also have a matching source declaration. The other cases intentionally target SDK metadata, for which a local source definition is not required.

Initial gold SHA256: `4de2d5ed0c4afe8afe00d29d26092321e3086e4e9e45b613669fcf906b2a2309`. The gold also records hashes of the original project, source, and 15 observed root/import configuration files. This is a pre-evaluation input roster, **not** a claim that the complete evaluated import/dependency closure has already been captured. All ten cases passed in both raw worker output and the production-imported artifact. Ten selected cases do not establish whole-project precision/recall or a CodeGraph comparison.

A subsequent richer target is the original `CSharpSyntaxGenerator/CSharpSyntaxGenerator.csproj` beside it. It has 17 local C# files plus four explicitly linked compiler source files for its net10.0 target, with no direct project references. It adds model/type relationships and cross-file calls, but also direct analyzer/library packages and an additional netstandard2.0 target. Do not substitute its entire multi-target build for the bounded first gate.

## Original build requirements and acquired dependencies

Relative paths below refer to the pinned upstream checkout:

- `global.json` pins SDK `11.0.100-rc.1.26425.128`, allows prereleases, and uses `rollForward: patch`. It also declares runtime `10.0.10` in its tools configuration. The initial isolated toolchain had only SDK10.0.100; the exact SDK11 archive is now acquired and verified. Changing or bypassing this upstream pin would change the acceptance configuration.
- Root `Directory.Build.props` imports `Microsoft.DotNet.Arcade.Sdk` before `eng/targets/Settings.props`. `global.json` pins Arcade to `12.0.0-beta.26479.3`. Root `Directory.Build.targets` imports Arcade targets plus Roslyn's before/after targets. The Tools-level props explicitly imports the ancestor props, so selecting a small project does not bypass Arcade.
- `eng/targets/TargetFrameworks.props` sets `NetRoslyn` to net10.0 in the ordinary build. The project uses this property, rather than declaring net10.0 literally.
- `eng/targets/Settings.props:164` adds three analyzer packages for ordinary builds: `Roslyn.Diagnostics.Analyzers` `5.12.0-1.26477.1`, `Microsoft.VisualStudio.Threading.Analyzers` `18.7.131`, and `Microsoft.CodeAnalysis.PerformanceSensitiveAnalyzers` `3.3.4-beta1.22504.1`. Versions come from `eng/Packages.props` and `eng/Version.Details.props`. The source-only-build condition can disable them, but selecting that condition solely to avoid dependency acquisition would be a different build configuration and is not this gate.
- The original `NuGet.config` declares public Microsoft Azure DevOps feeds, including dotnet-eng, dotnet9, dotnet7-transport, dotnet11, and dotnet-public. A full evaluated restore may add Arcade-injected packages, targeting packs, apphost packs, and transitive dependencies. The three analyzer pins are therefore a known lower bound, not a complete offline package manifest.
- The inspected normal user package cache did not contain Arcade; the isolated dependency-free worker setup does not establish availability of this Roslyn build closure. The first isolated restore failed before resolving the closure; the subsequent approved standard-evaluation restore resolved the ten-package cache recorded below.
- Moedex's worker currently targets net10.0 and binds to SDK-bundled dotnet-format/Roslyn and MSBuild assemblies. An isolated worker copy targeting net11.0 subsequently built and captured this project successfully with SDK11; its project adjustment and binary hashes are recorded. This does not establish compatibility with every SDK version.
- An empty-feed restore into a fresh private package cache cannot resolve these unstaged dependencies. A public Git source adapter and a larger explicit projection budget address source provenance and size, but do not supply SDK or package dependencies.

## Official SDK metadata checked

Microsoft's [11.0 release metadata](https://builds.dotnet.microsoft.com/dotnet/release-metadata/11.0/releases.json) lists SDK `11.0.100-rc.1.26425.128` and the macOS ARM64 archive:

`https://builds.dotnet.microsoft.com/dotnet/Sdk/11.0.100-rc.1.26425.128/dotnet-sdk-11.0.100-rc.1.26425.128-osx-arm64.tar.gz`

Published SHA512: `969b6f38a0ebe853dd632dc9ef07dd0690cc96bfe51d6379d09d7a405f471602ab5e5e09c505f937836fa9ee9717a0807a8c828599c87865528fd952c8c6958b`.

The exact archive was subsequently downloaded: **197,522,617 bytes**, with the published SHA512 verified before extraction into `/private/tmp/moedex-roslyn-compiler/dotnet`. Acquisition metadata is retained in `sdk-acquisition.json`; SDK/MSBuild inventory is in `sdk-info.json` and `sdk-info.stdout`. The installed SDK reports MSBuild `18.11.0-1.26425.128+3551975be`.

An isolated copy of Moedex’s worker was retargeted from net10.0 to net11.0 to match the SDK11-bundled Roslyn and BuildHost. It built with zero warnings/errors; `worker-adjustment.json` records the original and adjusted project hashes, and `worker-build.*` records the command and logs. No upstream Roslyn project or SDK pin was changed. The subsequent successful capture established compatibility for this pinned project/configuration.

The exact Arcade package was also retrieved from the public dotnet-eng feed, with acquisition evidence in `arcade-acquisition.json`; this does not establish the complete dependency closure. The first original-project restore used only its existing public NuGet configuration and isolated home/temp/package directories, under a 600-second timeout and bounded logs. It exited 1 after 199.656 seconds: service-index loading failed across the configured feeds, then MSB4236 reported Arcade unavailable. Stderr also contained a macOS `CSSM_ModuleLoad` warning; that warning alone is not a proven root cause. Retain `restore-discovery.*` and `restore.binlog` as the failed attempt.

## Replay the measured capture

Keep the upstream checkout at commit
`36d26c5466e4d25940657ccb8d5b9557ccaf7be1` and retain its original project,
props, targets, source bytes, and `global.json`. Provision the SDK archive above
and the exact packages in the result record into isolated directories. The
successful discovery restore used the original public `NuGet.config`, Debug,
net10.0, `--disable-parallel`, `-p:NuGetAudit=false`, and
`-p:RestoreUseStaticGraphEvaluation=false`; its exact command/environment are
retained in `restore-standard-approved.json` outside the repository. Do not
reuse an ambient user package cache or copy absolute-path assets between
workspaces.

Freeze the provisioned cache using the production command:

```sh
moedex semantic dependencies pack \
  --packages /private/tmp/moedex-roslyn-compiler/packages \
  --output /private/tmp/moedex-roslyn-compiler/new-bundle
```

The measured bundle manifest SHA256 was
`88bc84bb4c21f3854107557e425dc53402d1b8569d0111f033299181fc1f44e9`.
Use a new workspace/output for each replay. With the recorded binary and worker,
the measured command was equivalent to:

```sh
base=/private/tmp/moedex-roslyn-compiler
/usr/bin/sandbox-exec -p '(version 1)(allow default)(deny network-outbound (remote ip "*:*"))' \
  "$base/moedex" semantic capture \
  --checkout /private/tmp/moedex-public-oracle/corpus/roslyn \
  --repo roslyn --origin https://github.com/dotnet/roslyn.git \
  --commit 36d26c5466e4d25940657ccb8d5b9557ccaf7be1 \
  --project src/Tools/CompilerGeneratorTools/Source/CSharpErrorFactsGenerator/CSharpErrorFactsGenerator.csproj \
  --framework net10.0 --configuration Debug \
  --dotnet "$base/dotnet/dotnet" \
  --sdk-path "$base/dotnet/sdk/11.0.100-rc.1.26425.128" \
  --worker "$base/worker/bin/Debug/net11.0/Moedex.SemanticWorker.dll" \
  --workspace "$base/new-capture-workspace" --output "$base/new-capture.semantic" \
  --dependency-bundle "$base/new-bundle" --max-projection-bytes 536870912 \
  --restore-standard-evaluation --restore-offline --timeout 15m
```

The actual command is retained as `capture-offline.sh`, alongside
`capture.stdout`, `capture.stderr`, and `capture.semantic`. The network check
returned permission-denied errno 1 for both IPv4 and IPv6. This macOS profile
specifically denied outbound IP; cleared feeds alone would not establish that
boundary. The full source projection was 35,115 files / 456,238,809 bytes; the
compiler capture covered only the selected project. The retained workspace
reported in capture output is required for later snapshot admission and source
revalidation. The worker captures observed inputs; this is not an immutable-build
attestation or proof of arbitrary upstream build safety.

## Snapshot and executable MCP acceptance

The published snapshot `20260930T230839Z-1` contains three lexical shards, a
43,057,564-byte symbol sidecar, a 103,192,408-byte token sidecar, the 438,373-byte
audit artifact, and its 103,158-byte compact compiler index. Its capability list
is `rank_symbols`, `rank_tokens`, and `search`; compiler components explicitly
record selected-project coverage, and no graph capability is advertised.

The tracked checker passed **10/10** frozen cases, including the local `Generate`
definition, against one generation and the exact artifact/source/context hashes.
The actual stdio server loaded both ranking sidecars from cache: 31,998 blobs/docs
and 17,591 symbol-bearing blobs. The probe completed in 6.857 seconds and the
server exited 0. This is one observed integration run, not a latency benchmark.

Logs and verdict are retained in `/private/tmp/moedex-roslyn-compiler/mcp-final`;
the durable result record includes their hashes, verdict, and published snapshot
manifest. [Requests](results/roslyn-compiler-20260930/mcp-input.jsonl),
[responses](results/roslyn-compiler-20260930/mcp.jsonl), and the
[verdict](results/roslyn-compiler-20260930/mcp-evaluation.json) are retained in the
repository as well. The daemon restart lost the build process session exit status; CURRENT,
manifest inspection, artifact hashes, and the successful executable MCP run
independently verify publication.

The final build explicitly selects the lexical/ranking and compiler components:

```sh
/private/tmp/moedex-roslyn-compiler/moedex-final index snapshot build \
  --corpus /private/tmp/moedex-public-oracle/corpus/roslyn \
  --index-dir /private/tmp/moedex-roslyn-compiler/index-compiler \
  --semantic-artifact /private/tmp/moedex-roslyn-compiler/capture.semantic \
  --semantic-workspace /private/tmp/moedex-roslyn-compiler/capture-workspace/semantic-git-projection-2659488801/source \
  --graph=false -v
```

For a replay, use the exact retained workspace reported by its capture and a
fresh index root, then point the checker below at that root.

Replay the production MCP gate after snapshot publication using the tracked
stdlib-only checker. Its output directory must not already exist. It starts
only the specified local stdio server; no endpoint or network client is used.

```sh
python3 research/semantic-intelligence/check-roslyn-mcp.py \
  --binary /private/tmp/moedex-roslyn-compiler/moedex-final \
  --index-dir /private/tmp/moedex-roslyn-compiler/index-compiler \
  --artifact /private/tmp/moedex-roslyn-compiler/capture.semantic \
  --output-dir /private/tmp/moedex-roslyn-compiler/new-mcp-check
```

The sibling frozen gold is the default (`--gold` can select an explicit file).
The checker requires exact context/source hashes, all ten labeled bindings, the
local definition, the supplied artifact hash, and one serving generation. It
retains requests, parsed responses, raw stdout/stderr, and `mcp-evaluation.json`
on success or failure. Responses have a 120-second timeout, the whole probe
defaults to 600 seconds, each output stream is capped at 16 MiB, and owned
process descendants are cleaned up. These probe deadlines are independent of
the earlier compiler capture deadline.

## Dependency discovery follow-up

The approved retry could load the public feeds and resolve Arcade, but the pinned
SDK's static-graph restore failed with `NullReferenceException` in
`MSBuildStaticGraphRestore.GetDependencyGraphSpec`. The exact null cause was not
established. A subsequent restore of the same original project succeeded in
31.797 seconds with the sole additional evaluation setting
`-p:RestoreUseStaticGraphEvaluation=false`. No upstream file or dependency pin was
changed. This is the documented standard MSBuild evaluation path, using the same
NuGet dependency resolution algorithm; see [Microsoft's restore documentation](https://learn.microsoft.com/en-us/nuget/reference/msbuild-targets#restoring-with-msbuild-static-graph-evaluation).

Evidence under `/private/tmp/moedex-roslyn-compiler` includes
`restore-discovery-approved.*` for the failed static-graph attempt and
`restore-standard-approved.*` for the successful restore, each with exact
command, isolated environment, timing, bounded logs, and binary log. The cache
inventory `dependency-acquisition.json` records ten packages, 1,570 files, and
226,233,999 unpacked bytes, including archive bytes. Each entry records the feed
from NuGet's acquisition metadata, archive SHA256/SHA512, verified NuGet archive
SHA512, and extracted file hashes, sizes, modes, and executable flags.

The resolved cache contains Arcade `12.0.0-beta.26479.3`; Roslyn diagnostics,
banned-API, and public-API analyzers `5.12.0-1.26477.1`; threading analyzers
`18.7.131`; performance-sensitive analyzers `3.3.4-beta1.22504.1`; and net10.0
reference/apphost packs `10.0.12` (NETCore, ASPNetCore, WindowsDesktop, and
osx-arm64 apphost). This is the observed cache inventory, not a minimal closure
claim.

`source-after-restore.json` verifies every one of the 35,115 original projected
files, totaling 456,238,809 bytes, against its frozen SHA256 roster after restore:
zero mismatches. Generated extras are deliberately outside this original-source
check. The roster file SHA256 is
`0c36b5aabececa8c0d0c22666e4236fd9903d7a4ea122efeb2c3003f2dab9888`.
The subsequent offline production capture and all ten frozen cases passed as
recorded above. Source checks and package hashes provide provenance, not a
whole-repository semantic quality estimate.

## Full-repository syntax-graph scale failure

The first snapshot attempt was interrupted after native sampling identified
quadratic per-file reconstruction of the symbol-name index. Bulk extraction and
decoding now build that inverted view once; deterministic parity, mutation,
serialization, and focused race tests pass.

The corrected run completed a 43,057,564-byte symbol sidecar, then prepared
167,750 graph names in 423.234 seconds. Its scheduler planned 237,535 batches on
18 workers; `Main` alone had a 993,188,595 candidate-pair upper bound and 43,365
batches. At 90 seconds, only 47 batches had completed. The disposable run was
interrupted (exit 130), retaining evidence as a failed graph-scale gate. These
are candidate upper bounds, not accepted semantic edges.

Compiler acceptance used explicit `--graph=false`, preserving full eligible
lexical coverage and the recorded selected-project compiler context while
advertising no syntax-graph capability. Graph construction remains enabled by
default. This component selection does not convert the failed graph-scale gate
into a pass; broad candidate fan-out remains a separate performance blocker.
