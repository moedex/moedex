# .NET semantic worker foundation

This offline CLI loads a restored C# project with the real `MSBuildWorkspace`,
extracts compiler bindings, and writes `moedex.semantic-worker.v1` JSONL to stdout.
It does not restore packages, publish an index, or alter a serving snapshot.
The Go importer must validate the complete stream before admitting an artifact.

The first supported toolchain is the verified .NET SDK **10.0.100**. Build the
project using that SDK. Roslyn 5.0 and workspace dependencies come from its
`DotnetTools/dotnet-format` bundle; no NuGet dependencies are required. The entire
adjacent `BuildHost-netcore` directory is copied because workspace loading uses a
separate build-host process. This SDK layout is an explicit dependency, not a
portable assumption about every SDK release.

`--sdk-path` selects project evaluation; it does not replace the compiler and
workspace libraries bundled with the worker. Those libraries must support the
project's generators and language features. Inspection during the fresh-corpus
gate found that the retained SDK8 `dotnet-format` deployment actually reports
Roslyn **4.8.0.0** in its capture manifest. Earlier descriptions calling that
bundle 4.11 were incorrect; the hashed captures retain the actual version.

For SDK8 Razor projects, the verified opt-in build uses coherent Roslyn4.11
packages. Provision the exact dependencies explicitly, then build from the
retained cache with cleared feeds for offline replay:

```sh
dotnet build tools/semantic-dotnet/Moedex.SemanticWorker.csproj \
  -p:UsePinnedRoslyn=true --source https://api.nuget.org/v3/index.json \
  --packages /isolated/worker-packages
```

This pins compiler/workspaces4.11.0, Build.Locator1.7.8 and MSBuild compile
references17.11.4. MSBuild runtime assets are excluded so the selected installed
SDK supplies the engine. Package provisioning is opt-in; the default SDK-bundled
build remains package-free. Use separate output/build directories when switching
toolchains. Compiler, workspace and extractor assembly hashes remain part of capture identity.
The current extractor version is 10; readers retain support for earlier captures.

```sh
dotnet build tools/semantic-dotnet/Moedex.SemanticWorker.csproj -p:UseSharedCompilation=false
dotnet /path/to/Moedex.SemanticWorker.dll \
  --repo group/project --root /isolated/projection \
  --project App/App.csproj --configuration Debug --framework net10.0 \
  --sdk-path /path/to/dotnet/sdk/10.0.100 > capture.jsonl
```

`--repo`, `--root`, `--project`, `--framework`, and `--sdk-path` are required.
Configuration defaults to `Debug`. One invocation selects one explicit target
framework and configuration; it enumerates every project loaded through project
references. It does not claim to enumerate all multi-target framework variants.
`LoadMetadataForReferencedProjects=false` prevents fallback to stale built
project outputs. Non-C# dependencies and workspace diagnostics yield incomplete
status. Compilation errors remain visible and propagate to dependent contexts.
Because workspace compilation diagnostics omit generator-driver failures, a
public `CSharpGeneratorDriver` verification pass runs the same generators using
the workspace's actual parse options, additional texts, and analyzer-options
provider. It collects generator diagnostics and compares generated tree paths
and text digests with workspace output. Generator exceptions or disagreement
mark the context incomplete. Generators therefore execute twice; generators
with nondeterministic output cannot receive complete status from this worker.

Run against an already restored **disposable, writable projection**. MSBuild
evaluation executes project targets, and compiler generators/analyzers execute
code. This executable is **not a build sandbox**; the caller must isolate hostile
projects, environment, network, and credentials. It may write `obj/` through
design-time targets. Local build-host IPC must be permitted. Do not point it at a
canonical managed checkout. Production projection/process lifecycle integration
and cancellation/time/memory limits are not implemented by this standalone
worker. The managed capture command below owns projection, deadline, output and
process cleanup; OS-level memory and execution containment remain external.

Each project record includes evaluated project/import/package assets, compiler
and workspace identities, actual compiler options, metadata references, project
references (including aliases and embedded-interop settings), analyzers,
additional files, analyzer configurations, and raw source digests. Sensitive
arbitrary environment properties are not dumped. `capture_json` holds the exact
serialized capture; `build_context` is SHA256 of its UTF8 bytes. Referenced
project contexts are included transitively. Input records distinguish `source`,
`sdk`, and `external` scope for consumer verification. Source/config bytes are
rehashed before the final terminal row; a detected mutation fails the stream.
This is observed-input capture, **not an immutable-build attestation**: files can
change before first observation or after the final check, and arbitrary custom
target/generator reads, environment values, and transitive analyzer dependencies
are not comprehensively intercepted. The importer rechecks confined source and
configuration inputs at admission. SDK/external identities are retained for audit.

Physical source paths must remain inside the projection; escaping links and
external linked sources fail explicitly. Real source-generated documents receive
reserved `.generated/` paths and embedded raw UTF8 bytes. Disk-generated source
files also carry embedded bytes. Original UTF8/BOM/CRLF bytes are preserved and
verified against compiler text. Other source encodings fail explicitly; emitted
byte spans never refer to a transcoded stand-in. Local roots and physical paths
may appear inside capture evidence or diagnostic text; do not treat the output as
a redacted public report.

Symbols use Roslyn original-definition documentation descriptors where available.
The namespace is `repo/relative.csproj` for source symbols and assembly identity
for external symbols. `descriptor_kind: location_fallback` explicitly marks
locals and other symbols without documentation IDs; these are not edit-stable.
Merged namespaces use the global assembly namespace because no unique owning
project exists. References currently cover simple names, invocations, and object
creation. Compiler-generated implicit operations, every operator/conversion
binding, dynamic dispatch targets, and runtime call graphs are not claimed.

Extractor version 2 optionally attaches `domain_facts` to existing resolved
reference rows in complete contexts. These are compile-time facts under
`csharp-framework-v1`, with typed targets and the original source/build evidence:

- Microsoft DI generic `AddSingleton`, `AddScoped`, and `AddTransient` registrations
  with service/implementation types (or a self registration), without factories
  or instance arguments.
- MassTransit `IPublishEndpoint.Publish<T>` and class base-list `IConsumer<T>`
  references, retaining the message contract.
- EF Core `DbSet<T>` property types and generic `ToTable<T>` calls with constant
  nonempty table names and optional constant schemas.

Recognition requires the exact framework metadata type and assembly name;
project-local lookalikes do not qualify. Assembly bytes remain bound by the
captured metadata references. This is API identity evidence, not an authenticity
attestation for a package. Constructed/open generic target types, generic
containing types, unresolved/dynamic calls, factory bodies, private wrappers,
nonconstant table names, and incomplete contexts produce no positive domain
fact. Their ordinary compiler occurrences and diagnostics remain present.
Facts establish configuration sites and declared contracts, not actual service
activation, message delivery, database reads/writes, or an effective runtime
configuration. They do not change occurrence or stream-summary counts.
The allowlist follows the public
[Microsoft DI API](https://github.com/dotnet/runtime/blob/main/src/libraries/Microsoft.Extensions.DependencyInjection.Abstractions/src/ServiceCollectionServiceExtensions.cs),
[MassTransit publish contract](https://github.com/MassTransit/MassTransit/blob/develop/src/MassTransit.Abstractions/IPublishEndpoint.cs),
[consumer contract](https://github.com/MassTransit/MassTransit/blob/develop/src/MassTransit.Abstractions/IConsumer.cs),
and [EF relational builder API](https://github.com/dotnet/efcore/blob/main/src/EFCore.Relational/Extensions/RelationalEntityTypeBuilderExtensions.ToTable.cs).

Exit 0 means all loaded projects are complete; exit 2 means a structurally
complete stream with incomplete analysis; exit 1 means failure and **no terminal
summary**. Every successful stream terminates in `stream_summary` with exact
project/declaration/reference counts. Consumers must reject truncation regardless
of earlier project statuses. The format is a worker interchange contract; the
Go importer owns persisted semantic identities and artifact format.

Run the offline integration tests (build and all fixture work happen in a temp
directory; no corpus artifacts are written to this repository):

```sh
python3 tools/semantic-dotnet/test_worker.py \
  --dotnet /path/to/dotnet --sdk-path /path/to/dotnet/sdk/10.0.100
```

The suite checks 19 independently authored fixture reference labels, distinct
linked-file project contexts, UTF8/BOM/CRLF spans, exact capture digests, repeated
output determinism, configuration invalidation, missing restore status,
outside-source rejection, project-reference aliases, and repository namespaces.
It also exercises the SDK's real `GeneratedRegex` generator and a custom throwing
generator whose failure would otherwise be absent from compilation diagnostics.
Arbitrary package graphs, third-party incremental generator ecosystems, and
general analyzer execution diagnostics are not yet comprehensive test coverage.
An additional pipe-synchronized mutation control changes a source after its
project row is emitted and verifies that the worker fails without a terminal
summary.

## Managed capture

For a repository already acquired into Moedex's managed catalog and lock, use
the explicit capture command with an installed SDK and built worker:

```sh
moedex semantic capture \
  --managed-root /managed/corpus --repo group/project \
  --project App/App.csproj --framework net10.0 --configuration Debug \
  --dotnet /path/to/dotnet --sdk-path /path/to/dotnet/sdk/10.0.100 \
  --worker /path/to/Moedex.SemanticWorker.dll \
  --workspace /scratch/new-capture --output /scratch/new-artifact.json \
  --restore-offline --timeout 5m
```

Both destinations must be new. The runner projects exact locked Git blobs,
excluding dirty/untracked checkout content, and enforces managed workspace
privacy eligibility. It performs explicitly enabled restore using cleared feeds
and a private empty package cache. The initial route supports SDK framework
references without package downloads; unavailable packages fail. It then runs
the worker with bounded output and a deadline, validates the complete stream,
and stages the artifact. Failure cleans up newly owned workspace state.

Successful JSON output includes `artifact` and `workspace`. Keep the returned
workspace at that exact location: it is the compiler projection root, which is
nested inside the `--workspace` parent. Captured `obj`/assets/configuration inputs
must remain available for later attachment:

```sh
moedex index snapshot build --corpus /managed/corpus --index-dir /scratch/index \
  --semantic-artifact /scratch/new-artifact.json \
  --semantic-workspace /exact/workspace/from/capture/output
```

The attachment validates recorded inputs against the retained workspace and
source correspondence against canonical indexed bytes. There is no separate
receipt file. Do not recreate assets in a different directory and assume the
capture still matches.

This executes trusted MSBuild/analyzer/generator code. Empty package feeds and
process cleanup are not an OS network, memory or disk sandbox. See
[ADR 0030](../../docs/adr/0030-managed-compiler-capture.md) for the exact boundary.

## Public Git capture

For a public Git checkout, replace `--managed-root` with `--checkout /path/to/repo`,
`--commit FULL_COMMIT_ID`, and `--origin https://host/owner/repo.git`.
Use the canonical checkout directory basename as `--repo` so the artifact can
attach through unmanaged indexing. The checkout must be the repository root;
HEAD, configured origin, and raw tracked file bytes must match the selection.
Untracked build outputs are excluded. Origin is retained in the artifact's hashed
audit metadata without claiming network verification or remote ownership, and no
acquisition project ID is assigned.

`--max-projection-bytes 536870912` explicitly allows a 512 MiB source projection;
the default remains 256 MiB and the maximum is 1 GiB. Other artifact and worker
limits remain in force. Restore uses cleared feeds and a private package cache,
empty by default or populated from the explicit verified bundle described below.
In particular, successful Roslyn source projection does not imply its SDK and
Arcade requirements are available. See [ADR 0032](../../docs/adr/0032-public-git-compiler-capture.md).

## Explicit offline dependencies

For a project requiring packages, first acquire its dependency closure separately
into an isolated NuGet global-packages directory. Record the package sources and
archive hashes; packing does not authenticate a package's publisher.

```sh
moedex semantic dependencies pack \
  --packages /scratch/provisioned-packages --output /scratch/new-bundle
```

Add `--dependency-bundle /scratch/new-bundle` to the capture command, retaining
`--restore-offline`. The runner verifies and copies the complete manifest into
its private cache, keeps cleared feeds, and records `dependency_bundle_sha256`
in the result and artifact contexts. Missing packages or cache/manifest mutation
fail the capture. Keep the bundle and the retained capture workspace for audit;
the latter also contains `dependency-manifest.json`. See
[ADR 0033](../../docs/adr/0033-offline-compiler-dependency-bundles.md) for limits and
the distinction between cleared feeds and OS network isolation.

If a pinned SDK fails in NuGet static-graph restore,
`--restore-standard-evaluation` explicitly selects standard MSBuild evaluation
for the restore command only. The default preserves project-selected behavior;
this option does not rewrite upstream project files. It is recorded in capture
JSON as `restore_standard_evaluation`. Roslyn’s pinned SDK11 required this option
after its static-graph task threw a null-reference exception.

Snapshot building enables the syntax graph by default. Use explicit
`moedex index snapshot build --graph=false` when validating lexical/ranking and
compiler components independently. The resulting snapshot advertises no graph
capabilities; this is not a successful graph-scale run. The Roslyn acceptance
record retains its separately interrupted candidate-graph scale attempt.

## Worker7: closed interface members and keyed DI

Worker7 preserves qualified named arguments in `constructed_interface_method_v1`
identities for supported closed interface methods. Nongeneric source classes can
record `csharp-interface-closed-v1` correspondence; explicit default interface
bodies record `csharp-interface-default-v1` at their own declarations. The latter
are open templates and do not select a closed class or prove dispatch. Generic
classes/methods, nested interface containers, array/open/constructed generic type
arguments and inherited class correspondence remain outside this increment.

`csharp-keyed-di-v1` recognizes exact two-type Microsoft DI keyed registration
calls with a `typeof` key. A bounded source helper summary substitutes closed
arguments into a first-statement keyed registration, regardless of helper name.
It emits configuration evidence at the helper call, with service, implementation,
key type, exact metadata API witness and lifetime. Conditional/nested bodies,
factories, arbitrary keys, recursive helpers and incomplete contexts produce no
positive summary. This does not resolve later container mutations or prove runtime
service selection. See [ADR 0047](../../docs/adr/0047-closed-interface-and-keyed-registration-evidence.md).

Run `test_framework_bridge.py` with explicit `--dotnet`, `--sdk`, `--worker`,
`--packages` and a fresh `--output` directory. It pins SDK selection for offline
restore, then checks positive, negative and incomplete source fixtures. Pass its
`capture.jsonl` and source root as `MOEDEX_BRIDGE_STREAM` / `MOEDEX_BRIDGE_ROOT` to
`go test ./internal/semanticimport -run TestPublicFrameworkBridge` for import,
persistence, mapped-index and public compiler-tool verification.

## Worker8: closed-class default selection

`csharp-interface-selection-v1` records Roslyn's selected closed default at a
concrete class declaration, separately from the open template. Its
`selected_default_symbol_id` preserves qualified arguments;
`default_template_symbol_id` permits source-body lookup. Overrides, most-specific
defaults, and inherited defaults are resolved by the compiler. This rule does not
infer forwarding or runtime execution, and it does not create contract-path hops.

Run `test_default_selection.py` with the same explicit SDK/worker/package arguments
and a fresh output directory. Pass `capture.jsonl` and its root through
`MOEDEX_DEFAULT_STREAM` / `MOEDEX_DEFAULT_ROOT` to
`go test ./internal/semanticimport -run TestPublicDefaultSelection` for the public
import/index/query gate. See [ADR 0048](../../docs/adr/0048-closed-class-default-interface-selection.md)
for exact bounds and exclusions.

## Worker9: bounded default forwarding

A selected default may carry `forwarding` under `csharp-default-forward-v1`:
one call on `this`, casting the sole parameter to an interface type parameter,
with its closed interface target, concrete source implementation, qualified cast
type and exact source-call witness. Unsupported bodies retain selection-only
evidence. Cast success and runtime execution remain unproved.

Run `test_default_forwarding.py` with explicit `--dotnet`, `--sdk`, `--worker`,
`--packages` and a fresh `--output`. Pass `capture.jsonl` and its source root through
`MOEDEX_FORWARD_STREAM` / `MOEDEX_FORWARD_ROOT` to
`go test ./internal/semanticimport -run TestPublicDefaultForwarding` for the public
round-trip and corruption gate. See [ADR 0049](../../docs/adr/0049-bounded-default-interface-forwarding.md).

## Capture portability budgets

Managed capture creates a private toolchain directory exposing only the requested
installed `sdk/version` directory. It copies the small dotnet host executable and
links trusted installed SDK/runtime directories, so both restore and Roslyn's
child build host discover the selected SDK without editing committed `global.json`.
The SDK and runtimes must remain installed while the retained workspace is used.
This does not sandbox MSBuild or make runtime/toolchain files immutable.

Restore invokes the selected SDK's `MSBuild.dll` directly with cleared feeds.
Worker failures report bounded explicit error diagnostics; source declarations and
capture manifests are excluded from failure messages.

`semantic dependencies pack` and `semantic capture` both accept
`--max-dependency-bytes`. Zero retains the 1 GiB default. An explicit value up to
4 GiB permits larger complete caches; eShopOnWeb's frozen cache uses a 2 GiB budget.
Every file is still enumerated, hashed, privately staged and verified before and
after execution. The manifest/file-count limits and exclusive output rules remain.
Budgets must be supplied independently at pack and capture; a larger bundle does
not silently raise a later capture's default.


### Typed responses and conditional registrations (worker10)

`csharp-framework-v5` records two bounded compile-time observations:

- `message_response`: the message contract in the bound one-argument generic
  `MassTransit.ConsumeContext.RespondAsync<T>(T)` or `RespondAsync<T>(object)` API.
  It does not establish message delivery, a requester, or a request/response route.
- `di_registration_if_absent`: the service, implementation and lifetime in the
  parameterless generic `TryAddSingleton`, `TryAddScoped` or `TryAddTransient`
  extension, with one or two type arguments. The kind retains the API's
  if-absent condition; it does not prove registration occurred or select a runtime
  implementation. A surrounding branch remains outside this observation.

Matching requires exact metadata owner, assembly name and method signature.
Factory/instance/runtime-type DI overloads, response pipe/non-generic overloads,
source lookalikes, open or constructed generic targets, and incomplete
compilations do not receive these facts. Assembly names are provenance, not
package publisher authentication. Run `test_response_di.py` against the cached
MassTransit.Abstractions8.2.1 package and SDK8 framework references, then set
`MOEDEX_RESPONSE_DI_STREAM` and `MOEDEX_RESPONSE_DI_ROOT` for
`TestPublicResponseAndConditionalDI` to verify artifact/index persistence and MCP.
